# WGPanel 项目架构设计文档

> 生产可用级 WireGuard VPN Docker 部署方案 —— 现代化 Web 管理 UI + 全参数可视化配置 + 实时监控面板 + 单命令一键部署 + host / ipvlan / macvlan 三种网络模式（L2 直挂 IPv6 走 RA 下发）

| 项 | 值 |
|---|---|
| 项目代号 | WGPanel |
| License | MIT |
| 后端 | Go 1.24.6（单静态二进制，`CGO_ENABLED=0`） |
| 前端 | React 19 + TypeScript 5 + Vite 6 + Tailwind CSS 3 + shadcn 风格组件 |
| 存储 | SQLite（WAL 模式，纯 Go 驱动 `modernc.org/sqlite`，零 CGO） |
| 内核交互 | `golang.zx2c4.com/wireguard/wgctrl`（netlink）+ `vishvananda/netlink` |
| 镜像形态 | 多阶段构建 → `alpine:3.20` + 单二进制 + 内嵌前端静态资源 |
| 运行环境 | 非 root（uid 10001）+ 仅 `CAP_NET_ADMIN` |
| 支持架构 | `linux/amd64`、`linux/arm64`、`linux/arm/v7` |

> 环境变量前缀统一为 `WGP_`（如 `WGP_DATA_DIR`、`WGP_ADMIN_PASSWORD`）。
> 早期草稿里的 `WGPANEL_` 前缀未落地，实现与本文档均以 `WGP_` 为准。

---

## 1. 设计目标与差异化定位

### 1.1 与现有同类项目的差异

| 维度 | wg-easy / wg-quick / Headscale | **WGPanel** |
|---|---|---|
| UI | 简易表单 / 纯文本 | 现代化暗色玻璃拟态仪表盘，全响应式（桌面 + 移动端） |
| 参数暴露 | 极简，参数写死在 compose | **全量 wgctrl 参数可视化**：ListenPort / MTU / DNS / SaveConfig / AllowedIPs / PersistentKeepalive / 预共享密钥 / 路由表与 fwmark |
| 监控 | 无 / 仅 `wg show` | SSE 实时推送：CPU/内存/磁盘/网络 IO、隧道总吞吐、每 Peer 握手时间、上下行字节、在线状态、连接时长、IP 分配、历史曲线（1h/24h/7d/30d） |
| 部署 | 依赖 `NET_ADMIN` + 手动配置 | 单命令：`docker run -d --network host -v wgp-data:/data --cap-add=NET_ADMIN --device /dev/net/tun -e WGP_NATIVE_MODE=host ghcr.io/rewind2026/wgpanel:latest` |
| 网络模式 | 多数强制 host 网络 | **三种部署模式**：host 共享宿主栈；ipvlan / macvlan 直挂物理二层、IPv6 由路由器 RA/SLAAC 下发。原 bridge 模式已移除——RA 到不了 bridge 容器，IPv6 无法直连 |
| 账号 | 无 / 单用户密码 | 单账号模型：首个 owner 由 `WGP_ADMIN_USERNAME` / `WGP_ADMIN_PASSWORD` 初始化，此后只能自助改密；账号相关操作全部进审计日志 |
| 配置持久化 | wg0.conf 覆盖式 | SQLite 事务化存储 + 自动生成 wg 配置 + 配置版本历史与回滚 |

### 1.2 非功能指标（SLO）

| 指标 | 目标 |
|---|---|
| 冷启动到 API 可用 | < 800 ms |
| 单二进制内存占用（空闲） | < 60 MB RSS |
| REST API P99 延迟 | < 30 ms（240 Peer 实测最高 13.61 ms） |
| 监控数据刷新间隔 | 1 s（可配置 0.5 s ~ 10 s） |
| Peer 增删改生效延迟 | < 200 ms（不重启接口，走 netlink 热更新） |
| 前端首屏可交互 | < 1.5 s（4G 网络，gzip 后 < 400 KB） |
| 可用性 | 单点无状态外部依赖，故障自动重建（systemd-like restart policy） |
| 单面板 Peer 上限 | **253**（见下方说明，非可调的性能参数） |

> **Peer 上限 253 是 IPAM 的硬约束，不是性能目标。** 网段只接受 `/24 ~ /30`，
> `/24` 的可用主机位是 `.1 ~ .254`，扣掉服务端自身占的 `.1` 后只剩 253 个可分配
> 地址，换更大的网段会被 `ipam.NewAllocator` 直接拒绝。因此压测基线一律取
> **240**（上限的 95%）；真机实测即在此规模下完成。该数字由
> `TestIPAMCeilingIsTheRealPeerLimit` 钉死，改动前必须同步改测试。

---

## 2. 技术栈选型

### 2.1 后端

| 关注点 | 选型 | 选型理由 |
|---|---|---|
| 语言 | **Go 1.24** | 静态编译单文件、goroutine 天然适配长连接推送、WireGuard 生态原生 Go 实现、交叉编译无依赖 |
| HTTP 路由 | **chi v5** | 轻量（~10KB）、零反射中间件栈、路由分组与中间件组合最直观，性能对标 Gin |
| 数据库 | **SQLite (modernc.org/sqlite)** | 纯 Go 驱动 → CGO_ENABLED=0 静态二进制 → 可用 `alpine`/`scratch`；单文件持久化契合「无外部依赖」定位；WAL 模式下读写并发足够单机场景 |
| 内核控制 | **wgctrl** | 官方 WireGuard 项目的 Go 控制面库，通过 netlink 操作内核模块；支持热增删 Peer、读握手/流量计数器 |
| 网络配置 | **vishvananda/netlink + 内置 iptables/nft 管理** | 精确控制路由表、fwmark、NAT 规则；支持 iptables-legacy 与 nftables 双后端 |
| 配置渲染 | **wgtypes** | 生成标准 `wg-quick` 配置文件，兼容用户手动 `wg syncconf` / 导出到其他平台 |
| 认证 | **golang-jwt/v5 + argon2id (x/crypto)** | JWT 无状态会话；argon2id 为当前 OWASP 推荐的密码哈希算法 |
| 实时推送 | **SSE** | 监控是单向推送，SSE 由浏览器原生 `EventSource` 自动重连，省掉一套自定义重连/心跳/鉴权握手代码；WebSocket 换来的是客户端上行能力，本项目用不到 |
| 日志 | **log/slog**（标准库） | 结构化日志、零依赖、可按级别动态调整 |
| 指标采集 | `/proc` 直接解析 + `runtime.Metrics` | 容器内无 external exporter 依赖，直接读 `/proc/net/dev`、`/proc/stat`、`/proc/meminfo` |

### 2.2 前端

| 关注点 | 选型 | 选型理由 |
|---|---|---|
| 框架 | **React 19 + TypeScript 5** | 生态最完备、类型安全、团队上手成本最低 |
| 构建 | **Vite 6** | 秒级 HMR，产物自动分包，生产构建 < 10 s |
| 样式 | **Tailwind CSS 3** | 原子化 CSS，产物体积小；设计 token 化（CSS 变量）支持暗色主题 |
| 组件 | **手写 shadcn 风格组件** | 源码级组件（copy-in），完全可控可二次开发，无组件库版本绑架 |
| 图表 | **手写 SVG 图表组件** | 本项目只需单轴时序曲线，引入 ECharts（约 300 KB gzip）为一个折线图不划算；SVG 直接绑定 CSS 变量实现暗色主题 |
| 路由 | **React Router 7（声明式 `<Routes>`）** | 面板路由数量有限且不涉及文件约定，声明式足够，避免引入编译期路由生成步骤；配合 `React.lazy` 做路由级代码分割 |
| 数据层 | **TanStack Query v5** | 缓存/失效/乐观更新/轮询降级，正文面板数据层标准解 |
| 状态 | **React Context + Hooks** | 仅主题、Toast、鉴权三处全局状态，上下文足够，无需额外状态库 |
| 移动端 | **Tailwind 断点 + 抽屉式侧边栏** | 单套代码适配 360px ~ 2560px |

### 2.3 基础设施

| 关注点 | 选型 |
|---|---|
| 镜像 | 多阶段 Dockerfile：`node:22-alpine` 构建前端 → `golang:1.24-alpine` 构建后端（CGO_ENABLED=0） → `alpine:3.20` 运行时 |
| 编排 | `docker compose`（v2 内置 `docker compose up -d`），无依赖 Compose 编排其它中间件 |
| 反向代理 | 可选。用户可直接暴露 8080，也可用 Nginx/Caddy 前置做 TLS |

---

## 3. 系统架构与模块拆分

### 3.1 整体架构

