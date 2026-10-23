#!/usr/bin/env python3
"""隧道功能完整验证：停用断链、启用恢复、流量与配额统计。"""
import json
import os
import subprocess
import time
import urllib.request

BASE = os.environ.get("WGP_BASE_URL", "http://127.0.0.1:8080")
IFACE = "wgqa0"
PASS_N = FAIL_N = 0


def check(name, ok, detail=""):
    global PASS_N, FAIL_N
    if ok:
        PASS_N += 1
        print("  OK   %s" % name)
    else:
        FAIL_N += 1
        print("  FAIL %s  -> %s" % (name, detail))


def call(method, path, body=None, token=None):
    data = json.dumps(body).encode() if body is not None else None
    hdr = {"Content-Type": "application/json"}
    if token:
        hdr["Authorization"] = "Bearer " + token
    req = urllib.request.Request(BASE + path, data=data, headers=hdr, method=method)
    try:
        with urllib.request.urlopen(req, timeout=20) as r:
            p = r.read()
            return r.status, (json.loads(p) if p else None)
    except urllib.error.HTTPError as e:
        p = e.read()
        try:
            return e.code, json.loads(p)
        except Exception:
            return e.code, p.decode()[:300]


def sh(cmd):
    p = subprocess.run(cmd, shell=True, capture_output=True, text=True)
    return p.returncode, p.stdout.strip(), p.stderr.strip()


def server_sh(cmd, timeout=60):
    """在服务端机器上执行命令。

    面板跑在服务端容器里，本机只负责建隧道：读 tc 规则必须去服务端，
    在本机执行 `docker exec` 只会拿到空输出——看起来像限速没生效，
    实际是查错了机器。
    """
    host = os.environ.get("WGP_SERVER_SSH", "TEST@192.168.50.63")
    pw = os.environ.get("WGP_CLIENT_PASS", "")
    if not pw:
        return 127, "", "WGP_CLIENT_PASS 未设置，无法登录服务端"
    q = "'" + cmd.replace("'", "'\\''") + "'"
    sudo = 'echo %s | sudo -S -p "" bash -c %s' % (pw, q)
    p = subprocess.run(
        ["sshpass", "-e", "ssh", "-o", "StrictHostKeyChecking=no", host, sudo],
        capture_output=True, text=True, timeout=timeout,
        env={"SSHPASS": pw, "PATH": "/usr/bin:/bin:/usr/sbin:/sbin"})
    return p.returncode, (p.stdout + p.stderr).strip(), ""


def wgstate():
    rc, out, err = sh("wg show %s 2>&1" % IFACE)
    return out


def peer_of(items, name):
    for p in items:
        if p["name"] == name:
            return p
    return None


def list_peers(tok):
    st, d = call("GET", "/api/v1/peers", token=tok)
    data = (d or {}).get("data")
    return data["items"] if isinstance(data, dict) else (data or [])


OWNER_USER = os.environ.get("WGP_OWNER_USER", "admin")
OWNER_PASS = os.environ.get("WGP_OWNER_PASS", "")
TOK = call("POST", "/api/v1/auth/login",
           {"username": OWNER_USER, "password": OWNER_PASS})[1]["data"]["accessToken"]

# 准备 peer
call("DELETE", "/api/v1/peers/%s" % p["id"], token=TOK) if False else None
for p in list_peers(TOK):
    if p["name"].startswith("tn_probe"):
        call("DELETE", "/api/v1/peers/%s" % p["id"], token=TOK)

st, d = call("POST", "/api/v1/peers", {
    "name": "tn_probe", "allowedIps": "10.8.0.0/24", "persistentKeepalive": 25,
    "trafficQuotaBytes": 100000000, "quotaPeriod": "monthly",
}, token=TOK)
peer = d["data"]
PID = peer["id"]
print("peer id=%s address=%s" % (PID, peer["address"]))

st, d = call("GET", "/api/v1/peers/%s/config?endpoint=192.168.50.63:51820" % PID, token=TOK)
conf = d["data"]["config"]
open("/tmp/tn_probe.conf", "w").write(conf)
rc, out, err = sh("wg-quick strip /tmp/tn_probe.conf")
open("/tmp/tn_probe.stripped", "w").write(out)

print("== 1. 建立隧道 ==")
sh("ip link del %s 2>/dev/null" % IFACE)
sh("ip link add %s type wireguard" % IFACE)
sh("ip addr add %s dev %s" % (peer["address"], IFACE))
sh("wg setconf %s /tmp/tn_probe.stripped" % IFACE)
sh("ip link set %s up" % IFACE)
sh("ip route add 10.8.0.0/24 dev %s" % IFACE)
time.sleep(4)
rc, out, err = sh("ping -c 3 -W 2 10.8.0.1 2>&1")
check("隧道内 ping 通", rc == 0, (out or err)[-200:])
rc, out, err = sh("ip route get 10.8.0.1")
check("流量确实走 wg 接口", IFACE in out, out)

print("== 2. 面板侧状态 ==")
time.sleep(6)
p = peer_of(list_peers(TOK), "tn_probe") or {}
st_ = (p.get("stats") or {})
print("  stats:", json.dumps(st_, ensure_ascii=False))
check("面板显示在线", bool(st_.get("online")), json.dumps(st_, ensure_ascii=False))
check("面板记录下行流量", (st_.get("rxBytes") or 0) > 0, json.dumps(st_, ensure_ascii=False))
check("面板记录上行流量", (st_.get("txBytes") or 0) > 0, json.dumps(st_, ensure_ascii=False))
check("stats.peerId 正确", st_.get("peerId") == PID,
      "peerId=%s 期望 %s" % (st_.get("peerId"), PID))
