# WGPanel

> 生产可用级 WireGuard VPN Docker 部署方案 —— 现代化 Web 管理 UI、全参数可视化配置、实时监控、单命令一键部署、原生兼容 Docker bridge 网络。

[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

---

## 这是什么

WGPanel 是一个单镜像、单卷、单命令即可跑起来的 WireGuard 管理面板。它把 WireGuard 的**全部核心参数**暴露成可视化控件，内置**实时监控面板**，并从第一天就设计了**审计日志**——这些都是现有同类项目（wg-easy / wg-quick / Headscale）没有覆盖的。

| 能力 | 说明 |
|---|---|
| 全参数可视化 | ListenPort / 监听地址 / MTU / DNS / AllowedIPs / FwMark / RouteTable / Keepalive / PSK 全部可配，改动热生效 |
| 实时监控 | CPU、内存、磁盘、网卡、隧道吞吐、每客户端上下行速率与累计流量、连接时长、最近握手；历史曲线 1h / 24h / 7d / 30d |
| 带宽与生命周期 | 每客户端流量配额（按周期结算、超限自动停用）、`tc` 上下行限速、到期自动禁用与预警 |
| 批量与订阅 | 批量吊销 / 启停 / 打标签、CSV 导入导出、订阅链接供客户端一键拉取 |
| 实时事件 | SSE 推送上下线、超限、到期、备份等事件到界面，无需外部通知服务 |
| 备份恢复 | 逻辑快照 AES-256-GCM 加密为单个 `.wgbak`，支持定时备份、一键恢复 |
| 一键部署 | `docker run` 一条命令，无需数据库、无需监控栈、无需预装 wireguard-tools |
| bridge 原生 | 容器内独立 netns 完成路由与 NAT，不需要 `--network host` |
| 声明式调谐 | 数据库是期望态，内核状态漂移（外部 `wg set` 改动、容器异常退出）自动修复 |
| 单账号 + 审计 | 单账号部署（自助改密），所有写操作进审计日志并可导出 CSV |

## 快速开始

### 方式一：安装脚本（推荐）

```bash
curl -fsSL https://raw.githubusercontent.com/Rewind2026/wgpanel/main/install.sh | sudo bash
```

脚本会生成随机初始账号密码、启动容器，并打印面板地址。

### 方式二：docker run

```bash
docker run -d --name wgpanel \
  --restart unless-stopped \
  --cap-drop ALL --cap-add NET_ADMIN \
  --device /dev/net/tun \
  --sysctl net.ipv4.ip_forward=1 \
  --sysctl net.ipv4.conf.all.src_valid_mark=1 \
  -p 51820:51820/udp -p 8080:8080/tcp \
  -v wgpanel-data:/data \
  -e WGP_ADMIN_PASSWORD='换成你的强密码' \
  ghcr.io/rewind2026/wgpanel:latest
```

> 宿主内核**尚未**加载 `wireguard` 模块时，面板会拒绝就绪并在日志中提示 `modprobe wireguard`。
> 不默认授予 `SYS_MODULE`：该权限等价于宿主 root，能加载任意内核模块。确有需要时由你在宿主侧加载模块。

### 方式三：docker compose

```bash
cp .env.example .env    # 按需修改，默认值即可运行
docker compose up -d
```

打开 `http://<服务器IP>:8080`，用 `admin` + 你设置的密码登录。

### 方式四：Kubernetes / 裸机

见 [`deploy/`](deploy/README.md)：包含 Helm Chart、Kustomize base + production overlay、systemd unit，
以及[反向代理与 TLS 配置](deploy/reverse-proxy/README.md)。

### 前置条件

- 宿主内核已加载 `wireguard` 模块：`sudo modprobe wireguard`
- Docker 20.10+（非 root 容器需要该版本起才把 `--cap-add` 放进 ambient 集合）

## 本地开发

```bash
# 后端（需要 Go 1.24+）
go build ./... && go test ./...

# 前端（需要 Node 22+）
cd web && npm install && npm run build

# 开发模式：后端 8080 + 前端热更新 5173
go run ./cmd/wgpanel            # 终端 1
cd web && npm run dev           # 终端 2
```

前端产物输出到 `internal/web/dist` 并被 `go:embed` 编译进二进制，因此**生产镜像里没有任何前端静态目录**。

## 配置

全部配置项见 [.env.example](.env.example)。

**先看这条**：WireGuard 的服务端参数（端口、网段、MTU 等）**不是由环境变量
决定的，而是存在数据库里**。环境变量只在数据库尚无记录的那一次启动时作为
初始值写入，之后无论怎么改 env、怎么重启容器，生效的永远是数据库里的值——
包括你在「服务端设置」页面上改的那些。

想改这些参数，去界面上的**服务端设置**页改，保存即通过 netlink 热更新，
不需要重启容器。删库重初始化能重新读 env，但会连同所有客户端、账号与
审计记录一起丢失。

| 变量 | 默认值 | 说明 |
|---|---|---|
| `WGP_ADMIN_USERNAME` / `WGP_ADMIN_PASSWORD` | `admin` / 随机 | 首个 owner 账号；密码留空则随机生成并打印到日志（仅一次）。**仅在库里一个账号都没有时生效**；之后改密码请用界面上的「账号」页 |
| `WGP_PUBLIC_ENDPOINT` | 自动探测 | 生成客户端配置时的 Endpoint；留空则推断容器出口 IP。**这一项每次都实时读取 env，不走数据库**，且在「系统运维」页可改 |
| `WGP_LISTEN_PORT` | `51820` | WireGuard UDP 端口。**仅首次初始化生效**，之后请在服务端设置页修改 |
| `WGP_ADDRESS_CIDR` | `10.8.0.1/24` | 隧道网段。**仅首次初始化生效**；修改后已分配的客户端地址不会自动迁移 |
| `WGP_MTU` | `1420` | 接口 MTU。**仅首次初始化生效**；容器网络下建议 ≤ 宿主 MTU − 80 |
| `WGP_NATIVE_MODE` | `bridge` | `bridge` = 容器内独立 netns；`host` = 使用宿主网络栈 |
| `WGP_SAMPLE_INTERVAL` | `1s` | 监控采样间隔（200ms ~ 10s） |
| `WGP_RECONCILE_INTERVAL` | `2s` | 期望态与内核状态的一致性检查周期 |
| `WGP_TRUSTED_PROXY` | 空 | 可信反向代理网段。**不要填 `0.0.0.0/0`**，否则任何人可伪造来源 IP 绕过登录限流 |
| `WGP_COOKIE_SECURE` | `0` | 面板位于 TLS 反向代理之后时置 `1`；直接 HTTP 暴露时置 `1` 会导致登录态丢失 |
| `WGP_MASTER_KEY` | 自动生成 | 私钥加密主密钥；**更换会导致历史私钥无法解密** |

## 数据与备份

所有状态都在卷 `/data` 内：

```
/data/wgpanel.db       SQLite（WAL）
/data/master.key       私钥加密主密钥
/data/runtime.secret   JWT 签名密钥
/data/backups/         面板生成的加密备份（.wgbak）
```

**推荐：使用面板内的备份功能。** 系统运维页「立即备份」或定时任务会导出逻辑快照，
用主密钥 AES-256-GCM 加密为单个 `.wgbak`，可下载、可一键恢复。
之所以不直接复制 `.db`：WAL 模式下只拷主库会丢掉尚未 checkpoint 的事务，恢复后数据不一致。

**冷备（停机后整目录打包）也可以**，但三个文件必须一起保存：

```bash
docker run --rm -v wgpanel-data:/data -v "$PWD:/backup" alpine \
  tar czf /backup/wgpanel-backup.tar.gz -C /data .
```

恢复时把压缩包解回 `/data` 并重启容器，Reconciler 会自动重建内核状态。

> 只拷 `wgpanel.db` 而丢掉 `master.key` 会导致所有 WireGuard 私钥不可解密。面板检测到主密钥缺失时会拒绝启动，而不是静默重建一个空的主密钥——那只会让所有历史私钥永久不可用。

## 项目结构

```
wgpanel/
├── ARCHITECTURE.md          架构设计：技术栈、模块拆分、部署架构、数据存储、权限体系
├── ROADMAP.md               功能路线图与验收标准
├── CHANGELOG.md             变更记录
├── CONTRIBUTING.md          贡献指南（含真实命令与包依赖规则）
├── cmd/wgpanel/             程序入口与生命周期
├── internal/
│   ├── config/              配置加载与校验（WGP_* 环境变量）
│   ├── domain/              领域模型与权限矩阵
│   ├── store/               SQLite 持久化与 10 版顺序迁移
│   ├── authn/               加密、JWT、登录限流
│   ├── wgdriver/            WireGuard 内核控制（wgctrl）
│   ├── netdriver/           网络控制面：链路/路由/NAT/fwmark/tc 限速
│   ├── ipam/                隧道地址分配
│   ├── policy/              配额周期结算、到期判定、自动恢复
│   ├── backup/              AES-256-GCM 加密备份的生成与恢复
│   ├── eventbus/            进程内事件总线
│   ├── reqmeta/             请求来源元数据（IP / User-Agent），供审计落库
│   ├── service/             应用服务层
│   ├── reconciler/          声明式状态调谐
│   ├── monitor/             指标采集
│   ├── scheduler/           降采样、清理、定时备份
│   ├── httpapi/             REST + SSE + Prometheus 传输层
│   ├── web/                 前端资源嵌入（go:embed）
│   └── version/             版本信息
├── deploy/                  Helm Chart / Kustomize / systemd unit / 反向代理文档
├── .github/                 CI、Release、交付物校验、Issue/PR 模板
├── e2e/                     端到端测试（API / 隧道 / UI）
└── web/                     React 19 + TypeScript 前端
    └── public/              站点图标（favicon.svg 为主源，PNG/ICO 兜底）
```

## 当前进度

M0–M6 已落地：核心链路、Web UI、实时监控、审计、加密备份、配额/限速/到期、
批量与订阅、多架构镜像与 Helm/Kustomize/systemd 交付物均已完成，
本机 `gofmt` / `go vet` / `go test` / `tsc --noEmit` / `npm run build` / `helm lint` / `kustomize build` 全绿。

M7 真机验证已在 `192.168.50.63` 完成：容器冒烟、崩溃自愈、性能基准（240 Peer）、
多架构镜像与 Helm/Kustomize/systemd 交付物均已通过，浏览器端 7 个页面全部实测通过。
长稳一项原定 72 小时采样，已按要求取消，改用进程内加速泄漏验证 + 45 分钟真机采样
（11 轮，未见线性增长，但样本不足以判定长稳通过）。
树莓派 arm 实测与 Lighthouse 未做。逐项清单见 [ROADMAP.md](ROADMAP.md)。

## 文档

| 文档 | 内容 |
|---|---|
| [ARCHITECTURE.md](ARCHITECTURE.md) | 技术栈选型理由、模块拆分、ADR、数据模型、API 概览、风险 |
| [ROADMAP.md](ROADMAP.md) | 里程碑、验收标准、测试覆盖率现状、后续方向 |
| [deploy/README.md](deploy/README.md) | 部署形态选型与共通注意事项 |
| [deploy/reverse-proxy/README.md](deploy/reverse-proxy/README.md) | Nginx / Caddy / Traefik 与 SSE 相关配置 |
| [CHANGELOG.md](CHANGELOG.md) | 变更记录 |
| [CONTRIBUTING.md](CONTRIBUTING.md) | 开发流程与提交规范 |

## 许可

[MIT](LICENSE)