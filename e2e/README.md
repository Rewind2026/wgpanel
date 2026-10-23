# 端到端测试套件

覆盖三层：后端 API 契约、浏览器 UI 交互、真机 WireGuard 隧道。
所有目标地址与凭据都从环境变量读取，脚本本身不含任何硬编码的 IP 或口令。

## 环境变量

| 变量 | 说明 | 默认值 |
| --- | --- | --- |
| `WGP_BASE_URL` | 面板地址 | `http://127.0.0.1:8080` |
| `WGP_OWNER_USER` | 拥有者账号 | `admin` |
| `WGP_OWNER_PASS` | 拥有者口令 | 空 |
| `WGP_TEST_PASSWORD_ROTATION` | 置 `1` 才跑改密闭环（会真的改口令再改回） | 关闭 |
| `WGP_QDISC_CMD` | 读取容器内限速规则的命令 | `docker exec wgpanel tc qdisc show dev wg0` |
| `WGP_CLIENT_HOST` / `WGP_CLIENT_USER` / `WGP_CLIENT_PASS` | 外部第三方客户端所在机器（`ext_client_test.py` 用） | `192.168.50.75` / `root` / 空 |
| `WGP_SERVER_SSH` | 服务端 SSH 目标（读容器内 `tc` 规则用） | `TEST@192.168.50.63` |
| `WGP_CLIENT_PROXY` | 客户端机器上把流量导入隧道的本地代理入口 | `socks5h://127.0.0.1:2080` |
| `WGP_TUNNEL_TARGET` | 隧道内要访问的服务端地址 | `http://10.8.0.1:8080` |
| `WGP_EXT_PEER` | 外部客户端对应的 peer 名 | `ext_client_75` |

## 用法

```bash
export WGP_BASE_URL=http://192.168.50.63:18099
export WGP_OWNER_USER=admin WGP_OWNER_PASS='...'

# 1. API 契约回归
python3 e2e/api_test.py

# 2. UI 视图与交互
cd e2e/ui && npm install && node ui_test.mjs && node ui_interact.mjs

# 3. 真机隧道（需要本机具备 /dev/net/tun 与 root）
python3 e2e/tunnel_test.py

# 4. 外部第三方客户端回归（需要先在客户端机器上起好隧道，见下）
python3 e2e/ext_client_test.py
cd e2e/ui && node ui_ext_client.mjs

# 5. 清理测试数据
python3 e2e/cleanup.py
```

## 外部客户端回归（`ext_client_test.py`）

`tunnel_test.py` 在跑测试的机器本机建隧道，验证的是内核实现；
`ext_client_test.py` 换成**一台干净的第三方机器**，验证的是「真实客户端
连上面板」这条路径，包括面板的观测是否如实、控制是否真的生效。

客户端机器的前提：能建隧道。两种做法二选一。

**A. 有 `/dev/net/tun` 与 wireguard 内核模块** —— 用 `wg-quick` 起，
配置从面板导出（记得先在「系统运维 → 公网出口地址」填好出口，
否则导出配置里没有 `Endpoint`，客户端不会主动发起握手）。

**B. 只有 `/dev/net/tun`、没有内核模块** —— 用纯用户态实现，例如 sing-box：

```json
{
  "log": { "level": "debug" },
  "inbounds": [
    { "type": "mixed", "tag": "socks-in", "listen": "127.0.0.1", "listen_port": 2080 }
  ],
  "outbounds": [
    {
      "type": "wireguard", "tag": "wg-out",
      "server": "192.168.50.63", "server_port": 51820,
      "local_address": [ "10.8.0.77/32" ],
      "private_key": "<面板导出的 PrivateKey>",
      "peer_public_key": "<服务端 PublicKey>",
      "mtu": 1420, "workers": 2, "reserved": [ 0, 0, 0 ]
    },
    { "type": "direct", "tag": "direct" }
  ],
  "route": {
    "rules": [ { "inbound": "socks-in", "outbound": "wg-out" } ],
    "final": "direct"
  }
}
```

