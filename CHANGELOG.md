# 变更日志

本文件记录 WGPanel 的所有重要变更。
格式参考 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/)，
版本号遵循 [语义化版本](https://semver.org/lang/zh-CN/)。

## [未发布]

### 变更

- **Go 工具链升到 1.26**。`golang.org/x/crypto` 与 `modernc.org/sqlite` 的新版本
  都把 `go` 指令提到 1.26.0，`go.mod` 随之跟进。Dockerfile 的构建阶段基础镜像
  从 `golang:1.24-alpine` 同步到 `golang:1.26-alpine`——旧镜像在
  `GOTOOLCHAIN=local` 下会直接报 `go.mod requires go >= 1.26.0`，
  连 `go mod download` 都过不去，镜像构建整段失败。CI 的 `go-version-file: go.mod`
  本来就跟着 `go.mod` 走，无需改动。
- **前端构建链升到 Vite 8**。Vite 8 底层换成 Rolldown，Rollup 的 `manualChunks`
  已废弃，改用 `advancedChunks.groups`（语义更接近 webpack 的 `splitChunks`）；
  `@vitejs/plugin-react` 同步升到 6.x（peer 依赖要求 Vite 8，两者必须同升）。
  分包结果不变：`react` 与 `query` 两个 chunk 照旧拆出。

### 修复

- **依赖升级 PR 集体失败**。三个 dependabot PR 并非代码问题，而是版本互锁：
  plugin-react 6.x 的 peer 要求 `vite ^8`，而仓库还钉在 `vite ^6`；
  反过来 `vite 8` 又要求 plugin-react 的 peer 覆盖到 8。
  单独升任一方都会在 `npm ci` 阶段 ERESOLVE，必须成对升级。
- **TypeScript 升到 7**。TS 7 移除了 `baseUrl` 编译选项，`tsconfig.json` 里的
  `baseUrl: "."` 直接报 `TS5102`。该选项本就多余——`paths` 自 TS 4.1 起
  就能相对 `tsconfig.json` 所在目录解析，去掉后 `@/*` 别名照常工作。

## [0.10.0] - 2026-10-28

### 变更

- **网络部署模式重构**：Docker 部署支持 `host` / `ipvlan` / `macvlan` 三种模式，
  每模式一份完整独立的 compose 文件（模式硬编码 `WGP_NATIVE_MODE`，不接受 .env 覆盖），
  `scripts/up.sh` 统一入口并做 L2 前置校验（必填参数、宿主地址冲突、ipvlan parent 独占）。
  L2 模式 IPv6 采用 Docker 官方「router-assigned IPv6 addresses」配方——网络不带 v6 子网，
  端点 sysctls 打开 `disable_ipv6=0` / `accept_ra=2`，容器内再注入
  `net.ipv6.conf.all.forwarding=1`，eth0 直接从上游路由器 RA/SLAAC 拿全局地址（GUA），
  外部设备可直连 WireGuard，家宽前缀轮换自动跟随。面板新增「系统运维 → 网络」诊断卡
  （模式、各接口地址、RA 三开关状态与自愈、实时 Endpoint），侧栏显示当前网络模式。
  K8s 与 systemd 形态改用 `managed`（自管 netns 转发/NAT）表述，行为不变。
  compose 数据卷补显式 `name: wgpanel-data`，目录改名不再静默新建空卷「丢」数据。
- **RA 诊断口径随模式收敛**。`netdriver.InspectRA` / `EnsureRA` 改为接收部署模式参数，
  `RAStatus` 新增 `applicable` 字段：只有 L2 模式（ipvlan/macvlan）的 RA 链路归面板管，
  `host` 与 `managed` 的 RA 策略归宿主网络栈或 CNI，面板不越界读写，
  界面按「不适用」展示而不是把宿主策略误报成可修复故障。

### 移除

- **bridge 部署模式**。docker bridge 是独立链路，上游路由器的 RA（链路本地组播）
  到不了容器，SLAAC 拿不到全局地址；发布宿主 IPv6 端口只能经 DNAT 解决入站握手，
  隧道内客户端访问 IPv6 互联网（出站数据面）无解。`WGP_NATIVE_MODE=bridge` 现在
  启动即报错并提示迁移方向；`docker-compose.yml`（bridge 版）已删除；
  `install.sh` 与文档中的 `docker run` 示例改为 `--network host`。
  原「容器内自管转发/NAT」能力以 `managed` 形态保留给 K8s / systemd，见 ARCHITECTURE ADR-8。

### 新增

- **站点图标**。之前面板没有 favicon，浏览器标签页只显示一个默认的地球图标，
  和 2077 风格的面板摆在一起很突兀。现在补齐整套图标资产：
  `web/public/favicon.svg` 作为主源（矢量，1.1 KB），图形与界面左上角的 Logo 同源——
  切角底板加霓虹黄闪电，颜色取自主题的 `primary` 与 `background`；
  另生成 `favicon.ico`（内嵌 16/32/48）、`favicon/favicon-*.png`（16~512）、
  `apple-touch-icon.png`（180×180）与 `site.webmanifest`，覆盖老浏览器、移动端书签与 PWA 安装。
  PNG/ICO 由 `e2e/ui/render-favicon.mjs` 从 SVG 光栅化生成，改 SVG 后重跑即可同步各尺寸。

### 修复

- **PWA manifest 被浏览器静默拒绝加载**。`.webmanifest` 不在 Go 内置 MIME 表里，
  `http.FileServer` 会把它回退成 `text/plain`，而浏览器要求 manifest 的
  `Content-Type` 是 `application/manifest+json`，不匹配就**静默丢弃**——
  页面不报错、控制台无警告，只是 PWA 安装入口消失。
  `mountWebUI` 现在对该扩展名显式指定类型，且不依赖系统 `mime.types`（容器里未必存在）。

## [0.9.0] - 2026-10-23

首个对外发布版本。对应 ROADMAP 的 `v0.9.0`（M4 + M5，生产候选 RC）：
M0-M6 已交付，M7 真机验证在本轮收尾，199 项回归全绿。

### 新增

- **流量配额**：`daily` / `weekly` / `monthly` / `never` 四种周期，配额用尽自动停用，
  周期结束后自动恢复。周期起点持久化在 `peer.quota_period_start`，重启或调度延迟都不会漏结算。
- **带宽限速**：基于 `tc` HTB 树的 per-Peer 上下行限速，调整时不断开已有连接。
- **生命周期**：到期自动禁用、到期前预警、批量吊销与批量标签。
- **订阅链接**：为单个 Peer 签发订阅 URL，客户端经 `GET /api/v1/subscription/{token}` 拉取，可一键吊销。
- **批量操作**：CSV 导入导出、批量启停、批量打标签。导入失败行给出精确行号与原因。
- **端到端测试套件**：`e2e/` 目录收录 API 契约回归、浏览器 UI 全页与交互、
  真机 WireGuard 隧道、外部第三方客户端四组脚本，
  目标地址与凭据全部走环境变量，脚本本身不含任何硬编码 IP 或口令。
- **外部第三方客户端回归**：`ext_client_test.py` 与 `ui/ui_ext_client.mjs`
  在一台干净的第三方机器上跑完整链路——握手、在线状态、流量统计、
  限速落到 `tc`、停用断链、启用恢复、密钥轮换、订阅拉取，
  以及 UI 对该客户端的呈现。与 `tunnel_test.py` 的区别是它不依赖被测机器自身。
- **服务端配置导出界面**：`/settings` 页面新增配置全文预览（按需拉取，不预取私钥）、
  `wgpanel.conf` 下载与修订历史摘要查看。此前三个后端端点已就绪但无界面入口。
- **备份恢复**：AES-256-GCM 加密的逻辑备份（`.wgbak`）、`backup_record` 索引、定时备份、一键恢复。
- **运行时设置**：面板内可改的运行参数（采样间隔、调谐周期、配额与到期强制开关、
  到期预警天数、备份开关/间隔/保留份数/目录）。
- **审计导出**：按当前筛选条件导出审计日志 CSV。
- **Prometheus 指标**：`/metrics` 暴露面板自身的运行指标。
- **事件推送**：SSE 推送领域事件，前端据此弹 Toast 并局部刷新。
- **部署交付物**：Helm Chart（含 `values.schema.json`）、Kustomize base + production overlay、
  systemd unit、反向代理与部署选型文档、GitHub Actions（CI / Release / 交付物校验）。
- **多架构镜像**：`linux/amd64`、`linux/arm64`、`linux/arm/v7` 交叉编译，
  Release 附带 SBOM 与 cosign keyless 签名。
- **前端健壮性**：`ErrorBoundary` 兜住单页渲染异常、404 页面、登录后回跳原目标页、
  修复移动端抽屉「打开即关闭」的自锁。

### 变更

- **多用户管理整体下线**。面板是单人部署，没有建号、删号与角色分配的实际使用场景，
  但整套 RBAC 管理面（用户列表、角色调整、启停、改密）在界面上摆着，
  让「谁在用这个面板」这件事看起来还需要人来管。现在前后端与存储一并清干净：
  `/api/v1/users` 路由组、`UserService` 的 CRUD、`user:read` / `user:write` 两个权限点、
  `user.created` / `user.updated` / `user.deleted` 事件名，以及「用户」页面。
  **保留并重写了自助改密**：新页面「账号」提供当前账号资料与改密表单，
  身份一律取自登录态，请求体里没有也不能有 `userId`（传入直接 400）。
  改密仍走 `token_version` 提升这一条机制，让已签发的 access token 全部立即作废。
  权限矩阵与角色枚举没有删——`owner` 之外的三档留作将来开多账号时的底子，
  当前运行时实际只有一个 owner。
- **通知出口（Webhook）整体下线**。实测这台机器没有外网出口，
  Webhook 投递必然超时失败，但它在界面上仍被当作可用功能展示，
  而且投递时既不设 `Authorization` 也不做 HMAC 签名，凭据只藏在 URL 里——
  这不是一个可以放心交给客户的出口。现在整条链路清干净：
  `internal/notify` 包、`webhook` 表（迁移 `v10` 删除）、`/api/v1/webhooks` 路由组、
  `eventbus` 的 `OnPublish` 同步钩子、系统运维页的通知卡片与全局设置里的通知总开关。
  **SSE 实时推送不受影响**：它走 `eventbus` 的订阅通道，与 Webhook 的钩子通道
  是两套独立机制，真机回归确认上下线、配额超限、到期等事件照常推送到界面。
- **API 令牌功能整体下线**。单机部署没有机器对机器接入的调用方，这套凭据
  只会在界面上让人以为还能签发长期凭据，实际部署里从签发到使用一次都没发生。
  现在前后端与存储一并清干净：`X-API-Token` 请求头、`/api/v1/tokens` 路由组、
  `api_token` 表（迁移 `v9` 删除）、`token.issued` / `token.revoked` 事件名，
  以及「用户与令牌」页的签发卡片与令牌表格。鉴权只剩 `Authorization: Bearer`，
  订阅链接是唯一匿名接口。刷新令牌的哈希逻辑保留——它与 API 令牌无关。
- **服务端 `[Peer]` 的 AllowedIPs 改用 Peer 自身地址下发**。此前误把客户端侧的
  `AllowedIPs`（默认 `0.0.0.0/0`）原样写进服务端 peer 条目。WireGuard 内核要求服务端
  AllowedIPs 在所有 peer 之间唯一，重复网段只会被内核裁决保留一个，其余静默变成
  `(none)`：握手能成功，但所有流量被丢弃，表现为「能连上却 ping 不通」。
  该缺陷同时让调谐器每 2 秒判定一次漂移并整表重放，持续清空握手时间与流量计数。
  现在客户端 `AllowedIPs` 只影响客户端配置，服务端使用 Peer 地址，
  与 `RenderServerConfig` 导出的配置保持一致。
- 环境变量前缀统一为 `WGP_`（早期草稿中的 `WGPANEL_` 未落地）。
- 服务端配置移除「加密套件」与「PostUp/PostDown」两组字段：前者内核无可配置项，
  后者需要执行任意 shell，与最小权限模型冲突。保留一个可写却不执行的字段比删除更危险。
- 不再提供 `wireguard-go` 用户态回落：宿主缺少 `wireguard` 模块时 `/readyz` 返回 503
  并在日志提示 `modprobe wireguard`。
- `peer.quota_period_start` 落库取代时间窗口近似判据，周期结算变为幂等。
- Peer 列表接口不再返回 `privateKey` / `presharedKey` 字段（含加密后的密文）。

### 修复

- **审计日志的「来源 IP」与「User-Agent」几乎全空**。两个原因叠加：
  其一是架构性的——`AuditEvent` 在 service 层构造，而 `*http.Request` 止步于 handler 层，
  service 方法签名里根本没有 IP/UA 参数，17 个写入点有 16 个填不了；
  其二是一行实打实的 bug——登录成功时审计传的是空的 `LoginRequest{}` 而非 `req`
  （同函数的失败分支全传对了），于是呈现出「登录失败有来源、登录成功反而没有」的怪象。
  实测库里 287 条 `auth.login` 的 ip/ua 全为空，42 条 `auth.login_failed` 全有。
  现在新增 `internal/reqmeta`：HTTP 中间件在整条链最前面把来源写进 context，
  `AuditRepo.Write` 落库前统一补齐。新增审计点不必记得填，请求来源也只有这一个入口。
  定时任务触发的策略执行没有 HTTP 来源，字段留空——这符合语义，不再伪装成有来源。
- **登出没有审计记录**。此前只有登录侧写审计。排查「谁在什么时候登进了面板」时，
  只有开门没有关门，退出侧是空白。现在补上：登出路由不挂鉴权中间件
  （会话可能已过期，硬要求 Bearer 会让人在令牌失效时永远登不出去），
  因此身份从被撤销的刷新令牌反查。
- **`GET /api/v1/peers` 返回的运行态缺身份字段**。驱动只按公钥索引，
  返回的 `stats.peerId` 恒为 0、`publicKey` 为空串，而前端把列表接口的
  stats 与 SSE 帧按 peerId 对齐，导致两路数据对不上号。现在由
  `PeerService.List` 补齐 `PeerID` 与 `PublicKey`。
- **CSP 拦截内联字体，展示体静默失效**。策略缺 `font-src`，回落到
  `default-src 'self'` 后把 `@fontsource` 打包出的 data: URI 全部拦掉。
  页面照常渲染，但 Chakra Petch 实际没生效，控制台满是 CSP 违规。
  现在显式放开 `font-src 'self' data:`。
- **HTTP 部署下剪贴板完全失效**。`navigator.clipboard` 只在安全上下文
  （HTTPS 或 localhost）可用，而面板默认就部署在普通 HTTP 上，
  「复制配置」「复制订阅链接」两个按钮全都不动。
  现在改为优先走现代 API、失败再退回 `execCommand` 的临时 textarea 方案。
- **启动时必留一条红色 console 错误**。应用启动会用 HttpOnly 刷新 Cookie
  换访问令牌，未登录时该请求必然 401，浏览器控制台因此留一条报错，
  看起来像故障。现在用本地会话标记在无会话时直接跳过该请求。
- **仪表盘 CPU 显示成网卡速率**。`DashboardPage` 的 CPU 行原本写的是
  `detail={formatRate(netRxBps + netTxBps)}`——把网卡总流量塞进了 CPU 的读数位，
  于是界面显示「CPU 7.9 KB/s」。百分比只喂给了进度条、没有任何数字，
  读者唯一能看到的那个数还是错的。现在显示 `cpuPercent.toFixed(1)%`。
  同一批修掉的还有三处同类错位：内存/磁盘行有百分比进度条却只显示字节数
  （补上百分比）、「下行速率」卡的副进度条传的是 `memPercent`（删掉）、
  「公网出口」的副信息里混装了运行时长（移走）。
- **Sparkline 只喂两个点**。`points={[cpuPercent, memPercent]}` 画出来是
  一条从 CPU 斜拉到内存的直线，既不是任何一方的趋势，还暗示「内存是 CPU 的
  下一阶段」。改为按 `collectedAt` 去重后累积最近 30 次 CPU 采样，
  并标注实际采样次数。
- **历史曲线「合计流量」把累计计数器逐点求和**。`rx_bytes` / `tx_bytes`
  是内核累计计数器（后端聚合本来就用 `MAX` 而非 `SUM`），逐点相加会把
  同一份字节数重复计入每个采样点：一条 5 GB 的曲线配 18 个采样点会显示成
  90 GB。改为逐段累加正向增量——不能只取首尾差，因为接口重建/容器重启
  会让计数器归零，末值反而小于首值，首尾差会被夹成 0 并把归零前的流量抹掉。
- **`computeRates` 没有计数器回退保护**。上一条修复的 `rateTracker`
  已经处理过归零场景，但历史曲线查询这条路径的差分没有同样保护：
  接口重建后累计值从 0 重来，差分为负，会画出向下的尖峰并算出负的合计流量。
  这类区间真实值无从得知（重启期间的字节没被记下来），按 0 处理，
  补 `TestComputeRatesClampsCounterResetToZero` 钉住。
- **采集器 `collect()` 存在并发写，可导致进程无日志终止**。该函数改写
  `lastPeers` 这张 map 与 `cpu`/`net`/`tunnelRx` 三个差分 tracker，
  但调用方不止采集循环自己：`SnapshotNow` 由 `/metrics`（Prometheus 默认
  每 15 秒抓一次）与 Peer 指标接口并发调用。`go test -race` 实测报出
  **11 处 data race**，其中包含对同一张 map 的并发读写——
  在 Go 里那是 fatal error，容器直接退出且不留任何日志。
  现在 `collect()` 全程持 `collectMu`，并新增并发用例钉住该行为。
  这个缺陷靠读代码看不出来：单线程跑永远正确，
  而 CI 不带 `-race` 时测试同样全绿。
- **计数器回退时会算出负速率**。`rateTracker` 直接用 `rx - prevRx` 做差分，
  而接口被删除重建、容器重启之后 wg 的累计计数从 0 重新开始，差值必然为负：
  界面显示负的实时速率，历史曲线上出现向下的尖峰，看起来像「流量倒流」。
  per-peer 路径此前有 `st.RxBytes >= prev.RxBytes` 的保护，
  隧道汇总与主机网卡两条路径没有。现回退当拍一律返回 0 并以当前值重建基线，
  下一拍即恢复正常速率——这也才是 ROADMAP M3 一直声称的行为。
- **恢复备份后的提示文案与实际行为相反**。后端返回「需要重新登录」，
  前端确认框照抄，但 access token 每次鉴权都会重读 `user` 表、
  JWT 签名密钥又不受恢复影响，发起恢复的会话其实仍然有效；
  照着提示去重新登录，反而可能因为密码已被回滚而失败。
  真正的风险是反方向的：恢复会把该备份之后创建的客户端、重置的密码
  一并回退，而管理员必须知道这件事。现如实说明回退范围。
- **浏览器里的实时监控从未连上过（SSE 鉴权链路不可用）**。`/api/v1/metrics/stream`
  挂在通用鉴权组内，只接受 `Authorization: Bearer` 头，而浏览器 `EventSource`
  构造器不接受自定义请求头——前端 `new EventSource('/api/v1/metrics/stream')`
  什么凭据都不带，于是长期处于 401 静默重连：界面只显示「未连接」，
  不报任何错；而 curl 带 `Authorization` 头的验证却一切正常，
  缺陷因此躲过了全部既有测试。文档里当时写着「依赖 Cookie 传递身份」，
  代码中却没有任何读 Cookie 的路径，文档与实现已经脱节。
  现引入**一次性短期票据**：`POST /api/v1/auth/stream-ticket` 用常规鉴权
  换取绑定身份、60 秒有效、消费即作废的票据，SSE 改用 `?ticket=` 连接，
  断线后自动重新换票并指数退避。票据绑定 `Principal` 而非只记「已通过」，
  SSE 路由还原身份后照常走 `metrics:read` 权限判定。
  没选「JWT 拼 URL」是因为那会让访问令牌落进反向代理访问日志和浏览器历史；
  没选「加 Cookie」是因为它要额外处理 CSRF 与 SameSite，
  并把会话凭据的攻击面扩大到每一条请求。
- **前端漏收 8 类领域事件，通知静默消失**：SSE 监听器与 TS 事件类型
  各自维护了一份只含 9 项的旧清单，而后端已扩到 17 项。
  浏览器 `EventSource` 对未注册具名监听器的事件既不报错也不重连，
  因此 `peer.created`、`peer.deleted`、`peer.key_rotated`
  等事件虽被后端推送，界面上毫无痕迹。
  现把事件全集收敛为 `web/src/types/api.ts` 的单一 `DOMAIN_EVENT_TYPES`，
  两处共用，并新增 `TestFrontendEventListMatchesBackend` 把该数组
  与 `domain.AllEventTypes()` 双向钉住——跨语言的两份清单没有编译器兜底，
  只能靠测试拦截。
- **导出的客户端配置把 `[Peer]` 的键写进了 `[Interface]`，导致 `wg-quick up` 直接失败**。
  `wg-quick` 的 `strip()` 只剥离 `Address`/`DNS`/`MTU`/`Table`/`Pre*`/`SaveConfig`
  这类自有字段，其余键原样透传给 `wg setconf`；而 `wg(8)` 在 `[Interface]` 段只接受
  `PrivateKey`/`ListenPort`/`FwMark`，遇到 `AllowedIPs`、`Endpoint`、
  `PersistentKeepalive`、`PresharedKey` 会报 `Line unrecognized` 并拒绝建立接口。
  典型现象是 `wg-quick up` 退出码 127、接口随即被删除，而日志里看不出配置哪里不对。
  现已将这些键全部移入 `[Peer]` 段。
- **服务端导出存在同类段位错误**：`PublicKey`、`PresharedKey`、`AllowedIPs`、
  `PersistentKeepalive` 写在 `[Interface]`，同样会被 `wg setconf` 拒绝。
  其中接口级 `AllowedIPs` 与 `PersistentKeepalive` 在 WireGuard 中本就没有对应语义
  （PSK 与 AllowedIPs 都是 per-peer 的），已从 `[Interface]` 移除。
- **客户端配置重复下发 PSK 且与服务端内核不一致**：同时输出 `server.PresharedKey`
  与 `peer.PresharedKey`，而 `server.PresharedKey` 是接口级字段、从未通过
  `ConfigureDevice` 下发到内核。重复键会让 `wg setconf` 报
  `Too many preshared keys`，即使不报错也会因两端 PSK 不匹配而永远握手失败。
  现在只下发 `peer.PresharedKey`，与 `toPeerConfig` 写入内核的值严格一致。
- **导出的客户端配置把监听端口重复追加到 `Endpoint` 尾部**。
  渲染逻辑对任何非空 endpoint 一律按 `%s:%d` 拼上 `server.ListenPort`，
  不区分它是否已经带端口，于是管理员填 `172.17.0.2:51820` 会得到
  `Endpoint = 172.17.0.2:51820:51820`；裸 IPv6 `2001:db8::1` 则得到
  `Endpoint = 2001:db8::1:51820`，`wg` 无法分辨哪个冒号是端口分隔符。
  这个缺陷的恶劣之处在于**面板侧完全不报错**：导出接口返回 200，
  配置文件看起来也正常，只有客户端 `wg setconf` 会拒绝，
  现象是「配置导出成功但怎么都连不上」，排查成本极高。
  真机上正是靠这条配置拉起真实 WireGuard 客户端时才暴露出来。
  现由 `joinEndpointPort` 统一归一化四种合法形态：
  裸地址补端口、已带端口原样保留、裸 IPv6 加方括号、已带方括号 IPv6 原样保留，
  并补 `TestRenderPeerConfigEndpointJoining` 钉死全部六种输入。
- `iptables` 参数顺序错误：动作标志被放在 `-t <table>` 之前，生成
  `-A -t nat POSTROUTING` 这类非法命令，导致 NAT 与 MSS 规则全部静默失败。
- `tc qdisc replace` 在 WireGuard 虚拟设备上不可用（内核不支持对已存在 qdisc 做 change），
  改为先 `del` 再 `add`。
- 限速 class 的 minor 号与 HTB 根 class 撞号，且挂在 `parent 1:` 而非 `parent 1:1`，
  导致 peer 之间没有真正的父子层级。已整体偏移并挂到根 class 下。
- 限速失败只记 DEBUG 且失败后仍写入指纹，导致一次失败即永久不再重试。
  改为首次 WARN，且仅在成功或明确不支持时记录指纹。
- **限速在内核状态被外部清空后不会自愈**。`tc` 状态只存在于内核内存，一次
  `tc qdisc del`、接口重建或容器迁移就会让它凭空消失，而数据库里的期望集合
  纹丝不动。原先只用「期望集合指纹」去重，因此始终判定无需下发，限速静默失效，
  只有等用户碰巧改了带宽才会恢复。这与其他内核态（接口、地址、路由）每轮校验
  并自愈的行为不一致。现在每次调谐会额外核对接口上是否真的还有本模块的 qdisc，
  缺失则重新下发；真机上实测删除后 2 秒内自愈，随后回到稳态。
- 非 root 容器内 `CapEff` 为 0（Docker 只把能力放进 bounding 集合，非 root 进程不继承），
  导致 `wg` 与 `iptables` 一律 EPERM。改为 root 运行配合 `cap_drop: ALL`，
  仅加回 `NET_ADMIN` 与 `DAC_OVERRIDE`。
- `/readyz` 把「转发已开启但 sysctl 只读」误判为不可用。
- **`/readyz` 掩盖调谐持续失败**。就绪探针此前只检查数据库、netlink、
  接口存在这几项静态条件，从不询问调谐器最近一次把期望态写入内核的结果。
  于是「接口在、数据库通、但每轮同步都在报错」这种状态依然返回 200，
  而期望配置实际从未生效——K8s 探针看到的就是一个健康的实例。
  现在 Reconciler 记录最近一次尝试的时间与错误，`/readyz` 在
  `status.reconcile` 暴露该状态；连续失败超过 3 个调谐周期后返回 503，
  单次成功即清零，避免瞬时抖动把实例拉黑。真机验证：注入错密钥后
  12 秒转 503，换回正确密钥后自动回到 200，稳态连续 15 秒无抖动。
- **主密钥丢失时静默重建新密钥**。`master.key` 文件不存在时的旧行为是
  直接生成一把新的 AES-256-GCM 主密钥并继续启动。数据库里所有服务端与
  Peer 私钥从此永久不可解密，而面板看起来完全正常——只有真正建连时
  才会发现客户端配置全部失效。现改为先探测数据库是否已存在密文，
  存在则拒绝启动并明确报错退出（`authn.ErrMasterKeyLost`），
  只有空库才允许自动生成。README 原本已声称这一行为，现与实现一致。
- **Peer 裸 IP 只校验不归一化**。`10.8.0.5` 这类不带掩码的地址能通过校验，
  却与内部归一化后的 `10.8.0.5/32` 形式并存于同一列，导致 IPAM 分配、
  限速 filter 匹配与去重统计三处判定不一致。现在创建时统一补全掩码。
- **批量操作产生事件风暴**。批量操作原本逐条发布事件，100 个 Peer 会打 100 条。
  现在批量路径汇总为一条带 Peer 名列表的事件。
- **只读接口把「读」放大成了「写」**。`GET /api/v1/metrics/overview` 走
  `Collector.SnapshotNow`，而它此前每次调用都无条件执行一次全量采集
  （遍历所有 Peer 取 netlink 计数 + 写 241 行 `traffic_sample`），
  采到的快照只用于本次返回，**采集周期内的缓存被直接丢弃**。
  后果是页面每 5 秒轮询一次 Overview，就等于每秒往库里塞 241 行样本：
  240 Peer 一天约 2082 万行，而接口全部 200、界面数字全部正确，
  没有任何可见异常。
  现改为采集周期内的冷缓存合并：`coldMu` 串行化「查缓存 → 采集 → 写回」
  全过程，命中缓存直接返回；按需采集只写缓存、不广播（广播仍由采集循环
  独占），采集失败不污染缓存。修完后 20 次请求只触发 1 次采集、只写 7 行样本。
  回归见 `internal/monitor/snapshot_test.go`（修复前该文件报出 20 次采集、
  8 次并发采集、140 行样本）。
- **`traffic_sample` 表没有任何索引**。曲线查询与保留策略清理都只能全表扫描：
  前者带 `TEMP B-TREE` 临时排序，后者在千万行量级下每轮清理都拖长事务。
  新增迁移 7 `traffic_sample_indexes`，补
  `idx_traffic_sample_ts(ts)` 与 `idx_traffic_sample_peer_ts(COALESCE(peer_id,0), ts)`
  （用表达式索引以同时服务 `peer_id IS NULL` 的汇总行查询）。
  真机验证查询计划变为
  `SEARCH traffic_sample USING INDEX idx_traffic_sample_peer_ts (<expr>=? AND ts>? AND ts<?)`
  与 `SEARCH traffic_sample USING COVERING INDEX idx_traffic_sample_ts (ts<?)`。
  回归用 `EXPLAIN QUERY PLAN` 的字面输出作断言
  （`internal/store/traffic_index_test.go`），比耗时阈值稳定、可重复。
- **SSE 的 `hello` 首帧写在订阅之前**。处理器的顺序是「写首帧 → flush →
  `Subscribe()` → 进入 select」，因此客户端收到 `hello` 时服务端还没订阅上。
  SSE 客户端（浏览器 `EventSource`、以及所有把 `hello` 当作「流已就绪」的代码）
  从这一刻开始就认为可以触发事件了，而这段空窗里发布的事件既不会被缓冲、
  也不会被感知——**事件被静默丢弃**，且没有任何错误。
  改为先 `Subscribe()` 再写首帧：空窗内的事件堆在带缓冲的 channel 里，
  首帧之后照常送达。`TestSSEPushLatencyAtPeerCeiling` 原本就会偶发
  「第 0 条事件 5 秒未送达」，单跑连过三次、全量跑才复现——这类只在负载下
  暴露的时序缺陷，只能靠全量 `go test -count=1` 反复跑出来。
- 另修正一条**测试断言本身的错误**（不是实现缺陷）：长稳用例原先用
  「`traffic_sample` 行数必须持续增长」来证明采集循环存活，而该用例同时在跑
  保留清理（每 700 ms 删掉 5 秒前的行），写入与删除互相抵消后行数本来就该持平。
  于是「保留策略生效」被误判成「采集停了」。改用最新样本时间戳推进作为存活判据，
  行数只作展示——**断言必须和被测系统的真实稳态一致**，否则它测的是自己的
  假设而不是系统。
- **`GET /api/v1/settings` 没有 RBAC 校验**。该路由组此前只在**写**的单个路由上
  挂了 `requirePerm(PermSettingsWrite)`，同组的 `GET /settings` 没有任何鉴权中间件——
  读接口比写接口更松，等于把备份目录、定时备份开关、采样间隔
  等全部运行时设置暴露给任意已登录的 viewer。改为在 `/settings` 路由组
  上用 `r.Use(...)` 统一收口，与 `/backups` 的写法对齐。
- **采样/调谐间隔能改但保存后不生效**。`PUT /settings` 写完数据库就返回，
  从不通知后台循环；两处 `time.Ticker` 一旦 `New` 就没有 `Reset` 的调用方，
  于是界面上把采样间隔从 2 秒改成 30 秒，返回 200、数据库也变了，
  采集频率却纹丝不动。现在 `reconciler` 与 `monitor` 都持有
  `intervalMu` + 带缓冲的 `retune chan`，保存时非阻塞投递新周期，
  循环内 `ticker.Reset(d)`。`SetInterval` 在值未变时**不**发送，
  避免重复保存相同设置唤醒后台 goroutine。
- **Go 零值 `time.Time` 在界面上显示成「739903 天前」**。Go 的零值序列化成
  `"0001-01-01T00:00:00Z"`，语义是「从未发生」，但它是 truthy 字符串，
  于是前端的 `a || b` 回退会选中它而不是回退到 `lastSeenAt`。
  结果每个从未握手的 Peer 都在「最后握手」列显示七十几万天前，
  绝对时间列还显示 `01/01`。修法两层：`formatRelative` / `formatTime`
  识别零值；取值处改用新的 `firstMeaningful(...)`，按候选顺序跳过
  无效值与零值时间。
- **移除 `peer.maxConnections`**。这是个**协议层面不存在的能力**：WireGuard 内核
  对每个 Peer 只保留一个 endpoint 槽位，「最大同时连接数」在数据面上没有对应物。
  之前它是一个能填、能存、但对握手毫无影响的字段——留着比删掉更危险，
  因为用户会以为它生效了。现在从创建/更新入参、界面表单与导入逻辑中移除
  （数据库列与 CSV 列位保留以兼容既有数据），并在 `domain/peer.go` 留下长注释
  说明为什么不能加回来。含该字段的请求现在返回 400 而不是被静默忽略。

### 补充：后端审计与浏览器验证轮

> 排在「修复」之后是因为这两项是审计过程中新发现的能力缺口，
> 属于补齐而非改动既有行为。

- **批量吊销的「全量」语义**。ROADMAP 一直写着「一键吊销指定**或全部** Peer」，
  但 `POST /peers/bulk-revoke` 只接受 `ids` 数组，传空数组直接 400，
  文档声称的能力接口并不存在——真遇上密钥泄露时，唯一的办法是让用户
  在浏览器里框选几百行。现在入参新增 `{"all": true}`，服务端自行遍历全量 Peer；
  界面把「批量吊销」与「全量吊销」拆成两个按钮，危险操作确认框里
  显示的确切数量与服务端将要遍历的数量一致。

### 移除

- **环境变量 `WGP_AUTO_RECONCILE` 与 `WGP_ENABLE_SWAGGER`**：两个都是无人读取的
  死配置——调谐循环无条件启动，Swagger 路由从未注册。留着它们会让人以为
  可以关掉调谐或打开 API 文档。同步删除 `config.AutoReconcile` /
  `config.EnableSwagger` 字段与 `.env.example` 中的对应行。

### 真机验证（M7）

以下结论取自真实节点（Docker bridge、`cap_drop: ALL` + `NET_ADMIN`/`DAC_OVERRIDE`、
发布 51820/udp），不是推测：

- **配额结算**：真机上无法像单测那样精确控制 `now`，改用直接拨动
  `quota_period_start`（该字段本就是引擎内部结算状态、不在 API 契约内）构造场景。
  首次观测只认领起点、**不清零**用量（预置 40000/100000 B 保持不变）；
  起点落后 3 天时**一个 tick 内**推进到今天并恢复 Peer（按周期逐步补跑是真实故障：
  落后 30 天就意味着 30 个 tick 期间用户仍超额可用）；同周期重复判定为空操作；
  起点写成明天（宿主 CST、容器 UTC，天然产生「同一绝对时刻、本地日期不同」）
  时不清零、不恢复、不改写已记录起点；拨回后 10 秒内补跑成功。
  全程 `policy run failed` 计数为 **0**。
- **API 性能（240 Peer）**：压测在容器 netns 内发起并等调谐收敛后才开压，
  `wg show wg0` 实测 243 个 Peer 与库内 `enabled` 数一致。
  REST 六个端点各 200 轮，P99 全部 < 30 ms（最高 `/api/v1/metrics/traffic?range=1h`
  13.61 ms）；`PUT /peers/{id}` P99 13.56 ms；SSE 端到端 P95 4.64 ms
  （口径为「发一次更新 → 该连接收到对应领域事件」，含传输、编码与缓冲）。
  压测用的 240 个 Peer 已全部删除，内核侧回到 3 个。
- **IPAM 硬上限是 253**：实测发现原定「300 Peer 压测」超出设计上限——
  `ipam.NewAllocator` 只接受 `/24 ~ /30`，`/24` 可用主机位 `.1 ~ .254`，
  扣掉服务端自身占的 `.1` 后只剩 253 个可分配地址。因此本文件与 ROADMAP 中的
  压测基线一律改为 **240**（上限的 95%），并由 `TestIPAMCeilingIsTheRealPeerLimit` 钉死。
- **长稳**：进程内加速泄漏验证已通过（SSE 断开后订阅者归零、后台循环不累积状态、
  Peer 反复增删不泄漏）。真机连续采样运行 **45 分钟后按用户要求取消**
  （原定 72 小时）：归档证据 11 轮，RSS 46.4 → 47.1 MB、goroutine 在 11–14 之间震荡、
  FD 恒定 18、`readyz` 全程 200，三项均无线性增长；同时删除了登记的定时任务
  与远端采样进程。**这一项不标记完成**——11 轮样本支撑不起「长稳无泄漏」的结论。
  与采样时长无关的真正防线是三条实现约束，已写成回归用例：
  订阅者表在断连时归零、`SetInterval` 值未变时不唤醒 goroutine、采集写库前冷缓存合并。
- **后端功能盘点**：对照 ROADMAP 逐项清点 18 个后端包、42 条路由，
  15 项能力中 13 项完整，发现并修复 5 个真实缺口（见上文「修复」前六条）；
  新增 `internal/httpapi/gaps_test.go`（8 个用例）逐条钉死。
- **真机冒烟**：匿名读设置 401、owner 200、viewer 403；
  空 `ids` 400、`{"all":true}` 吊销 14 个（即全库）；
  保存设置后日志出现 `metrics interval updated interval=3s` 与
  `reconcile interval updated interval=31s`；带 `maxConnections` 创建 400、
  正常创建返回 0。
- **浏览器逐页验证**（真实 UI，非接口模拟）：未登录跳 `/login`、登录、
  仪表盘实时指标，以及 `/`、`/monitor`、`/settings`、`/account`、`/audit`、
  `/system`、`/peers` 七页全部无崩溃、无控制台错误、无「739903 天前」、无 `01/01`。
  这一轮暴露了两个只有真实浏览器能发现的缺陷（零值时间、事件清单陈旧导致界面不刷新），
  已修。设置页改间隔为 7/42 并保存，面板日志即时出现
  `interval=7s` / `interval=42s`，界面承诺的「保存后立即生效」现在成立。

### 已知限制

- **带宽限速依赖 IFB + HTB，宿主需能创建 IFB 设备**。此前使用 `tc police`
  在接口上直接整形，实测在定制内核上计数恒为 0 且精度不可用。
  现改为 `wgifb0` IFB 设备 + ingress mirred 重定向 + 双向 HTB 树：
  下行按 Peer 地址 `dst` 分类，上行经 IFB 后按 `src` 分类。
  真机验证覆盖多 Peer 同时限速、动态改带宽、外部篡改后 2 秒内自愈、
  取消限速后 qdisc 完整清理。需要注意两条踩坑：
  `u32 filter replace` 必须用唯一 `(parent, prio, handle)` 定位，
  只给 prio 会变成追加而非替换；删除 ingress qdisc 的正确语法是
  `tc qdisc del dev wg0 ingress`，不是按 handle 删。
  宿主内核仍需支持 `sch_htb` 与 `cls_u32`，能力缺口会在启动日志中提示。

### 安全

- 容器以 root 运行但能力集压到最小：`cap_drop: ALL` 后仅加回 `NET_ADMIN`（配置 WireGuard、
  路由与 NAT）与 `DAC_OVERRIDE`（读写属主为 uid 10001 的数据卷）。
  非 root + `cap_add` 在 Docker 上不可靠，`SYS_MODULE` 不再作为默认建议，改为由宿主侧加载模块。
- 刷新令牌 Cookie 为 HttpOnly + SameSite，`Secure` 由 `WGP_COOKIE_SECURE` 控制。
- 仅在配置可信代理时才解析 `X-Forwarded-For`，并在文档中明确禁止 `WGP_TRUSTED_PROXY=0.0.0.0/0`。
- CSP 收紧为 `default-src 'self'`、`object-src 'none'`、`frame-ancestors 'none'`。
- 私钥经 AES-256-GCM 加密后落库，CSV 导出永不含私钥。
- **跨实例恢复必须同时迁移主密钥**。`.wgbak` 用面板主密钥加密，主密钥本身
  不包含在备份内——这是有意的：备份文件泄漏到别处也无法解开。
  代价是灾备演练时需要单独保存 `master.key`，只搬 `.wgbak` 会得到
  「backup cannot be decrypted with the current master key」而不是半个可用的实例。
  这一点在恢复接口的报错与部署文档里都已写明。
  真机验证：带原主密钥跨容器恢复后，服务端私钥与全部 Peer 私钥的
  SHA-256 指纹与恢复前逐字节一致，二次恢复幂等，内核 Peer 同步重建。

## [0.1.0]

### 新增

- M0 架构规划与工程骨架：ARCHITECTURE.md、ROADMAP.md、前后端可编译工程、Docker 交付物。
- M1 核心链路：SQLite 迁移、wgctrl 驱动、netdriver、IPAM、声明式 Reconciler、
  Server/Peer CRUD、JWT + 刷新令牌鉴权。
- M2 Web UI：登录、仪表盘、客户端管理、服务端设置。
- M3 实时监控：SSE 推送、指标采集、流量按 `peer_id` 归因、逐级降采样。
- M4 加固：RBAC 四角色、审计日志、登录限流、健康探针、代理信任、CSP。

[未发布]: https://github.com/Rewind2026/wgpanel/compare/v0.10.0...HEAD
[0.10.0]: https://github.com/Rewind2026/wgpanel/compare/v0.9.0...v0.10.0
[0.9.0]: https://github.com/Rewind2026/wgpanel/compare/v0.1.0...v0.9.0