```
┌──────────────────────────────────────────────────────────────────────────┐
│                          Docker Container (single)                        │
│                                                                          │
│  ┌─────────────────────────── WGPanel (Go 单进程) ─────────────────────┐ │
│  │                                                                    │ │
│  │  ┌──────────┐  ┌──────────┐  ┌──────────┐  ┌───────────────────┐  │ │
│  │  │ httpapi  │  │ SSE 推送 │  │  authn   │  │   scheduler       │  │ │
│  │  │ REST 路由│  │ eventbus │  │ JWT/Argon│  │ 流量聚合/清理/备份 │  │ │
│  │  └────┬─────┘  └────┬─────┘  └────┬─────┘  └────────┬──────────┘  │ │
│  │       │             │             │                  │             │ │
│  │       │                                                  │             │ │
│  │       └─────────────┴────────────┴─────────────────────┘             │ │
│  │                         │ 应用层                                  │ │
│  │  ┌──────────────────────┴───────────────────────┐                 │ │
│  │  │  service: auth / server / peer / account │                 │ │
│  │  │  policy(配额/到期) · backup(备份恢复)          │                 │ │
│  │  └───────┬───────────────────┬───────────────────┘                 │ │
│  │          │ repository         │                                    │ │
│  │  ┌───────┴────────┐  ┌───────┴─────────┐                          │ │
│  │  │ store (SQLite) │  │ reconciler      │                          │ │
│  │  │ 10 版顺序迁移   │  │ 声明式状态调谐   │                          │ │
│  │  └────────────────┘  └───┬─────────┬────┘                          │ │
│  │                          │         │                               │ │
│  │  ┌───────────────────────┴──┐  ┌───┴──────────┐                   │ │
│  │  │ wgdriver (wgctrl netlink)│  │ netdriver    │                   │ │
│  │  │ 设备生命周期/Peer 热更新 │  │ 路由/NAT/fwmark│                  │ │
│  │  └──────────────────────────┘  └──────────────┘                   │ │
│  └────────────────────────────────┬───────────────────────────────────┘ │
│                                   │ Linux syscall / netlink           │
│  ┌────────────────────────────────┴───────────────────────────────────┐ │
│  │  wg0 虚拟接口  ·  netns  ·  nftables/iptables  ·  /proc 指标源      │ │
│  └────────────────────────────────────────────────────────────────────┘ │
└──────────────────────────────────┬───────────────────────────────────────┘
                                   │
                          ┌────────┴────────┐
                          │  eth0（host / ipvlan / macvlan 模式直挂链路）│
                          └────────┬────────┘
                                   │
                    ┌──────────────┴───────────────┐
                    │  宿主机:51820/udp  ·  宿主机:8080/tcp │
                    └──────────────────────────────────┘
```

### 3.2 Go 模块划分（`internal/` 包）

```
wgpanel/
├── cmd/wgpanel/main.go              # 唯一入口：加载配置 → 初始化存储 → 启动引擎 → 启动 HTTP → 监听信号
├── internal/
│   ├── config/config.go             #   Config 结构体 + 环境变量覆盖默认值 + 合法性校验
│   │
│   ├── domain/                      # 领域模型（纯数据结构，零上游依赖）
│   │   ├── server.go                #   ServerConfig：全量 WireGuard 参数
│   │   ├── peer.go                  #   Peer：客户端配置 + 运行态 + 配额/到期语义
│   │   ├── user.go                  #   User / Role / Permission / 权限矩阵
│   │   ├── policy.go                #   事件类型（配额超限、到期、备份等）
│   │   ├── settings.go              #   Settings / Backup 的领域模型
│   │   ├── metrics.go               #   MetricsSnapshot / PeerStats / TrafficPoint
│   │   ├── audit.go                 #   AuditEvent
│   │   └── errors.go                #   错误构造与 HTTP 状态码映射
│   │
│   ├── store/                       # 持久化层
│   │   ├── store.go                 #   Store 门面：读写双连接池、事务、备份用转储
│   │   ├── schema.go                #   10 版顺序迁移的 DDL（init → … → drop_api_token → drop_webhook）
│   │   ├── migrate.go               #   迁移调度（只追加，不修改历史项）
│   │   ├── server_repo.go           #   服务端配置 CRUD + 版本历史
│   │   ├── peer_repo.go             #   Peer CRUD / 批量更新 / 订阅令牌
│   │   ├── user_repo.go             #   账号、刷新令牌
│   │   ├── traffic_repo.go          #   流量写入、降采样、四档窗口查询
│   │   ├── settings_repo.go         #   app_setting / backup_record
│   │   └── audit_repo.go            #   审计写入与检索
│   │
│   ├── wgdriver/                    # WireGuard 控制面驱动（内核面）
│   │   ├── driver.go                #   Driver 接口定义（可 mock）
│   │   ├── linux.go                #   wgctrl 实现：设备创建/销毁/配置应用/Peer 热更新
│   │   ├── stub.go                  #   驱动不可用时的管理态 stub
│   │   ├── factory.go               #   非 Linux 平台的占位实现
│   │   ├── render.go                #   wg-quick 配置渲染器（导出/兼容外部工具）
│   │   └── convert.go               #   domain ↔ wgtypes 转换
│   │
│   ├── netdriver/                   # 网络配置驱动（与 wgdriver 职责分离）
│   │   ├── netdriver.go             #   NetDriver 接口 + 能力探测（Capabilities）
│   │   ├── linux.go                 #   netlink 实现：链路/地址/MTU/路由表/fwmark/NAT/转发开关
│   │   ├── ratelimit_linux.go       #   tc HTB 树的 per-Peer 上下行限速
│   │   ├── stub.go / factory_*.go   #   非 Linux 平台与测试替身
│   │
│   ├── ipam/allocator.go            #   CIDR 内最小可用地址分配（并发安全、冲突检测）
│   │
│   ├── authn/                       #   加密、JWT、登录限流
│   │   ├── crypto.go                #     AES-256-GCM 主密钥与静态密钥加解密
│   │   ├── jwt.go                   #     访问令牌签发与校验
│   │   └── ratelimit.go / subtle.go #     令牌桶限流、常量时间比较
│   │
│   ├── service/                     # 应用服务层
│   │   ├── auth_service.go          #   登录/刷新/登出/owner 初始化
│   │   ├── server_service.go        #   服务端参数校验与热更新
│   │   ├── peer_service.go          #   Peer 生命周期（DB + 驱动编排 + 配置渲染）
│   │   ├── peer_bulk.go             #   批量吊销/标签/启停/CSV 导入导出
│   │   └── account_service.go       #   当前账号资料与自助改密
│   │
│   ├── policy/policy.go             #   配额周期结算、到期判定、预警与自动恢复
│   ├── backup/backup.go             #   AES-256-GCM 加密备份的生成、列表、恢复与删除
│   ├── eventbus/eventbus.go         #   进程内事件总线（非阻塞投递，唯一出口）
│   ├── reqmeta/reqmeta.go           #   请求来源元数据（IP / User-Agent），审计落库时统一补齐
│   │
│   ├── reconciler/                  # 声明式调谐器（核心设计）
│   │   ├── reconciler.go            #   期望态(DB) vs 实际态(内核) 差异检测 → 自动修复
│   │   ├── link.go                  #   外部链路漂移修复
│   │   └── ratelimit.go             #   限速树重放与失败上报
│   │
│   ├── monitor/                     # 采集器
│   │   ├── collector.go             #   指标采集循环 + 配额累加 + 上下线判定
│   │   ├── proc.go                  #   /proc 解析（CPU/内存/磁盘/netdev/uptime）
│   │   └── events.go                #   领域事件发布
│   │
│   ├── httpapi/                     # HTTP 传输层
│   │   ├── router.go                #   chi 路由树装配 + SPA 回退
│   │   ├── middleware.go            #   请求 ID / 日志 / 超时 / 安全响应头
│   │   ├── authenticator.go         #   JWT 校验
│   │   ├── response.go              #   统一响应包络与错误映射
│   │   ├── handlers_auth.go         #   登录/刷新/登出/健康探针/就绪探针
│   │   ├── handlers_server.go       #   服务端配置、修订历史、私钥轮换、conf 导出
│   │   ├── handlers_peers_bulk.go   #   批量操作、CSV 导入导出
│   │   ├── handlers_account.go      #   当前账号资料、自助改密
│   │   ├── handlers_system.go       #   设置、备份恢复
│   │   ├── handlers_metrics.go      #   指标与历史流量
│   │   ├── handlers_realtime.go    #   SSE 流式推送
│   │   ├── handlers_export.go       #   审计导出
│   │   └── handlers_prometheus.go   #   /metrics 文本格式暴露
│   │
│   ├── scheduler/                   # 后台任务
│   │   ├── scheduler.go             #   流量降采样、过期数据清理、出口地址探测
│   │   └── backup.go                #   定时备份循环
│   │
│   ├── web/                         # 前端资源嵌入
│   │   └── embed.go                 #   go:embed dist/*，SPA fallback
│   │
│   └── version/version.go           # 版本信息（ldflags 注入）
```

### 3.3 关键设计决策（ADR 摘要）

**ADR-1：声明式 + 调谐（Reconciler）而非命令式直调**

Peer/Server 的所有变更先落 SQLite（期望态），再由 Reconciler 每 2 s 及事件触发时对比实际内核状态并增量修复。

- 收益：接口幂等；内核状态漂移（如外部 `wg set` 改动、容器意外重启）能自愈；单一事实来源清晰，便于审计与回滚。
- 代价：引入最终一致性窗口（≤2 s）。为交互体验，Peer 增删改接口在返回前**同步等待一次调谐**（`ReconcileNow(ctx, resourceID)`），使 UI 感知为「立即生效」。

声明式也带来一个必须正面回答的问题：数据库里有配置 ≠ 内核里有配置。因此就绪探针必须反映「调谐器最近一次是否真的把期望态写进了内核」，而不能只检查数据库可查、驱动可枚举这类静态条件——那种 `/readyz` 在「每轮同步都在报错」时依然返回 200，而期望配置从未生效。实现上 Reconciler 记录最近一次尝试时间（`LastAttempt`）与错误（`SyncError`），`/readyz` 在 `status.reconcile` 暴露：尚未调谐为 `pending`，最近一次失败为 `degraded: <err>`，连续失败超过 3 个调谐周期（默认 6 s）返回 503，单次成功即清零。留出容忍窗口是因为调谐每 2 s 一轮，链路重建、NAT 检查、限速下发都可能撞上瞬时错误，不该因为一次抖动就把实例判死。

