#!/usr/bin/env python3
"""WGPanel 后端 API 回归测试（字段名/路由/响应结构均按源码校正）。"""
import json
import os
import time
import urllib.request
import urllib.error

BASE = os.environ.get("WGP_BASE_URL", "http://127.0.0.1:8080")
OWNER = (
    os.environ.get("WGP_OWNER_USER", "admin"),
    os.environ.get("WGP_OWNER_PASS", ""),
)
TOK = {}


def call(method, path, body=None, token=None, raw=False, ctype="application/json", cookie=None):
    url = BASE + path
    if isinstance(body, (bytes, bytearray)):
        data = bytes(body)
    elif body is not None:
        data = json.dumps(body).encode()
    else:
        data = None
    hdr = {"Content-Type": ctype}
    # 统一走浏览器登录拿到的 JWT；token="" 表示匿名请求（如订阅链接）。
    t = token if token is not None else TOK.get("access")
    if t:
        hdr["Authorization"] = "Bearer " + t
    if cookie:
        hdr["Cookie"] = cookie
    req = urllib.request.Request(url, data=data, headers=hdr, method=method)
    try:
        r = urllib.request.urlopen(req, timeout=30)
        b = r.read()
        if raw:
            return r.status, b, dict(r.headers)
        return r.status, (json.loads(b) if b else None), dict(r.headers)
    except urllib.error.HTTPError as e:
        b = e.read()
        if raw:
            return e.code, b, dict(e.headers)
        try:
            return e.code, json.loads(b), dict(e.headers)
        except Exception:
            return e.code, b[:300].decode("utf-8", "replace"), dict(e.headers)
    except Exception as e:
        return "ERR", str(e), {}


PASS, FAIL = [], []


def check(name, cond, detail=""):
    (PASS if cond else FAIL).append(name)
    print(("  OK   " if cond else "  FAIL ") + name + ("" if cond else "  -> " + str(detail)[:300]))


def unwrap(d):
    """统一拆包：{code,message,data} 或分页 {data:{items,total}}。"""
    if not isinstance(d, dict):
        return d
    data = d.get("data", d)
    if isinstance(data, dict) and "items" in data:
        return data["items"]
    return data


def as_list(d):
    """把可能分页/包裹的响应统一成 list。"""
    v = unwrap(d)
    if isinstance(v, list):
        return v
    return []


def login(username, password):
    st, d, h = call("POST", "/api/v1/auth/login", {"username": username, "password": password},
                    token="", raw=False)
    return st, d, h


def cookie_from(headers, name="wgp_refresh"):
    raw = headers.get("Set-Cookie", "")
    for part in raw.split(","):
        part = part.strip()
        if part.startswith(name + "="):
            return part.split(";")[0]
    return ""


print("== 0. 登录与清理 ==")
st, d, h = login(*OWNER)
check("owner 登录", st == 200 and isinstance(d, dict) and d.get("code") == 0, (st, d))
if st != 200:
    raise SystemExit("登录失败，终止")
TOK["access"] = d["data"]["accessToken"]
TOK["refresh"] = cookie_from(h)
check("登录响应带 refresh Cookie", bool(TOK["refresh"]), h.get("Set-Cookie"))

# 清掉历史残留，保证可重复执行
st, d, _ = call("GET", "/api/v1/peers")
for p in as_list(d):
    if str(p.get("name", "")).startswith("qa_"):
        call("DELETE", "/api/v1/peers/%s" % p["id"])
st, d, _ = call("GET", "/api/v1/peers")
check("残留 qa_ peer 已清理", not [p for p in as_list(d) if str(p.get("name", "")).startswith("qa_")],
      [p.get("name") for p in as_list(d)])

print("== 1. 认证 ==")
st, d, _ = call("GET", "/api/v1/auth/me")
check("GET /auth/me", st == 200 and unwrap(d).get("username") == OWNER[0], (st, d))

st, d, _ = call("POST", "/api/v1/auth/login", {"username": OWNER[0], "password": "wrongpassword123"})
check("错误密码被拒 401", st == 401, (st, d))

