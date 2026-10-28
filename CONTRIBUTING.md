# 贡献指南

感谢你愿意为 WGPanel 出力。这份文档说明本地环境、代码约定与提交流程。

## 开发环境

| 依赖 | 版本 | 说明 |
|---|---|---|
| Go | 1.26 | 后端与工具链 |
| Node.js | 22 | 前端构建；低于 20 会因 `crypto.randomUUID` 等特性失败 |
| Docker | 20.10+ | 仅用于验证镜像；非必需 |

## 本地起服务

```bash
# 后端（默认监听 :8080，数据落在 ./data）
go run ./cmd/wgpanel

# 前端（Vite 热更新，:5173，已配置代理到 :8080）
cd web && npm install && npm run dev
```

面板首次启动会创建数据目录、生成主密钥并初始化 owner 账户。
初始口令随机生成并打印到日志，**只显示一次**。

需要内核能力时，在容器里开发，或给宿主机上的进程授予 `CAP_NET_ADMIN`：

```bash
sudo setcap cap_net_admin+ep $(which wgpanel)
```

宿主没有 `wireguard` 内核模块时先加载：`sudo modprobe wireguard`。

## 提交前必过

```bash
export PATH=$PATH:/usr/local/go/bin

gofmt -l .            # 必须无输出
go vet ./...
go test -race ./...
cd web && npm run lint && npm run build
```

CI 跑的也是这几条；本地先过一遍能省一次往返。

## 代码约定

### 包依赖方向

单向依赖，不得反向或跨层引用：

```
domain ← store / wgdriver / netdriver / authn
       ← service / policy / backup / eventbus / reqmeta
       ← reconciler / scheduler / monitor / httpapi ← cmd
```

`domain` 不 import 任何上层包。新增领域类型时先想清楚它属于哪个层，
不要为了「顺手」在 `domain` 里引用 `httpapi`。

### 后端

- 错误用 `domain.ErrXxx` 构造，携带 HTTP 状态与业务错误码；
  不要在 handler 里手写 `respondError(w, log, errors.New(...))`。
- 所有 SQL 走 `internal/store` 的仓储方法，不在 service 层直接拼 SQL。
- 并发访问共享状态加锁或用 channel 串行化。Reconciler 是并发热点，
  改动它务必跑 `go test -race ./internal/reconciler/...`。
- 面向用户的错误文案用中文，解释「怎么办」而不只是「哪错了」。

### 前端

- 组件用 `@/` 别名导入，路径大小写必须与磁盘一致（生产构建区分大小写）。
- 列表页统一走 `@tanstack/react-query`，不要手写 `useEffect` + `fetch`。
- 权限判断用 `roleCan(user.role, 'xxx')`，与后端 `domain.Perm*` 一一对应。
  后端才是安全边界，前端判断只用于隐藏不可用控件。
- 破坏性操作走 `ConfirmDialog`，不要用 `window.confirm`。
- 新图表优先手写 SVG（见 `components/charts/TrafficChart.tsx`）。
  引入图表库会让首屏 gzip 体积翻几倍，与「首屏 < 400 KB」的验收标准冲突。
- TypeScript 开了 `strict` + `noUnusedLocals` + `noUnusedParameters`，
  留一个没用到的 import 就构建失败。
- 改站点图标只改 `web/public/favicon.svg` 这一个主源文件，
  然后跑 `cd e2e/ui && node render-favicon.mjs` 重新生成 PNG/ICO 各尺寸并一并提交。
  不要单独手改生成的 PNG——下次跑脚本会被覆盖。

### 提交信息

Conventional Commits，标题用祈使句、不加句号：

```
feat(peer): 支持批量标签与批量启停
fix(policy): 修正配额周期在重启后被重置的问题
docs(readme): 补充 host 模式的前置条件
```

涉及内核行为的改动（netdriver / wgdriver / reconciler），
请在 PR 描述里写清楚**在真实宿主上验证过什么**，
而不只是「单测通过」——这部分逻辑单测覆盖不到。

## PR 流程

1. 从 `main` 切分支：`feat/xxx`、`fix/xxx`、`docs/xxx`。
2. 改动尽量小而聚焦。一个 PR 做两件事，比拆成两个更容易 review。
3. 描述里写清：动机、改法、验证方式。
4. CI 全绿后请求 review。

## 报告安全问题

**不要**用公开 issue 报告安全漏洞。请通过
[GitHub Security Advisories](https://github.com/Rewind2026/wgpanel/security/advisories/new)
私下联系维护者，给我们留出修复时间。

## 行为准则

参与即表示同意 [行为准则](CODE_OF_CONDUCT.md)。

## 许可

贡献的代码以 MIT 许可证发布。