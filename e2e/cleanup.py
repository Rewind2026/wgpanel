#!/usr/bin/env python3
"""删除端到端测试产生的客户端。

只删测试专用的名字前缀，绝不碰真实数据。
面板是单人部署，没有测试账号这一说，故不清理账号。
"""
import json
import os
import urllib.error
import urllib.request

BASE = os.environ.get("WGP_BASE_URL", "http://127.0.0.1:8080")
OWNER = (
    os.environ.get("WGP_OWNER_USER", "admin"),
    os.environ.get("WGP_OWNER_PASS", ""),
)

# 需要清理的客户端名前缀
PEER_PREFIXES = ("e2e_", "ui_", "qa_", "tn_probe", "ext_")


def call(method, path, body=None, token=None):
    data = json.dumps(body).encode() if body is not None else None
    headers = {"Content-Type": "application/json"}
    if token:
        headers["Authorization"] = "Bearer " + token
    req = urllib.request.Request(BASE + path, data=data, headers=headers, method=method)
    try:
        with urllib.request.urlopen(req, timeout=20) as r:
            raw = r.read()
            return r.status, (json.loads(raw) if raw else None)
    except urllib.error.HTTPError as e:
        raw = e.read()
        try:
            return e.code, json.loads(raw)
        except Exception:
            return e.code, raw.decode()[:300]


def items(payload):
    data = payload.get("data") if isinstance(payload, dict) else payload
    return data["items"] if isinstance(data, dict) else (data or [])


def main():
    st, d = call("POST", "/api/v1/auth/login",
                 {"username": OWNER[0], "password": OWNER[1]})
    if st != 200:
        raise SystemExit("owner 登录失败：%s %s" % (st, d))
    token = d["data"]["accessToken"]

    st, d = call("GET", "/api/v1/peers?pageSize=500", token=token)
    for p in items(d):
        if str(p.get("name", "")).startswith(PEER_PREFIXES):
            st, _ = call("DELETE", "/api/v1/peers/%s" % p["id"], token=token)
            print("删除 peer %s %s -> %s" % (p["id"], p["name"], st))

    st, d = call("GET", "/api/v1/peers?pageSize=500", token=token)
    print("剩余 peer：%s" % [p["name"] for p in items(d)])


if __name__ == "__main__":
    main()