两个容易踩的点：

- `server` 只能写 IP，端口放 `server_port`。写成 `"1.2.3.4:51820"`
  会报 `no known endpoint for peer`，握手永远发不出去。
- LXC 容器即使 `mknod /dev/net/tun c 10 200` 成功，seccomp 仍会在
  `open()` 时返回 EPERM（`ip tuntap add` 会直接报 `Operation not permitted`）。
  这类环境只能走 B 方案。

覆盖：握手与在线状态、流量增长、限速是否落到 `tc`、停用断链、
启用恢复、密钥轮换致旧密钥失效、订阅签发与匿名拉取、
导出配置是否可直接交付客户端。

## 各脚本覆盖范围

- `api_test.py` —— 登录、CRUD、批量操作、限速与配额、导入导出、
  备份还原、账号资料与自助改密、审计、设置、SSE 推送、策略引擎。
  另含两条「功能确已下线」的断言：`/api/v1/users` 与 `/api/v1/webhooks`
  必须返回 404——是「从代码层面删掉」，不是「在界面上藏起来」。
- `cleanup.py` —— 清测试客户端，只按名字前缀删，不碰真实数据。
- `ui/ui_test.mjs` —— 全页渲染、移动端溢出、控制台错误、截图。
- `ui/ui_interact.mjs` —— 页面内建客户端、搜索、批量启停、编辑、
  复制、删除、审计回查、SSE、退出登录。
- `tunnel_test.py` —— 真实建隧道、验流量确实走 wg 接口、
  停用断链、启用恢复、轮换密钥、订阅链接拉取。限速断言经 `WGP_SERVER_SSH`
  到服务端读 `tc` 规则：面板跑在服务端容器里，测试机本机 `docker exec` 只会拿到空输出，
  看起来像限速没生效，实际是查错了机器。
- `ext_client_test.py` / `ui/ui_ext_client.mjs` —— 外部第三方客户端
  连上面板时的观测与控制回归，详见上一节。

## 已知契约

面板已移除 API 令牌（`X-API-Token` 头与 `/api/v1/tokens` 系列接口均不存在），
所有脚本统一按下面这套约定调用：

- **唯一的鉴权方式是 `Authorization: Bearer <accessToken>`**，没有第二种凭据。
- accessToken 来自 `POST /api/v1/auth/login` 的 `data.accessToken`，
  即「用户在浏览器里登录」拿到的那个 JWT；脚本开头都用它登录一次再复用。
- 续期走 `POST /api/v1/auth/refresh`，凭 HttpOnly 的 `wgp_refresh` Cookie
  换新的 accessToken（登录响应会带 `Set-Cookie`，需原样带回）。
- **订阅链接是唯一可以匿名访问的接口**：`GET /api/v1/subscription/<token>`
  不带任何认证头即可拉取 WireGuard 配置。签发/吊销订阅链接本身仍需要 Bearer。
- 失效的 accessToken 返回 401/403；`GET /api/v1/auth/me` 可用来验证当前令牌。

## 账号约定

面板是单人部署：没有建号、删号与角色分配，也没有 `/api/v1/users`。
与账号相关的只有两个接口，身份一律取自登录态，请求体里没有也不能有 userId：

| 接口 | 说明 |
| --- | --- |
| `GET /api/v1/account/profile` | 读当前账号资料 |
| `PUT /api/v1/account/password` | 改当前账号密码，请求体 `{"password":"..."}` |

改密后该账号的全部会话立即失效（含已签发的 access token），
所以 `api_test.py` 的改密闭环默认关闭，需要时用
`WGP_TEST_PASSWORD_ROTATION=1` 显式开启——它会真的改掉口令再改回来。

## 命名约定

测试数据一律带专用前缀（`e2e_`、`qa_`、`ui_`、`tn_probe`、`ext_`），
`cleanup.py` 按前缀清理。改前缀时记得同步 `cleanup.py` 里的常量。