st, d, _ = call("GET", "/api/v1/auth/me", token="not-a-real-token")
check("无效 token 被拒 401", st == 401, (st, d))

st, d, h2 = call("POST", "/api/v1/auth/refresh", None, token="", cookie=TOK["refresh"])
check("refresh 换新 token", st == 200 and d.get("code") == 0, (st, d))
if st == 200:
    TOK["access"] = d["data"]["accessToken"]
    newcookie = cookie_from(h2)
    check("refresh 轮换了 refresh Cookie", bool(newcookie) and newcookie != TOK["refresh"], newcookie)
    TOK["refresh"] = newcookie or TOK["refresh"]

st, d, _ = call("POST", "/api/v1/auth/refresh", None, token="", cookie="wgp_refresh=bogus")
check("无效 refresh 被拒 401", st == 401, (st, d))

st, d, _ = call("GET", "/api/v1/version")
check("GET /api/v1/version", st == 200, (st, d))
st, d, _ = call("GET", "/healthz")
check("GET /healthz", st == 200, (st, d))
st, d, _ = call("GET", "/readyz")
check("GET /readyz", st == 200, (st, d))

print("== 2. Peer CRUD ==")
st, d, _ = call("POST", "/api/v1/peers", {
    "name": "qa_test_peer", "allowedIps": "0.0.0.0/0,::/0",
    "persistentKeepalive": 25, "note": "qa", "tags": ["qa"],
    "trafficQuotaBytes": 1048576, "quotaPeriod": "monthly", "rateLimitMbps": 5,
})
check("创建 peer (201, 自动分配地址)", st == 201 and d.get("code") == 0, (st, str(d)[:300]))
peer = unwrap(d) if isinstance(unwrap(d), dict) else {}
pid = peer.get("id")
check("peer id 存在", isinstance(pid, int) and pid > 0, str(d)[:200])
check("peer 地址已分配", bool(peer.get("address")), peer)

st, d, _ = call("GET", "/api/v1/peers")
lst = as_list(d)
check("列出 peers", st == 200 and isinstance(lst, list), (st, str(d)[:150]))
check("新 peer 在列表中", any(p.get("id") == pid for p in lst), "not found")

st, d, _ = call("POST", "/api/v1/peers", {"name": "qa_dup", "address": peer.get("address")})
check("地址冲突被拒 409", st == 409, (st, str(d)[:200]))

st, d, _ = call("POST", "/api/v1/peers", {"name": "qa_dup", "address": "192.168.199.9/32"})
check("地址超出服务端网段被拒", st in (400, 409), (st, str(d)[:200]))

st, d, _ = call("PUT", "/api/v1/peers/%s" % pid, {
    "name": "qa_test_peer", "note": "updated", "trafficQuotaBytes": 2097152,
    "quotaPeriod": "monthly", "rateLimitMbps": 10, "enabled": True,
})
check("更新 peer", st == 200 and d.get("code") == 0, (st, str(d)[:250]))
st, d, _ = call("GET", "/api/v1/peers")
hit = [p for p in as_list(d) if p.get("id") == pid]
check("更新后配额=2MB", hit and hit[0].get("trafficQuotaBytes") == 2097152, hit)
check("更新后限速=10", hit and hit[0].get("rateLimitMbps") == 10, hit)
check("更新后 note=updated", hit and hit[0].get("note") == "updated", hit)

print("== 3. 启停 / 轮换 / 订阅 ==")
st, d, _ = call("PUT", "/api/v1/peers/%s" % pid, {"enabled": False})
check("停用 peer (PUT enabled=false)", st == 200, (st, str(d)[:200]))
st, d, _ = call("GET", "/api/v1/peers")
hit = [p for p in as_list(d) if p.get("id") == pid]
check("停用后 enabled=false", hit and hit[0].get("enabled") is False, hit)
st, d, _ = call("PUT", "/api/v1/peers/%s" % pid, {"enabled": True})
check("重新启用", st == 200, (st, str(d)[:200]))
st, d, _ = call("GET", "/api/v1/peers")
hit = [p for p in as_list(d) if p.get("id") == pid]
check("启用后 enabled=true", hit and hit[0].get("enabled") is True, hit)

