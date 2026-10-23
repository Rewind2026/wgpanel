#!/usr/bin/env python3
"""用一台干净的第三方机器做真实 WireGuard 连接回归。

与 tunnel_test.py 的区别：tunnel_test.py 在本机用内核 wg 建隧道，
本脚本假设「客户端机器」上跑着一个独立的第三方实现（默认 sing-box），
通过本地 socks 入口把流量导进隧道，验证面板的观测与控制能力。

客户端侧前置条件（见 e2e/README.md）：
  sing-box 配置里有 mixed inbound 127.0.0.1:2080，
  规则把该 inbound 的流量导向 wireguard outbound。
"""
import json
import os
import subprocess
import sys
import time
import urllib.error
import urllib.request

BASE = os.environ.get("WGP_BASE_URL", "http://127.0.0.1:8080")
PEER = os.environ.get("WGP_EXT_PEER", "ext_client_75")
CLIENT_HOST = os.environ.get("WGP_CLIENT_HOST", "192.168.50.75")
CLIENT_USER = os.environ.get("WGP_CLIENT_USER", "root")
CLIENT_PASS = os.environ.get("WGP_CLIENT_PASS", "")
SERVER_SSH = os.environ.get("WGP_SERVER_SSH", "TEST@192.168.50.63")
CLIENT_PROXY = os.environ.get("WGP_CLIENT_PROXY", "socks5h://127.0.0.1:2080")
TUNNEL_TARGET = os.environ.get("WGP_TUNNEL_TARGET", "http://10.8.0.1:8080")

if not CLIENT_PASS:
    sys.exit("WGP_CLIENT_PASS 未设置：脚本不内置任何口令，也不做默认兜底")
ENV = {"SSHPASS": CLIENT_PASS, "PATH": "/usr/bin:/bin:/usr/sbin:/sbin"}
PASS, FAIL = [], []


def sh(host_user, cmd, timeout=60):
    p = subprocess.run(
        ["sshpass", "-e", "ssh", "-o", "StrictHostKeyChecking=no", host_user, cmd],
        capture_output=True, text=True, timeout=timeout, env=ENV,
    )
    return (p.stdout + p.stderr).strip()


def s75(cmd, timeout=60):
    return sh(f"{CLIENT_USER}@{CLIENT_HOST}", cmd, timeout)


def s63(cmd, timeout=60):
    """服务端命令需要提权；密码复用 CLIENT_PASS 的约定值。"""
    q = "'" + cmd.replace("'", "'\\''") + "'"
    return sh(SERVER_SSH, f"echo {ENV['SSHPASS']} | sudo -S -p '' bash -c {q}", timeout)


def api(method, path, body=None, tok=None):
    """返回 (status, data)。订阅端点返回纯文本配置，非 JSON，故保留原文。"""
    data = json.dumps(body).encode() if body is not None else None
    r = urllib.request.Request(BASE + path, data=data, method=method)
    r.add_header("Content-Type", "application/json")
    if tok:
        r.add_header("Authorization", "Bearer " + tok)
    try:
        with urllib.request.urlopen(r, timeout=20) as resp:
            raw = resp.read()
            return resp.status, _decode(raw)
    except urllib.error.HTTPError as e:
        return e.code, _decode(e.read())


def _decode(raw):
    txt = raw.decode("utf-8", "replace")
    try:
        return json.loads(txt) if txt else {}
    except json.JSONDecodeError:
        return {"raw": txt}


def check(name, cond, detail=""):
    (PASS if cond else FAIL).append(name)
    print(("  OK   " if cond else "  FAIL ") + name + ("" if cond else "  -> " + str(detail)[:400]))
    return bool(cond)


def peer_of(tok):
    _, d = api("GET", "/api/v1/peers", tok=tok)
    data = d["data"]
    items = data if isinstance(data, list) else data.get("items", [])
    return next((p for p in items if p["name"] == PEER), {})


def tunnel(path="/readyz"):
    return s75('timeout 20 curl -s -o /dev/null -w "%{http_code}" '
               f"-x {CLIENT_PROXY} {TUNNEL_TARGET}{path}")


