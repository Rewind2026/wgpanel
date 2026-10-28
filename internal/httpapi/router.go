package httpapi

import (
	"context"
	"io/fs"
	"log/slog"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"

	"github.com/Rewind2026/wgpanel/internal/authn"
	"github.com/Rewind2026/wgpanel/internal/backup"
	"github.com/Rewind2026/wgpanel/internal/config"
	"github.com/Rewind2026/wgpanel/internal/domain"
	"github.com/Rewind2026/wgpanel/internal/eventbus"
	"github.com/Rewind2026/wgpanel/internal/monitor"
	"github.com/Rewind2026/wgpanel/internal/netdriver"
	"github.com/Rewind2026/wgpanel/internal/service"
	"github.com/Rewind2026/wgpanel/internal/store"
	"github.com/Rewind2026/wgpanel/internal/web"
	"github.com/Rewind2026/wgpanel/internal/wgdriver"
)

// refreshCookieName 是刷新令牌的 Cookie 名。
const refreshCookieName = "wgp_refresh"

// Deps 是 Server 依赖的全部组件。
type Deps struct {
	Config    config.Config
	Log       *slog.Logger
	Store     *store.Store
	Auth      *service.AuthService
	Account   *service.AccountService
	Servers   *service.ServerService
	Peers     *service.PeerService
	Collector *monitor.Collector
	Traffic   *store.TrafficRepo
	Audit     *store.AuditRepo
	Driver    wgdriver.Driver
	Net       netdriver.NetDriver
	Events    *eventbus.Bus
	Settings  *store.SettingRepo
	Backups   *backup.Service
	Version   string
	// CookieSecure 控制刷新令牌 Cookie 是否带 Secure 标记。
	CookieSecure bool
	// Reconcile 触发一次立即调谐，供恢复备份后收敛内核状态使用。
	Reconcile        func()
	LastReconcile    func() time.Time
	LastNetworkError func() error
	// SetReconcileInterval 在保存设置后把新的调谐间隔推给调谐器。
	// 不接这个回调的话，「调谐间隔」就只是一个落库字段：界面提示保存成功，
	// 实际要重启进程才生效。
	SetReconcileInterval func(time.Duration)
	// LastRateError 返回最近一次带宽限速下发错误，用于就绪探针暴露降级状态。
	LastRateError func() error
	// ReconcileStatus 返回最近一次调谐的错误与「是否已持续失败」。
	// 持续失败意味着期望态没落到内核，实例必须判为不就绪。
	ReconcileStatus func() (error, bool)
	// NetMode 声明进程所处的网络形态，决定就绪探针如何判定转发/NAT 缺失。
	NetMode        netdriver.Mode
	PublicEndpoint func() string
	// Tickets 是 SSE 一次性票据表；为 nil 时 New 会自行构造。
	Tickets *authn.StreamTicketStore
}

// Server 承载 HTTP 路由与处理器。
type Server struct {
	cfg           config.Config
	log           *slog.Logger
	store         *store.Store
	auth          *service.AuthService
	account       *service.AccountService
	servers       *service.ServerService
	peers         *service.PeerService
	collector     *monitor.Collector
	traffic       *store.TrafficRepo
	audit         *store.AuditRepo
	driver        wgdriver.Driver
	net           netdriver.NetDriver
	events        *eventbus.Bus
	settings      *store.SettingRepo
	backups       *backup.Service
	version       string
	trustProxy    bool
	cookieSecure  bool
	startedAt     time.Time
	reconcile     func()
	setInterval   func(time.Duration)
	lastReconcile func() time.Time
	netErr        func() error
	rateErr       func() error
	syncStatus    func() (error, bool)
	netMode       netdriver.Mode
	endpoint      func() string
	tickets       *authn.StreamTicketStore
}

// New 构造 HTTP 服务并装配路由。
func New(d Deps) *Server {
	s := &Server{
		cfg:           d.Config,
		log:           d.Log,
		store:         d.Store,
		auth:          d.Auth,
		account:       d.Account,
		servers:       d.Servers,
		peers:         d.Peers,
		collector:     d.Collector,
		traffic:       d.Traffic,
		audit:         d.Audit,
		driver:        d.Driver,
		net:           d.Net,
		events:        d.Events,
		settings:      d.Settings,
		backups:       d.Backups,
		version:       d.Version,
		trustProxy:    d.Config.TrustedProxy != "",
		cookieSecure:  d.CookieSecure,
		startedAt:     time.Now(),
		reconcile:     d.Reconcile,
		setInterval:   d.SetReconcileInterval,
		lastReconcile: d.LastReconcile,
		netErr:        d.LastNetworkError,
		rateErr:       d.LastRateError,
		syncStatus:    d.ReconcileStatus,
		netMode:       d.NetMode,
		endpoint:      d.PublicEndpoint,
		tickets:       d.Tickets,
	}
	// 票据表是 SSE 鉴权的必需依赖，缺了会让整条实时链路 401，
	// 与其运行时 panic，不如在装配阶段就暴露。
	if s.tickets == nil {
		s.tickets = authn.NewStreamTicketStore()
	}
	return s
}