st, d, _ = call("GET", "/api/v1/peers/%s/config" % pid, raw=True)
check("导出客户端配置", st == 200 and b"PrivateKey" in d, (st, str(d)[:200]))

st, d, _ = call("POST", "/api/v1/peers/%s/subscription" % pid, {})
check("签发订阅链接 (201)", st == 201 and d.get("code") == 0, (st, str(d)[:250]))
sub = unwrap(d) if isinstance(unwrap(d), dict) else {}
suburl = sub.get("url")
check("订阅链接含 url 与 token", bool(suburl) and bool(sub.get("token")), sub)
if suburl:
    st, d, _ = call("GET", suburl.replace(BASE, ""), token="", raw=True)
    check("订阅链接可匿名拉取配置", st == 200 and b"PrivateKey" in d, (st, str(d)[:150]))
    st, d, _ = call("DELETE", "/api/v1/peers/%s/subscription" % pid)
    check("吊销订阅链接", st in (200, 204), (st, str(d)[:200]))
    st, d, _ = call("GET", suburl.replace(BASE, ""), token="", raw=True)
    check("吊销后订阅链接失效", st in (404, 410, 400), (st, str(d)[:150]))

st, d, _ = call("POST", "/api/v1/peers/%s/rotate-key" % pid, {})
check("轮换密钥", st in (200, 204), (st, str(d)[:250]))

print("== 4. 批量操作 ==")
st, d, _ = call("POST", "/api/v1/peers/bulk-tag", {"ids": [pid], "tag": "qatag", "remove": False})
check("批量打标签", st == 200 and d.get("code") == 0, (st, str(d)[:250]))
st, d, _ = call("GET", "/api/v1/peers")
hit = [p for p in as_list(d) if p.get("id") == pid]
check("标签已生效", hit and "qatag" in (hit[0].get("tags") or []), hit)
st, d, _ = call("POST", "/api/v1/peers/bulk-tag", {"ids": [pid], "tag": "qatag", "remove": True})
check("批量移除标签", st == 200, (st, str(d)[:200]))
st, d, _ = call("GET", "/api/v1/peers")
hit = [p for p in as_list(d) if p.get("id") == pid]
check("标签已移除", hit and "qatag" not in (hit[0].get("tags") or []), hit)
st, d, _ = call("POST", "/api/v1/peers/bulk-enable", {"ids": [pid], "enabled": False})
check("批量停用", st == 200, (st, str(d)[:200]))
st, d, _ = call("GET", "/api/v1/peers")
hit = [p for p in as_list(d) if p.get("id") == pid]
check("批量停用已生效", hit and hit[0].get("enabled") is False, hit)
st, d, _ = call("POST", "/api/v1/peers/bulk-enable", {"ids": [pid], "enabled": True})
check("批量启用", st == 200, (st, str(d)[:200]))
st, d, _ = call("POST", "/api/v1/peers/bulk-revoke", {"ids": [pid]})
check("批量吊销", st == 200, (st, str(d)[:250]))

print("== 5. 导入导出 ==")
st, d, _ = call("GET", "/api/v1/peers/export", raw=True)
check("导出 peers CSV", st == 200 and b"name,address" in d, (st, str(d)[:200]))
check("导出不包含私钥", b"PrivateKey" not in d, "包含私钥")
st, d, _ = call("POST", "/api/v1/peers/import",
                b"name,address,allowedIps,persistentKeepalive,note,trafficQuotaBytes,quotaPeriod,rateLimitMbps,enabled\n"
                b"qa_imp_1,,0.0.0.0/0,25,imp,1048576,monthly,5,true\n"
                b"qa_imp_2,,0.0.0.0/0,25,imp,1048576,monthly,5,true\n"
                b"qa_imp_bad,,0.0.0.0/0,25,imp,1048576,bogus,5,true\n",
                ctype="text/csv")