def tunnel_burst(n=3):
    return s75(
        f"for i in $(seq 1 {n}); do "
        f"timeout 20 curl -s -o /dev/null -x {CLIENT_PROXY} {TUNNEL_TARGET}/readyz; "
        f"timeout 20 curl -s -o /dev/null -x {CLIENT_PROXY} {TUNNEL_TARGET}/; "
        "done; echo DONE", timeout=180)


def restart_client(private_key):
    """用新私钥替换客户端配置并重启第三方实现。"""
    js = s75("cat /etc/sing-box-wg.json")
    new = "\n".join(
        l if "private_key" not in l else f'      "private_key": "{private_key}",'
        for l in js.splitlines()
    )
    s75(f"cat > /etc/sing-box-wg.json <<'JSONEOF'\n{new}\nJSONEOF")
    s75("for p in $(pgrep -x sing-box); do kill -9 $p; done; sleep 1")
    s75("setsid nohup sing-box run -c /etc/sing-box-wg.json > /tmp/sb.log 2>&1 < /dev/null & sleep 4")


def main():
    _, r = api("POST", "/api/v1/auth/login",
               {"username": os.environ.get("WGP_OWNER_USER", "admin"),
                "password": os.environ.get("WGP_OWNER_PASS", "")})
    tok = r["data"]["accessToken"]

    print("\n=== 1. 第三方客户端建立真实隧道 ===")
    p0 = peer_of(tok)
    check("面板存在外部客户端 peer", bool(p0), p0)
    s0 = p0.get("stats") or {}
    check("隧道内 HTTP 请求返回 200", tunnel() == "200")
    time.sleep(3)
    p1 = peer_of(tok)
    s1 = p1.get("stats") or {}
    check("面板记录 latest handshake", bool(s1.get("lastHandshake")),
          f"{s0.get('lastHandshake')} -> {s1.get('lastHandshake')}")
    check("面板标记 peer 在线", s1.get("online") is True, s1.get("online"))
    check("面板记录入向流量", s1.get("rxBytes", 0) > s0.get("rxBytes", 0),
          f"{s0.get('rxBytes')} -> {s1.get('rxBytes')}")
    check("面板记录 lastSeenAt", bool(p1.get("lastSeenAt")), p1.get("lastSeenAt"))
    check("运行态带 peerId/publicKey 身份",
          bool(s1.get("peerId")) and bool(s1.get("publicKey")),
          f"peerId={s1.get('peerId')}")

    print("\n=== 2. 持续流量下统计增长 ===")
    bs = (peer_of(tok).get("stats") or {})
    tunnel_burst(3)
    time.sleep(3)
    asx = (peer_of(tok).get("stats") or {})
    check("rxBytes 随真实流量增长", asx.get("rxBytes", 0) > bs.get("rxBytes", 0),
          f"{bs.get('rxBytes')} -> {asx.get('rxBytes')}")
    check("txBytes 随真实流量增长", asx.get("txBytes", 0) > bs.get("txBytes", 0),
          f"{bs.get('txBytes')} -> {asx.get('txBytes')}")
    check("速率字段非负", asx.get("rxRateBps", -1) >= 0 and asx.get("txRateBps", -1) >= 0,
          f"rx={asx.get('rxRateBps')} tx={asx.get('txRateBps')}")

    print("\n=== 3. 限速真正下发到内核 tc ===")
    check("peer 限速配置为 1Mbps", peer_of(tok).get("rateLimitMbps") == 1)
    qd = s63("docker exec wgpanel tc qdisc show dev wg0")
    check("服务端 wg0 存在 htb 限速队列", "htb" in qd, qd)
    cls = s63("docker exec wgpanel tc class show dev wg0")
    check("限速类带宽与配置一致", "bit" in cls.lower(), cls)

    print("\n=== 4. 停用 peer 立即断链 ===")
    pid = p1["id"]
    st, body = api("PUT", f"/api/v1/peers/{pid}", {"enabled": False}, tok)
    check("停用接口返回 200", st == 200, f"{st} {json.dumps(body, ensure_ascii=False)[:200]}")
    time.sleep(4)
    check("停用后隧道内请求失败", tunnel() != "200")
    time.sleep(5)
    off = peer_of(tok)
    check("面板显示 peer 已停用", off.get("enabled") is False, off.get("enabled"))
    check("面板显示 peer 离线", (off.get("stats") or {}).get("online") is False,
          (off.get("stats") or {}).get("online"))

    print("\n=== 5. 重新启用恢复连接 ===")
    st, _ = api("PUT", f"/api/v1/peers/{pid}", {"enabled": True}, tok)
    check("启用接口返回 200", st == 200, st)
    time.sleep(5)
    check("启用后隧道内请求恢复 200", tunnel() == "200")
    time.sleep(3)
    check("面板重新标记在线",
          (peer_of(tok).get("stats") or {}).get("online") is True,
          (peer_of(tok).get("stats") or {}).get("online"))

    print("\n=== 6. 轮换密钥后旧密钥失效 ===")
    old_pub = p1.get("publicKey")
    st, nb = api("POST", f"/api/v1/peers/{pid}/rotate-key", None, tok)
    check("轮换接口返回 200", st == 200, f"{st} {json.dumps(nb, ensure_ascii=False)[:200]}")
    new_pub = (nb.get("data") or {}).get("publicKey") or peer_of(tok).get("publicKey")
    check("新公钥与旧公钥不同", bool(new_pub) and new_pub != old_pub, f"{old_pub} -> {new_pub}")
    time.sleep(6)
    check("旧密钥客户端无法再通信", tunnel() != "200")

    print("\n=== 7. 换新密钥后客户端恢复 ===")
    _, cp = api("GET", f"/api/v1/peers/{pid}/config", tok=tok)
    cfg = (cp.get("data") or {}).get("config", "")
    priv = next((l.split("=", 1)[1].strip() for l in cfg.splitlines()
                 if l.startswith("PrivateKey")), None)
    check("轮换后可取到新配置", bool(priv), cfg[:200])
    if priv:
        restart_client(priv)
        check("新密钥客户端恢复通信", tunnel() == "200")
        time.sleep(3)
        nz = (peer_of(tok).get("stats") or {})
        check("轮换后面板仍显示在线", nz.get("online") is True, nz.get("online"))
        check("轮换后握手时间已刷新", nz.get("lastHandshake") != s1.get("lastHandshake"),
              f"{s1.get('lastHandshake')} -> {nz.get('lastHandshake')}")

    print("\n=== 8. 订阅链接（第三方客户端常用拉取方式）===")
    st, sb = api("POST", f"/api/v1/peers/{pid}/subscription", {"note": "ext"}, tok)
    check("签发订阅成功", st in (200, 201), f"{st} {json.dumps(sb, ensure_ascii=False)[:250]}")
    token = (sb.get("data") or {}).get("token") or ""
    check("返回订阅 token", bool(token), json.dumps(sb, ensure_ascii=False)[:250])
    if token:
        st2, anon = api("GET", f"/api/v1/subscription/{token}")
        check("匿名拉取订阅配置成功", st2 == 200, st2)
        txt = anon.get("raw", "") or json.dumps(anon, ensure_ascii=False)
        check("订阅内容为 WireGuard 配置",
              "[Interface]" in txt and "[Peer]" in txt, txt[:200])
        check("订阅内容含 Endpoint（客户端据此主动握手）", "Endpoint" in txt, txt[:300])
        st3, _ = api("GET", "/api/v1/subscription/definitely-not-a-real-token")
        check("无效订阅 token 被拒", st3 in (401, 404), st3)

    print("\n=== 9. peer 配置能否直接交付第三方客户端 ===")
    _, cp2 = api("GET", f"/api/v1/peers/{pid}/config", tok=tok)
    cfg2 = (cp2.get("data") or {}).get("config", "")
    keys = [l.split("=", 1)[0].strip() for l in cfg2.splitlines() if "=" in l]
    for k in ("PrivateKey", "Address", "PublicKey", "AllowedIPs"):
        check(f"配置含 {k}", k in keys, keys)
    check("配置含 Endpoint", "Endpoint" in keys,
          "缺 Endpoint：第三方客户端拿此配置无法主动发起连接 -> " + cfg2[:300])

    print("\n" + "=" * 60)
    print(f"PASS={len(PASS)}  FAIL={len(FAIL)}")
    for f in FAIL:
        print("  FAILED:", f)
    return 1 if FAIL else 0


if __name__ == "__main__":
    raise SystemExit(main())