// publicEndpoint 返回对外可达的服务端地址，没有探测结果时返回空串。
func (s *Server) publicEndpoint() string {
	if s.endpoint == nil {
		return ""
	}
	return s.endpoint()
}

// publish 广播一条领域事件。
func (s *Server) publish(ev domain.Event) {
	if s.events == nil {
		return
	}
	s.events.Publish(context.Background(), ev)
}

// Handler 返回装配完成的路由。
func (s *Server) Handler() http.Handler {
	r := chi.NewRouter()
	r.Use(chimw.RequestID)
	r.Use(reqMetaMiddleware(s.trustProxy))
	// 真实 IP 一律由 clientIP(r, trustProxy) 解析，不使用 chi.RealIP：
	// 后者会无条件信任 X-Forwarded-For，任何人都能伪造来源 IP 绕过登录限流。
	r.Use(loggerMiddleware(s.log, s.trustProxy))
	r.Use(chimw.Recoverer)
	r.Use(timeoutMiddleware(30 * time.Second))
	r.Use(securityHeaders)

	r.Get("/healthz", s.handleHealth)
	r.Get("/readyz", s.handleReady)
	r.Get("/metrics", s.handlePrometheus)
	r.Get("/api/v1/version", func(w http.ResponseWriter, _ *http.Request) {
		respondJSON(w, http.StatusOK, map[string]any{"version": s.version})
	})

	// 订阅端点不挂面板鉴权：v2rayNG / Shadowrocket 这类客户端
	// 无法携带面板的 JWT，令牌本身即凭据。
	r.Get("/api/v1/subscription/{token}", s.handleSubscriptionConfig)

	r.Route("/api/v1", func(r chi.Router) {
		r.Route("/auth", func(r chi.Router) {
			r.Post("/login", s.handleLogin)
			r.Post("/refresh", s.handleRefresh)
			r.Post("/logout", s.handleLogout)
			r.Group(func(r chi.Router) {
				r.Use(authnMiddleware(NewAuthenticator(s.auth), s.trustProxy))
				r.Get("/me", s.handleMe)
				r.With(requirePerm(domain.PermMetricsRead, s.log)).Post("/stream-ticket", s.handleIssueStreamTicket)
			})
		})

		r.Group(func(r chi.Router) {
			r.Use(authnMiddleware(NewAuthenticator(s.auth), s.trustProxy))

			r.Route("/server", func(r chi.Router) {
				r.With(requirePerm(domain.PermServerRead, s.log)).Get("/", s.handleGetServer)
				r.With(requirePerm(domain.PermServerWrite, s.log)).Put("/", s.handleUpdateServer)
				r.With(requirePerm(domain.PermServerRead, s.log)).Get("/revisions", s.handleServerRevisions)
				r.With(requirePerm(domain.PermSystemShutdown, s.log)).Post("/reset-key", s.handleRotateServerKey)
				r.With(requirePerm(domain.PermConfigExport, s.log)).Get("/config", s.handleServerConfigText)
			})

			r.Route("/peers", func(r chi.Router) {
				r.With(requirePerm(domain.PermPeerRead, s.log)).Get("/", s.handleListPeers)
				r.With(requirePerm(domain.PermPeerCreate, s.log)).Post("/", s.handleCreatePeer)
				r.With(requirePerm(domain.PermPeerRevoke, s.log)).Post("/bulk-revoke", s.handleBulkRevoke)
				r.With(requirePerm(domain.PermPeerUpdate, s.log)).Post("/bulk-tag", s.handleBulkTag)
				r.With(requirePerm(domain.PermPeerUpdate, s.log)).Post("/bulk-enable", s.handleBulkEnable)
				r.With(requirePerm(domain.PermConfigExport, s.log)).Get("/export", s.handleExportPeersCSV)
				r.With(requirePerm(domain.PermConfigImport, s.log)).Post("/import", s.handleImportPeersCSV)
				r.With(requirePerm(domain.PermPeerUpdate, s.log)).Put("/{id}", s.handleUpdatePeer)
				r.With(requirePerm(domain.PermPeerDelete, s.log)).Delete("/{id}", s.handleDeletePeer)
				r.With(requirePerm(domain.PermPeerRevoke, s.log)).Post("/{id}/rotate-key", s.handleRotatePeerKey)
				r.With(requirePerm(domain.PermConfigExport, s.log)).Get("/{id}/config", s.handlePeerConfig)
				r.With(requirePerm(domain.PermConfigExport, s.log)).Post("/{id}/subscription", s.handleIssueSubscription)
				r.With(requirePerm(domain.PermConfigExport, s.log)).Delete("/{id}/subscription", s.handleRevokeSubscription)
			})

			// 单人部署：不提供建号、删号与角色分配。
			// 这里只挂两件事——读自己的资料、改自己的密码，
			// 身份一律取自请求 principal，请求体里没有 userId。
			r.Route("/account", func(r chi.Router) {
				r.Get("/profile", s.handleGetProfile)
				r.Put("/password", s.handleChangePassword)
			})

			// 读也要鉴权。设置里含备份目录、出口地址、策略开关等运行参数，
			// 泄漏本身不致命，但与同级的 /backups 要求 settings:write 不一致——
			// 那组是整组挂中间件，唯独这里漏了读侧，等于把写权限的护栏只装了一半。
			// 复用 settings:write 而不新增读权限点：设置没有独立于运维的
			// 只读语义，能读的人一定能改，多一个权限点只会让矩阵更难解释。
			r.Route("/settings", func(r chi.Router) {
				r.Use(requirePerm(domain.PermSettingsWrite, s.log))
				r.Get("/", s.handleGetSettings)
				r.Put("/", s.handleUpdateSettings)
			})

			// 网络诊断与设置同级：模式、接口地址、RA 链路都是部署环境的运行参数，
			// 复用 settings:write 权限点，不新增独立读权限。
			r.Route("/system", func(r chi.Router) {
				r.Use(requirePerm(domain.PermSettingsWrite, s.log))
				r.Get("/network", s.handleSystemNetwork)
			})

			r.Route("/backups", func(r chi.Router) {
				r.Use(requirePerm(domain.PermSettingsWrite, s.log))
				r.Get("/", s.handleListBackups)
				r.Post("/", s.handleCreateBackup)
				r.Get("/{filename}", s.handleDownloadBackup)
				r.Post("/{filename}/restore", s.handleRestoreBackup)
				r.Delete("/{filename}", s.handleDeleteBackup)
			})

			r.Route("/metrics", func(r chi.Router) {
				r.Use(requirePerm(domain.PermMetricsRead, s.log))
				r.Get("/overview", s.handleMetricsOverview)
				r.Get("/peers", s.handlePeerMetrics)
				r.Get("/traffic", s.handleTrafficHistory)
			})

			r.With(requirePerm(domain.PermAuditRead, s.log)).Get("/audit", s.handleAuditQuery)
			r.With(requirePerm(domain.PermAuditRead, s.log)).Get("/audit/export", s.handleExportAudit)
		})
	})

	// SSE 单独挂在 /api/v1 鉴权组之外：浏览器 EventSource 无法设置请求头，
	// 走 authnMiddleware 必然 401。这里接受换来的短期票据，
	// 但权限判定照旧——票据只解决「怎么带上身份」，不绕过 RBAC。
	r.With(s.streamAuthnMiddleware()).Get("/api/v1/metrics/stream", s.handleRealtime)

	s.mountWebUI(r)
	return r
}