check("导入 peers CSV", st == 200 and d.get("code") == 0, (st, str(d)[:300]))
imp = unwrap(d) if isinstance(unwrap(d), dict) else {}
check("导入成功 2 条、失败 1 条", imp.get("created") == 2 and imp.get("failed") == 1, imp)
st, d, _ = call("GET", "/api/v1/peers")
check("导入结果出现在列表", len([p for p in as_list(d) if str(p.get("name", "")).startswith("qa_imp_")]) == 2,
      [p.get("name") for p in as_list(d)])

print("== 6. 服务端配置 ==")
st, d, _ = call("GET", "/api/v1/server")
srv = unwrap(d) if isinstance(unwrap(d), dict) else {}
check("读服务端配置", st == 200 and bool(srv.get("interface")), (st, str(d)[:200]))
st, d, _ = call("GET", "/api/v1/server/config", raw=True)
check("读 wg 配置文本", st == 200 and b"[Interface]" in d, (st, str(d)[:200]))
st, d, _ = call("GET", "/api/v1/server/revisions")
check("修订记录列表", st == 200, (st, str(d)[:200]))
# PUT 是全量下发：只传一个字段会被校验拒绝，必须带上完整配置。
srv_full = dict(srv)
srv_full["mtu"] = (srv.get("mtu") or 1420) + 2
st, d, _ = call("PUT", "/api/v1/server", srv_full)
check("更新服务端配置", st == 200, (st, str(d)[:250]))
st, d, _ = call("GET", "/api/v1/server")
check("更新后 mtu 生效", (unwrap(d) or {}).get("mtu") == srv_full["mtu"], unwrap(d))
st, d, _ = call("PUT", "/api/v1/server", {"mtu": 1400})
check("服务端配置缺字段被拒", st >= 400, (st, str(d)[:200]))
srv_full["mtu"] = srv.get("mtu") or 1420
call("PUT", "/api/v1/server", srv_full)

print("== 7. 账号与自助改密 ==")
# 面板是单人部署：没有建号、删号与角色分配，只保留「我自己」。
st, d, _ = call("GET", "/api/v1/account/profile")
prof = unwrap(d) if isinstance(unwrap(d), dict) else {}
check("读账号资料", st == 200 and prof.get("username") == OWNER[0], (st, str(d)[:250]))
check("账号资料含角色", prof.get("role") == "owner", prof)

st, d, _ = call("GET", "/api/v1/account/profile", token="not-a-real-token")
check("未鉴权读账号资料被拒 401", st == 401, (st, str(d)[:200]))

# 用户管理接口必须彻底下线，不能是「隐藏」而是「不存在」。
st, d, _ = call("GET", "/api/v1/users")
check("用户列表接口已下线 404", st == 404, (st, str(d)[:200]))
st, d, _ = call("POST", "/api/v1/users",
                {"username": "qa_viewer", "password": "QaViewer2026Pass", "role": "viewer"})
check("创建用户接口已下线 404", st == 404, (st, str(d)[:200]))

# 改密的入参校验：弱密码、缺字段、多余字段都要被拒。
st, d, _ = call("PUT", "/api/v1/account/password", {"password": "short"})
check("弱密码被拒 400", st == 400, (st, str(d)[:200]))
st, d, _ = call("PUT", "/api/v1/account/password", {})
check("缺字段被拒 400", st == 400, (st, str(d)[:200]))
st, d, _ = call("PUT", "/api/v1/account/password",
                {"password": "QaNewPass2026!x", "userId": 999})
check("请求体带 userId 被拒 400（身份只能取自登录态）", st == 400, (st, str(d)[:200]))