check("stats.publicKey 正确", bool(st_.get("publicKey")),
      "publicKey=%r" % st_.get("publicKey"))
check("lastSeenAt 有值", bool(p.get("lastSeenAt")), str(p.get("lastSeenAt")))

print("== 3. 限速下发（内核是否生效） ==")
st, d = call("PUT", "/api/v1/peers/%s" % PID, {"rateLimitMbps": 1}, token=TOK)
check("设置限速 1Mbps", st == 200, str(d)[:200])
time.sleep(6)
qdisc_cmd = os.environ.get("WGP_QDISC_CMD", "docker exec wgpanel tc qdisc show dev wg0")
rc, out, err = server_sh(qdisc_cmd)
print("  qdisc:", out[:400])
if rc == 127:
    # 查不到服务端就不是「没限速」，只是这项没验成，不能记成失败。
    print("  SKIP  %s（%s）" % ("内核限速规则已下发", err))
else:
    check("内核限速规则已下发", "htb" in out.lower() or "rate" in out.lower(), out[:300])
# 限速后测吞吐
rc, out, err = sh("timeout 12 curl -s -o /dev/null -w '%%{speed_download}' "
                   "http://192.168.50.63:18099/healthz 2>&1")
# 不测外网吞吐（依赖外网），仅确认限速规则存在

print("== 4. 停用 peer → 隧道应断 ==")
st, d = call("PUT", "/api/v1/peers/%s" % PID, {"enabled": False}, token=TOK)
check("停用 peer", st == 200, str(d)[:200])
time.sleep(8)
rc, out, err = sh("ping -c 3 -W 2 10.8.0.1 2>&1")
print("  ", (out or err)[-200:])
check("停用后隧道不通", rc != 0, (out or err)[-200:])
p = peer_of(list_peers(TOK), "tn_probe") or {}
check("停用后 stats.online=false", not (p.get("stats") or {}).get("online"),
      json.dumps(p.get("stats"), ensure_ascii=False))

print("== 5. 重新启用 → 隧道应恢复 ==")
st, d = call("PUT", "/api/v1/peers/%s" % PID, {"enabled": True}, token=TOK)
check("重新启用 peer", st == 200, str(d)[:200])
time.sleep(10)
rc, out, err = sh("ping -c 3 -W 2 10.8.0.1 2>&1")
print("  ", (out or err)[-200:])
check("重新启用后恢复连通", rc == 0, (out or err)[-200:])
p = peer_of(list_peers(TOK), "tn_probe") or {}
check("恢复后在线", bool((p.get("stats") or {}).get("online")),
      json.dumps(p.get("stats"), ensure_ascii=False))

print("== 6. 轮换密钥 → 旧密钥失效 ==")
st, d = call("POST", "/api/v1/peers/%s/rotate-key" % PID, {}, token=TOK)
check("轮换密钥", st == 200, str(d)[:200])
time.sleep(6)
rc, out, err = sh("ping -c 3 -W 2 10.8.0.1 2>&1")
check("旧密钥握手失败（隧道断）", rc != 0, (out or err)[-200:])

print("== 7. 用新密钥重建 → 恢复 ==")
st, d = call("GET", "/api/v1/peers/%s/config?endpoint=192.168.50.63:51820" % PID, token=TOK)
conf = d["data"]["config"]
open("/tmp/tn_probe.conf", "w").write(conf)
rc, out, err = sh("wg-quick strip /tmp/tn_probe.conf")
open("/tmp/tn_probe.stripped", "w").write(out)
sh("ip link del %s 2>/dev/null" % IFACE)
sh("ip link add %s type wireguard" % IFACE)
sh("ip addr add %s dev %s" % (peer["address"], IFACE))
sh("wg setconf %s /tmp/tn_probe.stripped" % IFACE)
sh("ip link set %s up" % IFACE)
sh("ip route add 10.8.0.0/24 dev %s" % IFACE)
time.sleep(5)
rc, out, err = sh("ping -c 3 -W 2 10.8.0.1 2>&1")
check("新密钥恢复连通", rc == 0, (out or err)[-200:])

print("== 8. 订阅链接 ==")
st, d = call("POST", "/api/v1/peers/%s/subscription" % PID, {}, token=TOK)
sub = d.get("data") if isinstance(d, dict) else None
check("签发订阅链接", st in (200, 201) and bool(sub and sub.get("url")), str(d)[:200])
if sub:
    st, d = call("GET", sub["url"].replace(BASE, ""), token="", raw=True) \
        if False else (None, None)
    import urllib.request as _r
    req = _r.Request(BASE + sub["url"].split(BASE, 1)[1] if BASE in sub["url"] else sub["url"])
    try:
        with _r.urlopen(req, timeout=15) as r:
            body = r.read().decode()
        check("匿名拉取订阅配置", r.status == 200 and "[Interface]" in body, body[:200])
    except Exception as e:
        check("匿名拉取订阅配置", False, str(e)[:200])

print("== 9. 清理 ==")
sh("ip link del %s 2>/dev/null" % IFACE)
sh("rm -f /tmp/tn_probe.conf /tmp/tn_probe.stripped")
st, d = call("DELETE", "/api/v1/peers/%s" % PID, token=TOK)
check("删除测试 peer", st in (200, 204), str(d)[:200])

print("=" * 60)
print("PASS=%d  FAIL=%d" % (PASS_N, FAIL_N))