**ADR-2：SQLite 而非外部数据库**

单机节点场景下 SQLite WAL 模式提供足够的并发（读并发无阻塞，写串行化）。收益是无外部依赖，契合「单命令一键启动」核心定位。若未来需多节点 HA，通过 repository 接口抽象，切换 PostgreSQL 不影响 service 层。

具体连接策略：写连接池固定为 1 条（避免写锁竞争导致 SQLITE_BUSY），查询走独立的 4 条只读连接池。WAL 下读不阻塞读、读不阻塞写，若全部查询挤在单连接上，SSE、REST 与调谐器会互相排队，把 P99 延迟拉到采样间隔量级。

**ADR-3：SSE 单通道而非 WebSocket**

监控数据是单向推送，浏览器原生 `EventSource` 自带断线重连，省掉一整套自定义的重连退避、心跳保活与握手鉴权代码。WebSocket 换来的是客户端上行能力，本项目没有任何场景需要。

代价与对策：`EventSource` 构造器不接受自定义请求头（规范限制），所以 SSE 路由无法像其他接口那样直接校验 `Authorization: Bearer`，更不能把 JWT 拼进 URL——那会让访问令牌出现在反向代理访问日志与浏览器历史里。因此引入**一次性短期票据**：

1. 前端用常规 `POST /api/v1/auth/stream-ticket`（带 `Authorization` 头）换一张票据；
2. 票据绑定签发者的 `Principal`、有效期 60 秒、**消费一次即作废**；
3. 前端用 `GET /api/v1/metrics/stream?ticket=...` 建立连接；
4. 票据用尽或过期一律 401，前端重新换票再连。

绑定身份而非只记「已通过」是关键：SSE 路由还原 `Principal` 后照常走 RBAC（`PermMetricsRead`），票据只解决「凭据怎么带上」，不解决「有没有权限看」。另外该路由豁免请求超时中间件，保证长连接不被 30 秒超时掐断。

这不是理论上的谨慎。真机验证时发现该缺陷此前一直存在：SSE 路由挂在通用鉴权组内，而前端 `new EventSource('/api/v1/metrics/stream')` 什么凭据都不带，于是监控页长期处于 401 静默重连状态——界面只显示「未连接」，不报任何错，curl 带 `Authorization` 头的测试却一切正常。文档里当时写着「依赖 Cookie 传递身份」，而代码中根本没有读 Cookie 的路径，文档与实现已经脱节。

**ADR-4：Bridge 网络原生兼容方案**（历史决策，已被 ADR-8 取代：bridge 部署模式已移除）

不使用 `--network host`，容器内完成全部网络配置：

- 通过 `CAP_NET_ADMIN` 在容器 netns 内创建 wireguard 类型链路、绑定地址、设置 MTU 并拉起链路；
- 开启容器 netns 内的 `net.ipv4.ip_forward`；
- NAT 规则用 `netdriver` 以固定参数数组调用 iptables（MASQUERADE / FORWARD / MSS 钳制），**不经过 shell**；
- 需要自定义防火墙规则的场景不开放任意命令钩子（见 ADR-7）；
- host 网络模式仍保留，用于需要保留入站 UDP 源地址的特殊场景。

**ADR-5：JWT 双令牌**

Access Token（15 min，存内存）+ Refresh Token（7 d，HttpOnly Cookie，轮换制）。撤销通过 `refresh_token.revoked` 标记实现，兼顾无状态与服务端可撤销性。

刷新令牌的消费必须是原子的：若拆成「先 SELECT 校验、再 UPDATE 撤销」，两个并发刷新请求都能通过校验，同一令牌会被使用两次。本项目用单条 `UPDATE ... RETURNING` 完成「校验 + 撤销 + 取主」，由数据库保证一次性。

**ADR-6：wgdriver 与 netdriver 职责分离**

`wgctrl` 只负责 WireGuard 控制面（密钥、监听端口、Peer、握手与计数器）；让接口真正能收发包的工作（创建链路、绑定地址、MTU、转发、路由、NAT）由 `netdriver` 通过 netlink 与 iptables 完成。混在一起会让「内核 WireGuard 模块可用」和「网络已打通」这两个独立故障无法区分。

**ADR-7：不提供 PostUp/PostDown 任意命令执行**

早期设计允许配置上下线时执行的 shell 命令。已移除，原因是：面板的网络配置全部走 netlink 与固定参数的 iptables 调用，覆盖了原本需要钩子的场景；而执行用户输入的命令，等同于把容器内的 root shell 交给任何能改服务端配置的角色（Operator 及以上）。保留一个可写却从不执行的字段同样危险——运维会以为它生效了。

**ADR-8：移除 bridge 部署模式，网络形态收敛为 managed / host / ipvlan / macvlan**

bridge 网络是 Docker 自己的独立链路，上游路由器的 RA（链路本地组播）到不了容器，SLAAC 拿不到全局地址；发布宿主 IPv6 端口只能经 DNAT 解决入站握手，隧道内客户端访问 IPv6 互联网（出站数据面）没有出路。而 L2 直挂（ipvlan / macvlan）按 Docker 官方「router-assigned IPv6 addresses」配方，eth0 可直接持有 RA 下发的全局地址。因此：

- Docker 部署模式只剩 host / ipvlan / macvlan，各对应一份完整独立的 compose 文件，`scripts/up.sh` 统一入口与前置校验；
- 原 bridge 的「容器内自管转发 / 路由 / NAT」调谐路径**保留**，更名为 `managed`（K8s Pod、systemd 裸机形态），`netdriver` 行为不变；
- `WGP_NATIVE_MODE=bridge` 在配置校验层直接报错并给出迁移提示，不静默降级；
- L2 模式 IPv6 一律走 RA/SLAAC（端点 sysctls + netns sysctls 组合），不做 NAT66、不经 DHCPv6。

---

## 4. 部署架构设计

### 4.1 镜像构建（多阶段）

```dockerfile
# Stage 1: 前端构建
FROM node:22-alpine AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json* ./
RUN npm ci --no-audit --no-fund || npm install --no-audit --no-fund
COPY web/ ./
RUN npm run build                      # 产出 dist/

# Stage 2: 后端构建（静态 + 交叉编译）
FROM golang:1.24-alpine AS api
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# 前端产物复制进 embed 目录。必须在 COPY . . 之后，否则会被上下文里的占位目录覆盖。
COPY --from=web /src/internal/web/dist ./internal/web/dist
ARG VERSION=dev COMMIT=none BUILD_DATE=unknown
ARG TARGETOS=linux TARGETARCH=amd64 TARGETVARIANT=
# CGO_ENABLED=0 是关键：纯静态二进制可用最小基础镜像。
# 显式指定 GOOS/GOARCH/GOARM，让 amd64、arm64、armv7 共用同一份 Dockerfile。
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} GOARM="${TARGETVARIANT#v}" \
      go build -trimpath -ldflags "-s -w -X .../version.Version=${VERSION}" \
      -o /out/wgpanel ./cmd/wgpanel

# Stage 3: 运行时
FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata iptables ip6tables nftables iproute2 wireguard-tools \
 && addgroup -g 10001 wg && adduser -u 10001 -G wg -D -H wg \
 && mkdir -p /data && chown -R wg:wg /data
COPY --from=api /out/wgpanel /usr/local/bin/wgpanel
ENV WGP_DATA_DIR=/data WGP_LISTEN_ADDR=0.0.0.0:8080 TZ=UTC
VOLUME /data
EXPOSE 8080/tcp 51820/udp
HEALTHCHECK --interval=30s --timeout=5s CMD wget -qO- http://127.0.0.1:8080/healthz || exit 1
# 非 root 运行。面板仍需 CAP_NET_ADMIN；Docker 会把 --cap-add 授予的能力同时放进
# ambient 集合，因此非 root UID 也能保有该能力（要求 Docker ≥ 20.10）。
USER 10001:10001
ENTRYPOINT ["/usr/local/bin/wgpanel"]
```

前端通过 `go:embed` 编译进二进制，运行时层不含任何静态目录。`cap_drop: ALL` + `cap_add: NET_ADMIN` 是最小权限组合。

### 4.2 单命令启动

```bash
docker run -d \
  --name wgpanel \
  --restart unless-stopped \
  --network host \
  --cap-drop ALL --cap-add NET_ADMIN \
  --device /dev/net/tun \
  -e WGP_NATIVE_MODE=host \
  -e WGP_LISTEN_ADDR=0.0.0.0:8080 \
  -e WGP_LISTEN_PORT=51820 \
  -v wgpanel-data:/data \
  -e WGP_ADMIN_PASSWORD=<首次初始化密码，可选> \
  ghcr.io/rewind2026/wgpanel:latest
```

不默认授予 `SYS_MODULE`：加载 `wireguard` 内核模块是宿主的职责，现代发行版的 `wireguard` 包已内置该模块，容器内自行加载反而会把权限面扩大到整个宿主内核。

配套三份按模式独立的 `docker-compose.{host,ipvlan,macvlan}.yml`（唯一服务，无依赖编排，见 ADR-8）与 `install.sh`（生成随机密码 → 以 host 网络启动 → 打印面板地址）。