# 改密的完整闭环：会真的改掉 owner 口令，所以必须显式开关。
# 开启后脚本会把口令改回原值，中途失败会留下新口令，故默认关闭。
if os.environ.get("WGP_TEST_PASSWORD_ROTATION") == "1":
    newpass = "QaRotated2026Pass!x"
    oldtok = TOK["access"]
    st, d, _ = call("PUT", "/api/v1/account/password", {"password": newpass})
    check("改密成功", st == 200 and (unwrap(d) or {}).get("changed") is True, (st, str(d)[:250]))
    st, d, _ = call("GET", "/api/v1/peers", token=oldtok)
    check("改密后旧 access token 立即失效", st == 401, (st, str(d)[:200]))
    st, d, hnew = call("POST", "/api/v1/auth/login", {"username": OWNER[0], "password": newpass})
    check("改密后新口令可登录", st == 200, (st, str(d)[:200]))
    if st == 200:
        TOK["access"] = d["data"]["accessToken"]
        TOK["refresh"] = cookie_from(hnew) or TOK["refresh"]
    # 改回原口令，别把设备留在测试口令上。
    st, d, _ = call("PUT", "/api/v1/account/password", {"password": OWNER[1]})
    check("口令可改回原值", st == 200, (st, str(d)[:250]))
    st, d, hback = call("POST", "/api/v1/auth/login", {"username": OWNER[0], "password": OWNER[1]})
    check("改回后原口令可登录", st == 200, (st, str(d)[:200]))
    if st == 200:
        TOK["access"] = d["data"]["accessToken"]
        TOK["refresh"] = cookie_from(hback) or TOK["refresh"]

print("== 8. 监控 / 指标 ==")
for p in ["/api/v1/metrics/overview", "/api/v1/metrics/peers", "/api/v1/metrics/traffic"]:
    st, d, _ = call("GET", p)
    check("GET %s" % p, st == 200, (st, str(d)[:150]))

st, d, _ = call("GET", "/metrics", raw=True)
check("GET /metrics (prometheus 文本)", st == 200 and b"wgpanel" in d.lower(), (st, str(d)[:150]))
st, d, _ = call("GET", "/api/v1/metrics/traffic?range=1h")
check("流量历史带 range 参数", st == 200, (st, str(d)[:150]))

print("== 9. SSE 流 ==")
st, d, _ = call("POST", "/api/v1/auth/stream-ticket", {})
check("签发 stream ticket", st == 200 and d.get("code") == 0, (st, str(d)[:200]))
ticket = (unwrap(d) or {}).get("ticket") if isinstance(unwrap(d), dict) else None
if ticket:
    try:
        req = urllib.request.Request(BASE + "/api/v1/metrics/stream?ticket=" + ticket)
        rr = urllib.request.urlopen(req, timeout=8)
        chunk = rr.read(400)
        check("SSE 可连接并推送事件", b"data:" in chunk or b"event:" in chunk, chunk[:200])
        rr.close()
    except Exception as e:
        check("SSE 可连接并推送事件", False, e)
    try:
        req = urllib.request.Request(BASE + "/api/v1/metrics/stream?ticket=bogus")
        urllib.request.urlopen(req, timeout=8)
        check("无效 ticket 被拒", False, "连接成功")
    except urllib.error.HTTPError as e:
        check("无效 ticket 被拒", e.code in (401, 403), e.code)
    except Exception as e:
        check("无效 ticket 被拒", False, e)

print("== 10. 审计 ==")
st, d, _ = call("GET", "/api/v1/audit?page=1&pageSize=5")
items = as_list(d)
check("审计列表", st == 200 and isinstance(items, list) and len(items) > 0, (st, str(d)[:200]))
page = (d or {}).get("data") if isinstance(d, dict) else None
check("审计分页 total 存在", isinstance(page, dict) and "total" in page, page)
st, d, _ = call("GET", "/api/v1/audit/export", raw=True)
check("审计导出 CSV", st == 200 and len(d) > 50, (st, len(d) if isinstance(d, bytes) else str(d)[:150]))
st, d, _ = call("GET", "/api/v1/audit?action=peer.created")
check("审计按 action 过滤", st == 200, (st, str(d)[:150]))
st, d, _ = call("GET", "/api/v1/audit?from=notatime")
check("审计非法时间参数报错", st == 400, (st, str(d)[:150]))

