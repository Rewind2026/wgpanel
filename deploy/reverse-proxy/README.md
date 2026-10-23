# 反向代理与 TLS

面板默认监听明文 HTTP。放到公网时建议前置 TLS 反代，但有两个配置必须同步改对，
否则会出现「登录后 15 分钟被踢回登录页」或「登录限流形同虚设」。

## 两个必改项

| 变量 | 值 | 不改的后果 |
|---|---|---|
| `WGP_COOKIE_SECURE` | `1` | 刷新令牌 Cookie 不带 `Secure`，浏览器在 HTTPS 下也会回传；反过来说，**改成 HTTPS 访问却不设它，浏览器会把 Cookie 丢弃**，表现为登录状态莫名失效 |
| `WGP_TRUSTED_PROXY` | 反代来源网段，如 `10.0.0.0/8` | 面板只看到反代的 IP，所有人共享同一个限流桶，一个人输错密码会锁掉所有人 |

`WGP_TRUSTED_PROXY` **不要**图省事填 `0.0.0.0/0`。
那等于让任何人伪造 `X-Forwarded-For`，登录限流可以直接绕过。

## Caddy

Caddy 会自动申请证书，最省事：

```caddy
wgpanel.example.com {
    reverse_proxy 127.0.0.1:8080
}
```

## Nginx

```nginx
server {
    listen 443 ssl http2;
    server_name wgpanel.example.com;

    ssl_certificate     /etc/letsencrypt/live/wgpanel.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/wgpanel.example.com/privkey.pem;

    add_header Strict-Transport-Security "max-age=31536000" always;

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_http_version 1.1;

        # SSE：必须关缓冲、放开读超时，否则监控页会周期性卡住。
        proxy_set_header Connection '';
        proxy_buffering off;
        proxy_cache off;
        proxy_read_timeout 3600s;

        proxy_set_header Host              $host;
        proxy_set_header X-Real-IP         $remote_addr;
        proxy_set_header X-Forwarded-For   $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }
}

server {
    listen 80;
    server_name wgpanel.example.com;
    return 301 https://$host$request_uri;
}
```

## Traefik（Docker 场景）

```yaml
labels:
  - "traefik.enable=true"
  - "traefik.http.routers.wgpanel.rule=Host(`wgpanel.example.com`)"
  - "traefik.http.routers.wgpanel.entrypoints=websecure"
  - "traefik.http.routers.wgpanel.tls.certresolver=le"
  - "traefik.http.services.wgpanel.loadbalancer.server.port=8080"
```

## WebSocket 与 SSE

面板用的是 SSE（`/api/v1/metrics/stream`），不是 WebSocket。
Nginx 默认的 `proxy_buffering on` 会把事件攒在缓冲区里，
表现为监控页「几十秒才更新一次」，很容易被误判成采集器故障。
上面的配置里 `proxy_buffering off` 就是为此。

## WireGuard 端口不要走反代

UDP 51820 必须直连，HTTP 反代对它无能为力。
反代只保护管理面板（8080/tcp），隧道端口在防火墙上单独开放。