### 4.3 容器内需要的能力与获取方式

| 能力 | 需求 | 获取 |
|---|---|---|
| 创建 TUN 设备 | `/dev/net/tun` + `CAP_NET_ADMIN` | `--device /dev/net/tun --cap-add NET_ADMIN` |
| 加载 `wireguard` 内核模块（宿主未加载时） | 宿主侧 `modprobe wireguard` | **不**在容器内授予 `CAP_SYS_MODULE`；探测失败时 `/readyz` 返回 503 并在日志中给出该提示 |
| 开启 IP 转发 | 写 `net.ipv4.ip_forward` | L2 compose 的 `sysctls` 段或 `--sysctl net.ipv4.ip_forward=1`（host 模式归属宿主） |
| 配置路由与 NAT | `CAP_NET_ADMIN` + 可写的 `/run` | netlink / iptables-nft；`/run/xtables.lock` 需对运行用户可写（Helm/Kustomize/systemd 均已挂载或预创建） |
| 数据持久化 | 宿主可写卷 | `-v wgpanel-data:/data`（命名卷继承镜像内属主，非 root 开箱即用） |

### 4.4 容器网络下的关键调优

| 问题 | 方案 |
|---|---|
| 容器链路默认 MTU 1500 导致隧道性能损失 | 默认接口 MTU 自动计算：宿主 eth0 MTU − 80（IPv6/WG 开销），并提供 UI 手动覆盖 |
| 双重 NAT 导致 MTU 黑洞 | 开启 UDP MSS clamping（`TCPMSS --clamp-mss-to-pmtu`）在 FORWARD 链 |
| 容器重启后接口丢失 | `reconciler` 启动时检测接口缺失 → 重建设备 → 重新下发 Peer |
| 端口占用 | 启动前探测 51820 是否被占用，冲突时给出明确错误而非静默失败 |
| 时间同步 | `TZ` 环境变量 + 镜像内 tzdata，保证握手时间与图表时间轴正确 |

---

## 5. 数据存储方案

### 5.1 存储引擎

- **SQLite**，`_journal_mode=WAL`、`_synchronous=NORMAL`、`_foreign_keys=ON`、`_busy_timeout=5000`
- 文件位置：`/data/wgpanel.db`（卷挂载）
- 驱动：`modernc.org/sqlite`（纯 Go，`CGO_ENABLED=0`）
- 连接池：`SetMaxOpenConns(1)` 用于写；读连接单独 `SetMaxOpenConns(8)`，避免 WAL 写锁竞争

### 5.2 数据模型（核心表）

以下是 [`internal/store/schema.go`](./internal/store/schema.go) 中的实际 DDL，按迁移版本分块。`init → indexes → traffic → v4 → policy → quota_period_start` 六版顺序执行，`migrate.go` 只追加、不修改历史项。

```sql
-- ① 首批核心表
CREATE TABLE server_config (
  id                     INTEGER PRIMARY KEY CHECK (id = 1),   -- 单行
  interface_name         TEXT    NOT NULL DEFAULT 'wg0',
  listen_port            INTEGER NOT NULL DEFAULT 51820,
  private_key            TEXT    NOT NULL DEFAULT '',           -- 加密存储
  public_key             TEXT    NOT NULL DEFAULT '',
  preshared_key          TEXT    NOT NULL DEFAULT '',           -- 加密存储
  address_cidr           TEXT    NOT NULL DEFAULT '10.8.0.1/24',
  listen_ipv4            TEXT    NOT NULL DEFAULT '',
  listen_ipv6            TEXT    NOT NULL DEFAULT '',
  mtu                    INTEGER NOT NULL DEFAULT 1420,
  fwmark                 INTEGER NOT NULL DEFAULT 0,
  table_name             TEXT    NOT NULL DEFAULT 'auto',       -- auto/off/<表号>
  allowed_ips            TEXT    NOT NULL DEFAULT '0.0.0.0/0',
  dns                    TEXT    NOT NULL DEFAULT '',           -- 下发给客户端
  replace_peers          INTEGER NOT NULL DEFAULT 1,
  save_config            INTEGER NOT NULL DEFAULT 1,
  enable_ipv6            INTEGER NOT NULL DEFAULT 0,
  persistent_keepalive   INTEGER NOT NULL DEFAULT 25,
  revision               INTEGER NOT NULL DEFAULT 1,
  created_at             INTEGER NOT NULL,
  updated_at             INTEGER NOT NULL
);

CREATE TABLE server_config_revision (           -- 配置版本历史，支持回滚
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  revision INTEGER NOT NULL,
  snapshot TEXT NOT NULL,                        -- JSON 全量快照
  operator_id INTEGER,
  created_at INTEGER NOT NULL
);

CREATE TABLE peer (
  id                   INTEGER PRIMARY KEY AUTOINCREMENT,
  name                 TEXT    NOT NULL,
  public_key           TEXT    NOT NULL UNIQUE,
  private_key          TEXT    NOT NULL DEFAULT '',            -- 加密存储
  preshared_key        TEXT    NOT NULL DEFAULT '',            -- 加密存储
  address              TEXT    NOT NULL,                       -- 如 10.8.0.2/32
  allowed_ips          TEXT    NOT NULL DEFAULT '0.0.0.0/0,::/0',
  endpoint             TEXT    NOT NULL DEFAULT '',
  persistent_keepalive INTEGER NOT NULL DEFAULT 25,
  note                 TEXT    NOT NULL DEFAULT '',
  tags                 TEXT    NOT NULL DEFAULT '[]',           -- JSON array
  -- 策略
  traffic_quota_bytes  INTEGER NOT NULL DEFAULT 0,             -- 0 = 不限
  quota_period         TEXT    NOT NULL DEFAULT 'monthly',     -- daily/weekly/monthly/never
  rate_limit_mbps      INTEGER NOT NULL DEFAULT 0,             -- 0 = 不限
  max_connections      INTEGER NOT NULL DEFAULT 0,             -- 0 = 不限
  enabled              INTEGER NOT NULL DEFAULT 1,
  expires_at           INTEGER,                                 -- NULL = 永不过期
  last_seen_at         INTEGER,
  created_at           INTEGER NOT NULL,
  updated_at           INTEGER NOT NULL
);

CREATE TABLE user (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  username      TEXT NOT NULL UNIQUE,
  email         TEXT NOT NULL DEFAULT '',
  password_hash TEXT NOT NULL,                                 -- argon2id
  role          TEXT NOT NULL DEFAULT 'viewer',                -- owner/admin/operator/viewer
  enabled       INTEGER NOT NULL DEFAULT 1,
  last_login_at INTEGER,
  created_at    INTEGER NOT NULL,
  updated_at    INTEGER NOT NULL
);

CREATE TABLE refresh_token (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id    INTEGER NOT NULL REFERENCES user(id) ON DELETE CASCADE,
  token_hash TEXT NOT NULL UNIQUE,
  expires_at INTEGER NOT NULL,
  revoked    INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL
);

CREATE TABLE system_metric (                                    -- 系统指标（低频）
  ts             INTEGER PRIMARY KEY,
  cpu_percent    REAL    NOT NULL DEFAULT 0,
  mem_used_bytes INTEGER NOT NULL DEFAULT 0,
  mem_percent    REAL    NOT NULL DEFAULT 0,
  disk_used_bytes INTEGER NOT NULL DEFAULT 0,
  disk_percent   REAL    NOT NULL DEFAULT 0,
  net_rx_bytes   INTEGER NOT NULL DEFAULT 0,
  net_tx_bytes   INTEGER NOT NULL DEFAULT 0,
  uptime_seconds INTEGER NOT NULL DEFAULT 0,
  goroutines     INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE audit_log (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  actor_id    INTEGER,
  actor_name  TEXT    NOT NULL DEFAULT '',
  action      TEXT    NOT NULL,                               -- peer.create / server.update ...
  resource    TEXT    NOT NULL DEFAULT '',
  resource_id TEXT    NOT NULL DEFAULT '',
  detail      TEXT    NOT NULL DEFAULT '',                    -- JSON
  ip          TEXT    NOT NULL DEFAULT '',
  ua          TEXT    NOT NULL DEFAULT '',
  result      TEXT    NOT NULL DEFAULT 'success',
  created_at  INTEGER NOT NULL
);

CREATE TABLE config_snapshot (                                -- 导入导出历史
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  kind        TEXT NOT NULL,                                  -- peers_csv / wg_conf
  payload     BLOB NOT NULL,
  size        INTEGER NOT NULL DEFAULT 0,
  operator_id INTEGER,
  created_at  INTEGER NOT NULL
);
```

```sql
-- ② 索引。地址唯一索引是 IP 分配的最终防线：
--    即使 ipam 的并发分配出现竞争，数据库层也会拒绝重复地址。
CREATE INDEX        idx_peer_enabled    ON peer(enabled);
CREATE INDEX        idx_peer_created_at ON peer(created_at DESC);
CREATE INDEX        idx_peer_expires_at ON peer(expires_at);
CREATE UNIQUE INDEX idx_peer_address    ON peer(address);
CREATE INDEX        idx_refresh_token_user ON refresh_token(user_id);
CREATE INDEX        idx_refresh_token_exp  ON refresh_token(expires_at);
CREATE INDEX        idx_server_revision_rev ON server_config_revision(revision DESC);
```