// mountWebUI 挂载前端静态资源，并对未知路径回退到 index.html（SPA 路由）。
//
// 注意：回退时不能用 http.FileServer 服务 index.html——它会把以 index.html
// 结尾的路径 301 重定向到 "./"，在 SPA 路由下形成无限重定向环。
// 因此 index.html 直接从文件系统读出后写出。
func (s *Server) mountWebUI(r chi.Router) {
	assets, err := web.FS(s.cfg.WebAssetsDir)
	if err != nil {
		s.log.Warn("web assets unavailable, API only mode", "error", err)
		return
	}

	// index.html 在请求时解析一次，避免每帧重复读取。
	indexHTML, err := fs.ReadFile(assets, "index.html")
	if err != nil {
		s.log.Warn("web index.html unavailable, API only mode", "error", err)
		return
	}

	fileServer := http.FileServer(http.FS(assets))

	r.Get("/*", func(w http.ResponseWriter, req *http.Request) {
		clean := strings.TrimPrefix(path.Clean("/"+req.URL.Path), "/")
		if clean == "" || clean == "." {
			clean = "index.html"
		}

		// FileServer 会把目录路径（含根路径）301 重定向到 "./"，
		// 因此 index.html 一律直接写出，不经过 FileServer。
		if clean == "index.html" {
			s.writeIndex(w, indexHTML)
			return
		}

		if info, err := fs.Stat(assets, clean); err == nil && !info.IsDir() {
			req = req.Clone(req.Context())
			req.URL.Path = "/" + clean
			// .webmanifest 不在 Go 内置 MIME 表里，FileServer 会回退成
			// text/plain，浏览器因 Content-Type 不符而拒绝加载 manifest。
			// 这里显式指定，不依赖系统 mime.types（容器里不一定有）。
			if ext := path.Ext(clean); ext == ".webmanifest" {
				w.Header().Set("Content-Type", "application/manifest+json")
			}
			fileServer.ServeHTTP(w, req)
			return
		}

		// 未命中的路径视为前端路由，回退到 index.html。
		s.writeIndex(w, indexHTML)
	})
}

// writeIndex 输出 SPA 入口页。不缓存，保证发版后立即生效。
func (s *Server) writeIndex(w http.ResponseWriter, indexHTML []byte) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(indexHTML); err != nil {
		s.log.Debug("write index.html failed", "error", err)
	}
}