print("== 11. 设置 ==")
st, d, _ = call("GET", "/api/v1/settings")
setg = unwrap(d) if isinstance(unwrap(d), dict) else {}
check("读设置", st == 200 and bool(setg), (st, str(d)[:200]))
if setg:
    old = dict(setg)
    setg["sampleIntervalSeconds"] = 5
    st, d, _ = call("PUT", "/api/v1/settings", setg)
    check("写设置", st == 200, (st, str(d)[:250]))
    st, d, _ = call("GET", "/api/v1/settings")
    check("采样间隔即时生效", (unwrap(d) or {}).get("sampleIntervalSeconds") == 5, unwrap(d))
    bad = dict(old)
    bad["sampleIntervalSeconds"] = 99
    st, d, _ = call("PUT", "/api/v1/settings", bad)
    check("越界设置被拒 400", st == 400, (st, str(d)[:200]))
    call("PUT", "/api/v1/settings", old)

print("== 12. 备份 / 还原 ==")
st, d, _ = call("POST", "/api/v1/backups", {})
check("创建备份 (201)", st == 201 and d.get("code") == 0, (st, str(d)[:250]))
bf = unwrap(d) if isinstance(unwrap(d), dict) else {}
fname = bf.get("filename")
st, d, _ = call("GET", "/api/v1/backups")
check("备份列表", st == 200 and isinstance(as_list(d), list), (st, str(d)[:150]))
if fname:
    st, d, _ = call("GET", "/api/v1/backups/%s" % fname, raw=True)
    check("下载备份文件", st == 200 and len(d) > 100, (st, len(d) if isinstance(d, bytes) else d))
    st, d, _ = call("POST", "/api/v1/backups/%s/restore" % fname, {})
    check("还原备份", st in (200, 204), (st, str(d)[:250]))
    st, d, _ = call("DELETE", "/api/v1/backups/%s" % fname)
    check("删除备份", st in (200, 204), (st, str(d)[:200]))
st, d, _ = call("GET", "/api/v1/backups/../../etc/passwd", raw=True)
check("备份路径穿越被拒", st in (400, 404), (st, str(d)[:150]))

print("== 13. 通知出口已下线 ==")
# Webhook 整条链路下线：接口必须不存在，而不是「能调但没人用」。
st, d, _ = call("GET", "/api/v1/webhooks")
check("webhook 列表接口已下线 404", st == 404, (st, str(d)[:200]))
st, d, _ = call("POST", "/api/v1/webhooks",
                {"name": "qa_hook", "kind": "generic", "url": "https://example.com/hook",
                 "events": ["peer.created"], "enabled": False})
check("创建 webhook 接口已下线 404", st == 404, (st, str(d)[:200]))

print("== 14. 策略引擎：有效期自动停用 ==")
st, d, _ = call("POST", "/api/v1/peers", {
    "name": "qa_expired", "allowedIps": "0.0.0.0/0",
    "expiresAt": "2020-01-01T00:00:00Z",
})
check("创建已过期 peer (201)", st == 201, (st, str(d)[:250]))
exp_pid = (unwrap(d) or {}).get("id") if isinstance(unwrap(d), dict) else None
if exp_pid:
    ok = False
    for _ in range(24):  # 策略引擎默认 1 分钟一轮，最多等 2 分钟
        time.sleep(5)
        st, d, _ = call("GET", "/api/v1/peers")
        hit = [p for p in as_list(d) if p.get("id") == exp_pid]
        if hit and hit[0].get("enabled") is False:
            ok = True
            break
    check("过期 peer 被策略引擎自动停用", ok,
          [p for p in as_list(d) if p.get("id") == exp_pid] if exp_pid else "no peer")

print("== 15. 清理 ==")
st, d, _ = call("GET", "/api/v1/peers")
for p in as_list(d):
    if str(p.get("name", "")).startswith("qa_"):
        st, d, _ = call("DELETE", "/api/v1/peers/%s" % p["id"])
check("删除测试 peer", st in (200, 204), (st, str(d)[:200]))
st, d, _ = call("GET", "/api/v1/peers")
check("无残留 qa_ peer", not [p for p in as_list(d) if str(p.get("name", "")).startswith("qa_")],
      [p.get("name") for p in as_list(d)])

print()
print("=" * 60)
print("PASS=%d  FAIL=%d" % (len(PASS), len(FAIL)))
if FAIL:
    print("失败项:")
    for f in FAIL:
        print("  -", f)