```sql
-- ③ 流量时序：原始采样 + 降采样聚合
CREATE TABLE traffic_sample (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  ts           INTEGER NOT NULL,
  peer_id      INTEGER,                                      -- NULL = 服务器汇总
  rx_bytes     INTEGER NOT NULL DEFAULT 0,
  tx_bytes     INTEGER NOT NULL DEFAULT 0,
  rx_pps       INTEGER NOT NULL DEFAULT 0,
  tx_pps       INTEGER NOT NULL DEFAULT 0,
  handshake_ts INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE traffic_rollup (
  bucket    TEXT    NOT NULL,                                -- '10m' | 'hour' | 'day'
  ts        INTEGER NOT NULL,                                -- bucket 起始时间
  peer_id   INTEGER NOT NULL DEFAULT 0,                      -- 0 = 服务器汇总
  rx_bytes  INTEGER NOT NULL DEFAULT 0,
  tx_bytes  INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (bucket, ts, peer_id)                          -- 主键列在 SQLite 中不可为 NULL
);

> `traffic_rollup.peer_id` 为 `NOT NULL DEFAULT 0` 而非可空：它属于复合主键，SQLite 不允许主键列可空。用 `0` 表示服务器汇总行，省掉了查询侧 `IS NULL` 分支。

```sql
-- ④ 移除两处不成立的可配置项（见 ADR-7）
ALTER TABLE server_config DROP COLUMN private_key_algorithm;  -- 密钥套件由内核固定，无运行时开关
ALTER TABLE server_config DROP COLUMN post_up;                -- 不提供任意命令执行
ALTER TABLE server_config DROP COLUMN post_down;
```

```sql
-- ⑤ 策略与运维能力
ALTER TABLE peer ADD COLUMN quota_used_bytes  INTEGER NOT NULL DEFAULT 0;
ALTER TABLE peer ADD COLUMN suspended_by       TEXT    NOT NULL DEFAULT '';  -- quota/expiry/manual
ALTER TABLE peer ADD COLUMN subscription_token TEXT    NOT NULL DEFAULT '';

CREATE TABLE app_setting (          -- 全局设置（保留天数、备份开关等）
  key        TEXT PRIMARY KEY,
  value      TEXT    NOT NULL,
  updated_at INTEGER NOT NULL
);

CREATE TABLE backup_record (
  id             INTEGER PRIMARY KEY AUTOINCREMENT,
  filename       TEXT    NOT NULL,
  size_bytes     INTEGER NOT NULL DEFAULT 0,
  sha256         TEXT    NOT NULL DEFAULT '',
  schema_version INTEGER NOT NULL DEFAULT 0,
  peer_count     INTEGER NOT NULL DEFAULT 0,
  user_count     INTEGER NOT NULL DEFAULT 0,
  kind           TEXT    NOT NULL DEFAULT 'manual',  -- manual/auto
  operator_id    INTEGER,
  created_at     INTEGER NOT NULL
);

CREATE UNIQUE INDEX idx_peer_sub_token ON peer(subscription_token)
  WHERE subscription_token <> '';                          -- 部分唯一索引：空串可重复
```

```sql
-- ⑥ 记录配额已结算到的周期起点
ALTER TABLE peer ADD COLUMN quota_period_start INTEGER NOT NULL DEFAULT 0;
```

> 这一列是必需而非可选的。周期结算的判据只能是「当前周期起点 ≠ 上次结算的起点」。早期用「现在是否在周期起点后两分钟内」这种时间窗口近似，一旦某次判定因数据库繁忙、GC 或容器重启而错过，整个周期的配额就永远不会重置，被停用的 Peer 也永远回不来——且无任何报错，只表现为「配额再也不涨了」。落库后判定是幂等的：重跑一百次也只有第一次生效。

### 5.3 敏感数据保护

| 数据 | 保护方式 |
|---|---|
| WireGuard PrivateKey / PresharedKey | AES-256-GCM 加密存储，主密钥来自 `WGP_MASTER_KEY`；未设置时首次启动生成并写入 `/data/master.key`（权限 0600）。**若 `/data/master.key` 不存在但数据库中已有密文，面板拒绝启动并报错退出**（`authn.ErrMasterKeyLost`），不会静默生成新密钥 |
| 用户密码 | argon2id（time=3, mem=64MB, threads=4, keyLen=32, saltLen=16） |
| JWT Secret / Refresh Token | `WGP_JWT_SECRET` 或启动时随机生成并持久化；JWT 使用 HS256，刷新令牌存 SHA-256 哈希（库中无明文） |
| 备份文件 | 逻辑快照用主密钥 AES-256-GCM 加密为单个 `.wgbak`；换主密钥后旧备份不可恢复，此约束在文档与界面提示中均已写明 |
| 配置导出 | 导出接口强制 RBAC 校验 + 审计留痕。CSV 列固定且**不含任何私钥**，可安全外发 |

### 5.4 数据保留与清理

| 数据 | 原始保留 | 降采样保留 | 清理任务 |
|---|---|---|---|
| 流量采样（10 s 粒度） | 24 h | — | 每小时清理 |
| 流量 rollup 10m | 7 d | — | 每日清理 |
| 流量 rollup hour | 90 d | — | 每日清理 |
| 流量 rollup day | 2 年 | — | 每周清理 |
| system_metric | 7 d | — | 每小时清理 |
| audit_log | 180 d | — | 每日清理 |
| refresh_token | 过期后 7 d | — | 每小时清理 |

### 5.5 备份与恢复

实现位于 [`internal/backup`](./internal/backup) 与 [`internal/scheduler/backup.go`](./internal/scheduler/backup.go)。

**为什么不是「直接复制 .db 文件」**：WAL 模式下只复制主库文件会丢掉 `-wal` 里尚未 checkpoint 的事务，恢复后数据不一致。这里在写事务串行的前提下导出**逻辑数据**，天然得到一致快照。

**为什么用「列名 + 按列有序的值」而不是「领域对象 JSON」**：领域模型带 `omitempty` 与 `json:"-"`（私钥、密码哈希），JSON 往返会把它们丢掉——恢复后所有用户密码失效、所有客户端私钥为空。列有序的转储不含这类语义，值原样进出。

| 环节 | 行为 |
|---|---|
| 备份内容 | `server_config`（单行）、`peer`、`user`、`app_setting`。刻意**不含** `schema_migration`：它描述运行结构而非数据，恢复旧版本号会让后续迁移被跳过 |
| 落盘格式 | 单个 `.wgbak`，AES-256-GCM 加密（主密钥）。带 `FormatVersion`，恢复时不匹配直接拒绝，避免误解密出错误结构 |
| 索引 | 写入 `backup_record`（文件名、大小、SHA-256、schema 版本、Peer/账号数、manual/auto），UI 直接读表 |
| 文件名 | `wgpanel-<kind>-<时间戳>.wgbak`。时间戳只精确到秒，「手动点两次」或「定时与手动同秒触发」会撞名，先写的被覆盖而两条索引指向同一路径，故命中重名时追加序号 |
| 手动 | 系统运维页「立即备份」→ `POST /api/v1/backups` |
| 自动 | scheduler 循环按 `app_setting` 中的开关与间隔生成 `kind=scheduled` 的备份 |
| 下载/恢复/删除 | `GET/POST /api/v1/backups/{filename}`、`DELETE /api/v1/backups/{filename}` |
| 恢复后 | 发 `restore.applied` 事件，Reconciler 依据期望态重建内核状态与限速树 |

整库冷备（面板停机后拷 `/data`）仍可用，但必须**同时**带走 `wgpanel.db`、`master.key` 与运行时密钥文件——只拷 `.db` 会因主密钥缺失导致所有私钥不可解密。

> **跨实例恢复的密钥前提**：`.wgbak` 用主密钥加密，主密钥本身不在备份内
> （有意为之：备份文件泄漏到别处也解不开）。代价是灾备时必须单独保存
> `master.key`，只搬 `.wgbak` 会得到明确的
> `backup cannot be decrypted with the current master key`，而不是一个半可用的实例。
> 真机已验证：带原主密钥跨容器恢复后，服务端与全部 Peer 私钥的 SHA-256
> 指纹与恢复前逐字节一致。**这一项必须用哈希比对来验证**——密钥错配时
> 所有接口仍返回 200、配置照常导出，只有真正建连才会发现客户端全部失效。

---

## 6. 权限体系设计

### 6.1 设计原则

采用 **RBAC（Role-Based Access Control）** 而非细粒度 ACL：角色数量少、权限边界清晰、易审计、易维护，符合本项目「自托管单实例」的场景。

但当前是**单账号部署**：账号只有一个，角色矩阵与权限点保留下来是为了让
「多账号」这件事将来需要时不必重做鉴权，运行时的实际效果是「owner 全权限」。

### 6.2 角色定义

| 角色 | 说明 | 定位 |
|---|---|---|
| **owner** | 唯一所有者，初始管理员 | 不可删除、不可降级 |

> 面板是**单账号部署**：没有建号、删号与角色分配，因此实际存在的账号只有 owner 一个。
> 下面的权限矩阵仍保留 admin / operator / viewer 三档，是为了让「未来若要开多账号」
> 时不必重做鉴权；当前界面上不存在选择角色的入口，后端也没有创建用户的接口。
> owner 的权限集覆盖全部权限点，所以单账号部署下权限判定恒为真——它留在这里
> 是为了让矩阵本身保持完整，而不是因为运行时真的区分四种角色。

### 6.3 权限矩阵

权限点命名 `resource:action`。

| 权限点 | owner | admin | operator | viewer |
|---|:---:|:---:|:---:|:---:|
| `server:read` | ✅ | ✅ | ✅ | ✅ |
| `server:write`（端口/MTU/DNS/路由/算法等全量参数） | ✅ | ✅ | ✅ | ❌ |
| `peer:read` | ✅ | ✅ | ✅ | ✅ |
| `peer:create` / `peer:update` / `peer:delete` | ✅ | ✅ | ✅ | ❌ |
| `peer:revoke`（批量吊销、重置密钥） | ✅ | ✅ | ✅ | ❌ |
| `metrics:read`（实时监控面板） | ✅ | ✅ | ✅ | ✅ |
| `config:export`（导出 wg conf / 二维码） | ✅ | ✅ | ✅ | ❌ |
| `config:import` / `config:restore` | ✅ | ✅ | ❌ | ❌ |
| `audit:read` | ✅ | ✅ | ❌ | ❌ |
| `settings:write`（全局设置、备份策略、危险操作） | ✅ | ✅ | ❌ | ❌ |
| `system:shutdown`（重启接口、重置全部密钥） | ✅ | ✅ | ❌ | ❌ |

### 6.4 认证机制

```
登录 ──► POST /api/v1/auth/login {username, password}
          │
          ├─ argon2id 校验（常量时间比较）
          ├─ 签发 Access Token (HS256, 15min, claims: uid/usr/rol/ver)
          ├─ 签发 Refresh Token (opaque, 7d) → HttpOnly + SameSite Cookie
          └─ 写审计 audit:auth.login

ver 是签发时的 user.token_version。改密会提升它，此前签发的 access token
随即全部作废——只吊销 refresh token 挡不住在用会话。

后续请求 ──► Authorization: Bearer <access_token>（唯一凭据）

刷新   ──► POST /api/v1/auth/refresh  （轮换：单条 UPDATE...RETURNING 原子完成校验+撤销+签发）
登出   ──► POST /api/v1/auth/logout   （撤销 refresh）
```

Cookie 的 `Secure` 标志由 `WGP_COOKIE_SECURE` 控制：面板直接走 HTTPS 时置 `1`；置于 HTTP 反向代理后但代理未终止 TLS 时保持 `0`，否则浏览器根本不会回送 Cookie（表现为「登录成功但立刻被踢回登录页」）。

### 6.5 强制策略

| 策略 | 实现 |
|---|---|
| 首个账号 | 首次启动创建 `owner`（用户名来自 `WGP_ADMIN_USERNAME`，默认 `admin`；密码来自 `WGP_ADMIN_PASSWORD`，未设置则随机生成并打印一次）。**仅在库中一个账号都没有时执行**，已有的库不再读这两个变量 |
| 密码强度 | 最少 10 字符；拒绝与用户名相同 |
| 登录限流 | 按 IP + 用户名双维度滑动窗口，阈值 `WGP_LOGIN_MAX_ATTEMPTS`（默认 5）、锁定时长 `WGP_LOGIN_LOCK_WINDOW` |
| 会话管理 | refresh token 轮换制限制会话寿命；改密提升 `token_version`，在用的 access token 立即作废 |
| 修改自身角色 | 不适用：单账号部署，没有角色变更入口 |
| 反向代理信任 | `WGP_TRUSTED_PROXY` 决定是否采信 `X-Forwarded-For`。**不得设为 `0.0.0.0/0`**：那等于允许任何人伪造来源 IP，登录限流随之被绕过 |
| 前端路由守卫 | 前端 `RequirePermission` 组件做 UX 层拦截，但**真正鉴权在后端中间件**（前端仅体验优化，非安全边界） |
| API 白名单 | `/healthz`、`/readyz`、`/metrics`、`/api/v1/version`、`/api/v1/auth/login`、`/api/v1/auth/refresh`、`/api/v1/subscription/{token}`；其余 `/api/v1/**` 均经鉴权中间件 |

### 6.6 审计

所有写操作与敏感读操作（配置导出、密钥轮换、备份恢复）记录审计日志：`actor / action / resource / resource_id / detail / ip / ua / result`。拥有 `audit:read` 权限的账号可在审计页按操作类型、操作者、时间范围、资源检索，并导出 CSV（`GET /api/v1/audit/export`）；单账号部署下即 owner 本人。

---

## 7. 功能 Roadmap

### 7.1 里程碑总览

实际进度以 [`ROADMAP.md`](./ROADMAP.md) 为准，本节只定义各阶段主题与交付物。

| 阶段 | 主题 | 交付物 | 状态 |
|---|---|---|---|
| **M0** | 架构规划 + 工程骨架 | 本文档 + 可编译前后端骨架 + Dockerfile | 已完成 |
| **M1** | 核心链路打通 | Server/Peer CRUD、wg 热更新、容器网络下发、wg 配置导出、登录鉴权 | 已完成 |
| **M2** | Web UI 完整交付 | 仪表盘、Peer 管理（虚拟滚动/批量）、服务端全参数设置、实时监控、账号管理、审计、系统运维 | 已完成 |
| **M3** | 实时监控面板 | SSE 推送、服务器资源/隧道指标、每 Peer 实时与会话数据、四档历史曲线 | 已完成 |
| **M4** | 稳定性与安全加固 | RBAC 全路由覆盖、审计日志与导出、登录限流、加密备份/恢复、健康与就绪探针、Prometheus 端点 | 已完成 |
| **M5** | 增强能力 | 流量配额与周期结算、`tc` 限速、到期自动禁用、批量吊销/标签/启停、订阅链接与订阅拉取、CSV 导入导出 | 已完成 |
| **M6** | 生产发布 | 多架构镜像（amd64/arm64/armv7）、CI/CD、cosign 签名 + SBOM、Helm Chart、Kustomize、systemd unit、反代文档、社区文件 | 已完成 |
| **M7** | 真机验证与调优 | 真实节点冒烟、P99 基准、72h 稳定性、树莓派 arm64 实测、Lighthouse | 待验证 |

「已完成」指代码与自动化校验已落地；依赖真实网络与硬件的验证项集中在 M7，逐条列在 [`ROADMAP.md`](./ROADMAP.md)。

### 7.2 各阶段验收标准

**M0 / M1**
- [x] 架构文档：技术栈 / 模块拆分 / 部署架构 / 数据存储 / 权限体系
- [x] Go 工程可编译，模块划分落地；`gofmt` / `go vet` / `go test -race` 全绿
- [x] React 工程可构建，路由与布局骨架就绪，`tsc --noEmit` 无错
- [x] Dockerfile + docker-compose + install.sh 可用
- [x] 首次启动自动完成 DB 迁移、密钥生成、owner 初始化
- [x] Server 参数（路由表、fwmark、MTU、DNS、监听地址）可改并热生效，接口重启后保持
- [x] bridge 模式下容器内自动完成：链路创建、地址绑定、MTU、转发开关、路由、NAT、MSS 钳制
- [x] Peer 增删改热生效，支持 IP 自动分配/冲突检测/指定（数据库唯一索引兜底）
- [x] 导出 `.conf`，格式兼容官方 `wg-quick`
- [x] 登录 / 刷新 / 登出 / 限流 全链路可用；刷新令牌在并发下保持一次性

**M2 / M3**
- [x] 360px / 768px / 1440px / 2560px 四档断点无溢出、无重叠
- [x] 首屏 gzip 后 ≈124 KB（预算 400 KB），路由级懒加载分包
- [x] 全量参数均有对应控件（含枚举下拉、校验提示、危险项确认）
- [x] Peer 列表虚拟滚动；批量吊销/标签/启停
- [x] SSE 1 s 刷新，断线自动重连
- [x] 服务器：CPU/内存/磁盘/网络 IO/隧道总吞吐/在线 Peer 数
- [x] Peer：在线状态、IP、上下行速率与累计字节、最近握手、连接时长、标签
- [x] 历史曲线：1h / 24h / 7d / 30d 四档聚合切换
- [x] 单页渲染异常由 `ErrorBoundary` 兜住，不再整站白屏

**M4**
- [x] RBAC 中间件覆盖全部路由，越权返回 403
- [x] 审计日志页可检索与导出；所有写操作留痕
- [x] 备份可生成、加密落盘、可从备份恢复；损坏库可从备份恢复
- [x] `/healthz` 与 `/readyz` 反映真实依赖状态（驱动不可用时 `/readyz` 返回 503）
- [x] `/readyz` 纳入调谐结果：连续调谐失败超 3 周期返回 503，`status.reconcile` 暴露 `pending` / `ok` / `degraded`
- [x] 公开 `/metrics` 暴露 Prometheus 文本格式

**M5**
- [x] 流量配额（周期结算，落库判据幂等）、超限自动停用并在配额恢复后自动恢复
- [x] `tc` HTB 限速（上下行独立），Peer 参数变更后限速树重放
- [x] Peer 到期预警与自动禁用
- [x] 批量吊销与密钥重置、CSV 导入导出、订阅链接签发与拉取

**M6**
- [x] 多架构静态二进制与镜像，Release 附带 SBOM 与 cosign keyless 签名
- [x] CI：gofmt / vet / race test / 前端 lint+build / embed 完整性检查
- [x] Helm Chart（含 `values.schema.json`）、Kustomize base+overlay、systemd unit 均通过本地渲染校验
- [x] 反向代理与部署选型文档

**M7（进行中）**
- [x] 真实节点 `docker run` 冒烟：建 Peer → 握手 → 收发流量 → 配额结算 → 到期禁用
- [x] 240 Peer 规模 REST / SSE 基准（对照 §1.2 的 < 30 ms；P99 最高 13.61 ms、SSE P95 4.64 ms）
- [ ] 长稳：内存增长、SQLite 文件增长、goroutine 泄漏
      （进程内加速泄漏验证已通过；真机 72 小时采样按用户要求取消，实际 11 轮 / 45 分钟，判定留空）
- [ ] 树莓派 4 / armv7 上的镜像实测
- [ ] Lighthouse（性能 / 可访问性 / 最佳实践）

---

## 8. API 设计概览

RESTful，基础路径 `/api/v1`，统一响应包络。

```jsonc
// 成功
{ "code": 0, "message": "ok", "data": { } }
// 失败
{ "code": 40301, "message": "permission denied", "data": null }
```

| 方法 | 路径 | 说明 | 权限 |
|---|---|---|---|
| GET | `/healthz` | 存活探针（进程活着即 200） | 公开 |
| GET | `/readyz` | 就绪探针（数据库 + wg 驱动 + netlink/转发 + 调谐结果；后两者不满足返回 503） | 公开 |
| GET | `/metrics` | Prometheus 文本格式指标 | 公开 |
| GET | `/api/v1/version` | 版本、提交、构建时间（前端页脚展示） | 公开 |
| GET | `/api/v1/subscription/{token}` | 客户端按订阅令牌拉取配置 | 公开（令牌即凭证） |
| POST | `/api/v1/auth/login` | 登录 | 公开 |
| POST | `/api/v1/auth/refresh` | 刷新令牌（轮换） | 公开（Refresh Cookie） |
| POST | `/api/v1/auth/logout` | 登出 | 公开（Refresh Cookie） |
| GET | `/api/v1/auth/me` | 当前用户与权限集 | 登录 |
| POST | `/api/v1/auth/stream-ticket` | 换 SSE 一次性票据（60 s，绑定当前身份） | 登录 + `metrics:read` |
| GET | `/api/v1/server` | 服务端配置 | `server:read` |
| PUT | `/api/v1/server` | 更新服务端配置（热生效 + 记版本快照） | `server:write` |
| GET | `/api/v1/server/revisions` | 配置版本历史 | `server:read` |
| GET | `/api/v1/server/config` | wg0.conf 全文（`text/plain`，`?download=1` 触发下载） | `config:export` |
| POST | `/api/v1/server/reset-key` | 轮换服务端私钥 | `system:shutdown` |
| GET | `/api/v1/peers` | Peer 列表（含运行态） | `peer:read` |
| POST | `/api/v1/peers` | 创建 Peer | `peer:create` |
| PUT | `/api/v1/peers/{id}` | 更新 Peer | `peer:update` |
| DELETE | `/api/v1/peers/{id}` | 删除 Peer | `peer:delete` |
| POST | `/api/v1/peers/{id}/rotate-key` | 轮换客户端密钥 | `peer:revoke` |
| GET | `/api/v1/peers/{id}/config` | 导出 conf / 二维码内容 | `config:export` |
| POST | `/api/v1/peers/{id}/subscription` | 签发订阅令牌 | `config:export` |
| DELETE | `/api/v1/peers/{id}/subscription` | 吊销订阅令牌 | `config:export` |
| POST | `/api/v1/peers/bulk-revoke` | 批量吊销（重置密钥） | `peer:revoke` |
| POST | `/api/v1/peers/bulk-tag` | 批量改标签 | `peer:update` |
| POST | `/api/v1/peers/bulk-enable` | 批量启停 | `peer:update` |
| GET | `/api/v1/peers/export` | 导出 CSV（不含私钥） | `config:export` |
| POST | `/api/v1/peers/import` | 导入 CSV（`text/csv` 原文） | `config:import` |
| GET | `/api/v1/metrics/overview` | 服务器汇总快照 | `metrics:read` |
| GET | `/api/v1/metrics/peers` | 每 Peer 实时统计 | `metrics:read` |
| GET | `/api/v1/metrics/traffic?range=24h` | 历史流量（`1h`/`24h`/`7d`/`30d`） | `metrics:read` |
| GET | `/api/v1/metrics/stream` | SSE 实时流 | 一次性票据（`?ticket=`，豁免超时中间件） |
| GET | `/api/v1/account/profile` | 当前账号资料 | 登录 |
| PUT | `/api/v1/account/password` | 自助改密（改后全部会话失效） | 登录 |
| GET | `/api/v1/settings` | 全局设置 | `settings:write` |
| PUT | `/api/v1/settings` | 更新全局设置 | `settings:write` |
| GET | `/api/v1/backups` | 备份列表（读 `backup_record`） | `settings:write` |
| POST | `/api/v1/backups` | 立即生成加密备份 | `settings:write` |
| GET | `/api/v1/backups/{filename}` | 下载备份 | `settings:write` |
| POST | `/api/v1/backups/{filename}/restore` | 从备份恢复 | `settings:write` |
| DELETE | `/api/v1/backups/{filename}` | 删除备份 | `settings:write` |
| GET | `/api/v1/audit` | 审计检索 | `audit:read` |
| GET | `/api/v1/audit/export` | 审计导出 CSV | `audit:read` |

CSV 导入导出的列顺序在导入与导出两侧共用同一常量：
`name, address, allowedIps, endpoint, persistentKeepalive, note, tags, trafficQuotaBytes, quotaPeriod, rateLimitMbps, maxConnections, enabled, expiresAt`。多标签以 `|` 分隔。**任何一列都不是私钥。**

> `maxConnections` 这一列保留在 CSV 里只是为了**兼容既有文件**——它已经不再有任何语义。
> WireGuard 内核对每个 Peer 只保留一个 endpoint 槽位，「最大同时连接数」在协议层
> 没有对应物；导入时该列的值被丢弃，创建/更新接口也不再接受这个字段
> （传入返回 400，因为 `decodeJSON` 使用 `DisallowUnknownFields`，静默忽略更危险）。
> 保留一个能填、不能生效的字段比删除它更糟，所以选择从接口与界面中移除，
> 而不是继续留着一个会误导用户的输入框。

### 8.1 SSE 事件

`GET /api/v1/metrics/stream` 的每一帧为 `event: <type>` 加一行 JSON：

```jsonc
{ "type": "peer.online", "payload": { }, "sentAt": 1730000000 }
```

指标快照是自包含的全量对象而非增量补丁，客户端直接替换即可，断线重连后无需补齐差量。在快照之外，`eventbus` 还投递十三类领域事件，全部由 SSE 推给界面：

| 事件 | 触发时机 |
|---|---|
| `peer.created` / `peer.updated` / `peer.deleted` | Peer 增删改**已落库并完成调谐**（批量操作折叠为一条事件，不按 Peer 逐条轰炸） |
| `peer.key_rotated` | 客户端密钥轮换（含批量轮换） |
| `peer.online` / `peer.offline` | 采集器检测到握手状态翻转 |
| `peer.quota_exceeded` | 本周期用量达到配额，Peer 被自动停用 |
| `peer.expiring` / `peer.expired` | 临近到期预警 / 已到期自动停用 |
| `peer.restored` | 配额周期结算或人工启用后恢复 |
| `server.updated` | 服务端参数变更并已下发 |
| `backup.created` | 备份生成成功 |
| `restore.applied` | 备份恢复完成 |

事件清单必须**前后端同步维护**。这不是靠自觉能守住的：真机上就出现过后端扩到 17 类、前端两处（SSE 监听器、TS 类型）各写一份 9 项旧清单的情况，而浏览器 `EventSource` 对未注册具名监听器的事件既不报错也不重连——`peer.created` 等事件后端一直在推，界面上毫无痕迹，通知只是静默消失。因此事件全集收敛为前端 `web/src/types/api.ts` 的单一 `DOMAIN_EVENT_TYPES`，并由 `TestFrontendEventListMatchesBackend` 与后端 `AllEventTypes()` 双向比对：跨语言的两份清单没有编译器兜底，只能靠测试钉住。

`eventbus` 为进程内非阻塞投递：订阅者（每个 SSE 连接）消费过慢只会丢自己的消息，不会反压事件生产者。

---

## 9. 前端信息架构

```
/login                     登录页（移动端优先）
/                          仪表盘 Dashboard          metrics:read
  ├─ 服务器状态卡（监听端口/公网端点/隧道地址/运行时长）
  ├─ 资源卡（CPU / 内存 / 磁盘）
  ├─ 隧道流量卡（实时速率 + 24h 面积图）
  └─ Peer 概览（总数/在线/超限/流量 TOP N）
/peers                     客户端管理                peer:read
  ├─ 列表（虚拟滚动；移动端卡片视图；多选进入批量条）
  ├─ 创建/编辑抽屉（全参数表单：地址、AllowedIPs、标签、配额、周期、限速、到期）
  └─ 详情弹窗（conf、二维码、订阅链接、实时统计、流量曲线）
/monitor                   实时监控                  metrics:read
  ├─ 隧道总吞吐 + 每 Peer 上下行实时速率
  ├─ 在线状态、最近握手、连接时长
  └─ 历史曲线（1h / 24h / 7d / 30d）
/settings                  服务端设置                server:read
  ├─ 服务端配置（端口/地址/MTU/DNS/路由表/fwmark/AllowedIPs/Keepalive/行为开关）
  ├─ 配置版本历史与回滚
  └─ wg0.conf 预览与下载
/account                   账号                      登录
  └─ 当前账号资料、自助改密
/audit                     审计日志                  audit:read
  ├─ 按操作类型/操作者/时间/资源检索
  └─ 导出 CSV
/system                    系统运维                  settings:write
  ├─ 全局设置（保留天数、自动备份开关与间隔）
  ├─ 备份：立即备份、列表、下载、恢复、删除
  └─ 版本与构建信息
*                          404（渲染在已登录外壳内）
```

侧边栏按权限过滤：没有 `audit:read` 的人看不到审计入口，没有 `settings:write` 的人看不到系统运维入口。但这只是体验优化——真正的拦截在后端中间件。

UI 规范：
- 主题：深色为主（VPN 场景常驻后台），玻璃拟态卡片 + 微渐变 + 细边框高光；提供浅色主题。
- 布局：`grid` + `flex`；侧边栏固定 240px，`< 1024px` 折叠为抽屉（`Esc` 关闭、路由变化自动收起）。
- 图表：手写 SVG 折线/面积图，颜色绑定 CSS 变量以自动跟随主题，支持时间范围切换。
- 反馈：所有变更走 Toast；危险操作用 AlertDialog；加载用 Skeleton 而非全屏 Spin。
- 容错：`ErrorBoundary` 兜住单页渲染异常，提供重试/重载，不会整站白屏。
- 无障碍：键盘可达、焦点环、ARIA 标签、色彩对比 ≥ 4.5:1。

### 9.1 站点图标

图标在 `web/public/` 下，Vite 构建时原样拷进 `internal/web/dist`，随二进制一起 embed，镜像里不额外增加静态目录。

- 主源是 `favicon.svg`：矢量、体积小（1.1 KB），现代浏览器直接采用。图形与界面左上角的 Logo 同源——切角底板加霓虹黄闪电，颜色取自主题的 `primary` 与 `background`。
- 兜底是 `favicon.ico`（内嵌 16/32/48）与 `favicon/favicon-*.png`（16~512），覆盖不支持 SVG favicon 的老浏览器与移动端书签。
- `apple-touch-icon.png`（180×180）给 iOS 主屏，`site.webmanifest` 供 PWA 安装。
- PNG/ICO 由 `e2e/ui/render-favicon.mjs` 从 SVG 光栅化生成（Playwright 渲染），改 SVG 后重跑该脚本即可同步各尺寸。

有一处必须显式处理：`.webmanifest` 不在 Go 内置 MIME 表里，`http.FileServer` 会回退成 `text/plain`，浏览器因 `Content-Type` 不符而**静默拒绝加载** manifest——页面本身无报错，只是 PWA 安装入口消失。因此 `mountWebUI` 对该扩展名显式指定 `application/manifest+json`，不依赖系统 `mime.types`（容器里未必存在）。

---

## 10. 目录结构（仓库）

```
wgpanel/
├── ARCHITECTURE.md / ROADMAP.md / README.md / CHANGELOG.md
├── LICENSE                      # MIT
├── go.mod / go.sum
├── Dockerfile
├── docker-compose.host.yml / .ipvlan.yml / .macvlan.yml
├── install.sh                   # 一键安装脚本
├── .env.example
├── .dockerignore / .gitignore
├── cmd/wgpanel/main.go
├── internal/                    # 见 §3.2
│   └── web/dist/                # 前端构建产物（go:embed）
├── deploy/                      # 交付形态
│   ├── README.md                #   各种部署形态的选型与共通注意事项
│   ├── helm/wgpanel/            #   Helm Chart（含 values.schema.json）
│   ├── kustomize/               #   Kustomize base + production overlay
│   ├── systemd/                 #   非容器部署的 unit 文件
│   └── reverse-proxy/           #   Nginx / Caddy / Traefik 与 SSE 相关配置
├── .github/
│   ├── workflows/ci.yml             # gofmt/vet/race test + 前端类型检查 + 镜像构建
│   ├── workflows/release.yml        # 多架构镜像 + SBOM + cosign keyless 签名
│   ├── workflows/validate-delivery.yml # Helm/Kustomize 交付物校验
│   ├── ISSUE_TEMPLATE/ / PULL_REQUEST_TEMPLATE.md
│   └── dependabot.yml
├── CONTRIBUTING.md / CODE_OF_CONDUCT.md
└── web/                         # 前端源码
    ├── package.json
    ├── vite.config.ts
    ├── tailwind.config.ts
    ├── tsconfig.json
    ├── public/                  #   静态资源原样拷贝到 dist
    │   ├── favicon.svg          #     站点图标主源（矢量）
    │   ├── favicon.ico          #     老浏览器兜底（内嵌 16/32/48）
    │   ├── favicon/             #     PNG 多尺寸（16~512）
    │   ├── apple-touch-icon.png #     iOS 主屏图标
    │   └── site.webmanifest     #     PWA manifest
    └── src/
        ├── main.tsx / App.tsx   #   入口与路由表（含懒加载与错误边界）
        ├── lib/                 #   api client / useMetricsStream / useVirtualList / permissions / utils
        ├── components/
        │   ├── ui/              #   Button / Card / Input / Dialog / Badge / Skeleton / StatTile
        │   ├── layout/          #   AppLayout（侧边栏 + 顶栏 + 移动端抽屉）
        │   ├── charts/          #   TrafficChart（手写 SVG）
        │   └── providers/       #   AuthProvider / ToastProvider
        ├── pages/               #   login / dashboard / peers / monitor / settings / account / audit / system
        └── types/               #   与后端 DTO 对齐的类型定义
```

---

## 11. 风险与应对

| 风险 | 影响 | 应对 |
|---|---|---|
| 宿主未加载 `wireguard` 内核模块 | 无法创建接口 | 启动能力探测 → `/readyz` 返回 503，日志给出 `modprobe wireguard` 提示。**不提供 `wireguard-go` 用户态回落**：它会带来一个纯 Go 的额外依赖与完全不同的数据面行为，与「内核面 + netlink 控制面」的架构前提冲突 |
| 容器双重 NAT 导致 MTU 黑洞 | 丢包/断流 | 自动 MTU 计算 + MSS clamping + UI 手动覆盖 |
| `iptables` 需要可写的 `/run/xtables.lock` | 非 root 下规则下发失败 | 镜像内 `/run` 可写；Helm/Kustomize 挂 `emptyDir` 到 `/run`；systemd 用 `ExecStartPre` 预创建锁文件并放开 `ReadWritePaths` |
| 非 root + `CAP_NET_ADMIN` 在老 Docker 上失效 | 无法下发网络配置 | 依赖 Docker ≥ 20.10 把 `--cap-add` 放进 ambient 集合；老版本用 `docker run --user 0:0` 临时兜底，已写入镜像注释与部署文档 |
| `WGP_TRUSTED_PROXY` 配成 `0.0.0.0/0` | 任意请求可伪造 `X-Forwarded-For`，登录限流被绕过 | 文档与部署模板中明确禁止；反代文档单列一条 |
| 高并发 Peer（>500）时 SQLite 写锁竞争 | 写入阻塞 | 写连接池固定 1 条 + 读池独立；采集器批量写、按采集周期合并冷缓存请求（避免读接口放大成写）；必要时切 PostgreSQL（repository 抽象已隔离） |
| Peer 数量逼近 IPAM 上限（253）时 `address pool exhausted` | 新建 Peer 失败并返回 500 | 这是设计上限而非缺陷，UI 与报错文案需明示；单面板需要更多 Peer 只能部署多实例（当前不支持更大网段） |
| 前端依赖 CDN 不可达 | 面板白屏 | 所有资源本地打包，无外部 CDN 依赖 |
| 时钟漂移导致握手时间异常 | 监控数据失真 | 镜像内 tzdata + `TZ` 环境变量；面板按本地时钟渲染时间轴 |
| 主密钥丢失 | 私钥不可解密 | 启动时检测 `/data/master.key`，缺失即拒绝启动并明确提示（不做静默重建）；备份文档强调主密钥必须与库同备 |
| 调谐持续失败但服务「看起来健康」 | 编排系统把不通流量的实例继续留在负载后面 | `/readyz` 暴露 `status.reconcile`，连续失败超 3 个调谐周期返回 503；限速类失败只标 `degraded` 不摘流量 |

---

## 12. 与本项目同类方案的差异化总结

1. **全参数可视化**：不是「端口 + 密钥」两个输入框，而是把 wgctrl 的每一个字段都映射为带校验与说明的 UI 控件。
2. **开箱即用的实时监控**：默认开启，无需安装 exporter 或额外容器。
3. **零依赖一键部署**：单镜像、单卷、单命令，不需要 Compose 编排数据库/监控栈，不需要前置安装 `wireguard-tools`。
4. **三种网络模式**：host / ipvlan / macvlan 各一份独立 compose；L2 直挂的 IPv6 由路由器 RA/SLAAC 下发，外部设备可直连（原 bridge 模式因拿不到 RA 已移除，见 ADR-8）。
5. **声明式调谐**：内核状态可自愈，接口崩溃/容器重启后自动恢复。
6. **企业级权限与审计**：从第一天就内置 RBAC 与审计，而不是后补。

---

*文档版本 v1.1 · 对应工程状态：M0–M6 已落地，M7 真机验证待执行*