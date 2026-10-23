package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/Rewind2026/wgpanel/internal/authn"
	"github.com/Rewind2026/wgpanel/internal/backup"
	"github.com/Rewind2026/wgpanel/internal/config"
	"github.com/Rewind2026/wgpanel/internal/domain"
	"github.com/Rewind2026/wgpanel/internal/eventbus"
	"github.com/Rewind2026/wgpanel/internal/httpapi"
	"github.com/Rewind2026/wgpanel/internal/monitor"
	"github.com/Rewind2026/wgpanel/internal/netdriver"
	"github.com/Rewind2026/wgpanel/internal/policy"
	"github.com/Rewind2026/wgpanel/internal/reconciler"
	"github.com/Rewind2026/wgpanel/internal/scheduler"
	"github.com/Rewind2026/wgpanel/internal/service"
	"github.com/Rewind2026/wgpanel/internal/store"
	"github.com/Rewind2026/wgpanel/internal/version"
	"github.com/Rewind2026/wgpanel/internal/wgdriver"
)

func main() {
	if err := run(); err != nil {
		slog.Error("fatal", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	log := newLogger(cfg)
	slog.SetDefault(log)

	log.Info("starting wgpanel",
		"version", version.Version,
		"commit", version.Commit,
		"dataDir", cfg.DataDir,
		"listen", cfg.ListenAddr,
		"nativeMode", cfg.NativeMode,
	)

	netMode, err := netdriver.ParseMode(cfg.NativeMode)
	if err != nil {
		return err
	}

	if err := cfg.EnsureDataDir(); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st, err := store.Open(ctx, cfg)
	if err != nil {
		return err
	}
	defer st.Close()

	masterKey, err := resolveMasterKey(ctx, st, cfg)
	if err != nil {
		return err
	}
	cipher, err := authn.NewCipher(masterKey)
	if err != nil {
		return err
	}
	jwtSecret, err := resolveJWTSecret(cfg)
	if err != nil {
		return err
	}

	// 网络驱动负责链路/地址/路由/NAT，控制面驱动负责 WireGuard 协议配置。
	netDrv := netdriver.Platform()
	caps := netDrv.Probe()
	for _, p := range caps.Problems(netMode) {
		log.Warn("network capability gap", "mode", netMode, "detail", p)
	}

	// 内核驱动不可用时降级为 stub：面板仍可读写配置，接口不会误报为正常。
	driver, driverErr := wgdriver.New(netDrv)
	if driverErr != nil {
		log.Warn("wireguard control plane unavailable, management-only mode", "error", driverErr)
		driver = nil
	} else if err := driver.Available(); err != nil {
		log.Warn("wireguard driver not ready", "error", err,
			"hint", "宿主需加载 wireguard 内核模块，容器以 --cap-add NET_ADMIN 启动")
	}
	if driver != nil {
		defer driver.Close()
	}
	active := safeDriver(driver)

	issuer := authn.NewTokenIssuer(jwtSecret, cfg.SessionTTL)
	limiter := authn.NewLoginLimiter(cfg.LoginMaxAttempts, cfg.LoginLockWindow)
	authSvc := service.NewAuthService(st.UserRepo(), issuer, limiter,
		cfg.SessionTTL, cfg.RefreshTTL, st.AuditRepo())

	serverSvc := service.NewServerService(st.ServerRepo(), cipher, st.AuditRepo(), cfg, nil)
	peerSvc := service.NewPeerService(st.PeerRepo(), st.ServerRepo(), cipher,
		st.AuditRepo(), active, nil)
	accountSvc := service.NewAccountService(st.UserRepo(), st.AuditRepo())

	// 事件总线是进程内唯一的出口：SSE 实时推送挂在它上面，
	// 新增事件发布点时不需要同时改推送与其他消费链路。
	bus := eventbus.New(log, 64)

	// Peer 的增删改必须走总线：不接上就是「配置改了但界面不更新」。
	peerSvc.SetPublisher(bus)

	policyEngine := policy.New(st.PeerRepo(), st.SettingRepo(), st.AuditRepo(), bus, log)
	backupSvc := backup.New(st, cipher, cfg.DataDir, log)

	// 服务层需要触发调谐，调谐器又依赖服务层读取期望态，用 holder 打破循环依赖。
	var holder *reconcilerHolder
	if driver != nil {
		holder = new(reconcilerHolder)
		recon := reconciler.New(driver, netDrv, &stateProvider{servers: serverSvc, peers: peerSvc},
			cfg.ReconcileInterval, log, reconciler.WithNetworkMode(netMode))
		holder.bind(recon)
		serverSvc.SetReconcile(func(ctx context.Context) { holder.Reconcile(ctx) })
		peerSvc.SetReconcile(func(ctx context.Context) { holder.Reconcile(ctx) })
		// 策略引擎只改数据库里的 enabled，改完立即调谐让内核跟上。
		policyEngine.SetReconcile(func() { holder.Trigger() })
	}

	if err := serverSvc.EnsureInitialized(ctx); err != nil {
		return err
	}
	generated, err := authSvc.EnsureOwner(ctx, cfg.AdminUsername, cfg.AdminPassword)
	if err != nil {
		return err
	}
	if generated != "" {
		log.Warn("initial administrator created — this password is shown only once",
			"username", cfg.AdminUsername, "password", generated)
	}

	endpoint := func(ctx context.Context) string {
		return scheduler.DetectPublicEndpoint(ctx, serverSvc, st.SettingRepo())
	}
	collector := monitor.New(&snapshotProvider{
		servers:  serverSvc,
		peers:    peerSvc,
		endpoint: endpoint,
	}, active, st.TrafficRepo(), cfg.SampleInterval, log)
	// 采集器负责把「内核计数器的增量」翻译成领域事实：
	// 配额累加、上下线判定都从这里出去，避免与调谐器各算各的。
	collector.SetSinks(st.PeerRepo(), bus)

	var lastReconcile func() time.Time
	var lastNetErr func() error
	var lastRateErr func() error
	var triggerReconcile func()
	var reconcileStatus func() (error, bool)
	var setReconcileInterval func(time.Duration)
	if holder != nil {
		lastReconcile = holder.LastSuccess
		lastNetErr = holder.reconciler.LastNetworkError
		lastRateErr = holder.reconciler.RateLimitError
		triggerReconcile = holder.Trigger
		reconcileStatus = holder.reconciler.SyncError
		setReconcileInterval = holder.reconciler.SetInterval
	}

	api := httpapi.New(httpapi.Deps{
		Config:               cfg,
		Log:                  log,
		Store:                st,
		Auth:                 authSvc,
		Account:              accountSvc,
		Servers:              serverSvc,
		Peers:                peerSvc,
		Collector:            collector,
		Traffic:              st.TrafficRepo(),
		Audit:                st.AuditRepo(),
		Driver:               active,
		Net:                  netDrv,
		Events:               bus,
		Settings:             st.SettingRepo(),
		Backups:              backupSvc,
		Version:              version.Version,
		CookieSecure:         cfg.CookieSecure,
		Reconcile:            triggerReconcile,
		SetReconcileInterval: setReconcileInterval,
		LastReconcile:        lastReconcile,
		LastNetworkError:     lastNetErr,
		LastRateError:        lastRateErr,
		ReconcileStatus:      reconcileStatus,
		NetMode:              netMode,
		PublicEndpoint:       func() string { return endpoint(context.Background()) },
		Tickets:              authn.NewStreamTicketStore(),
	})

	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           api.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		// SSE 需要长连接，故不设置 WriteTimeout，由中间件 Timeout 兜底。
		IdleTimeout: 120 * time.Second,
	}

	if cfg.MetricsEnabled {
		go collector.Start(ctx)
	}
	if holder != nil {
		go holder.reconciler.Start(ctx)
	}
	go scheduler.New(cfg, st, log).Start(ctx)
	go policyEngine.Start(ctx, cfg.PolicyInterval)
	scheduler.StartBackupLoop(ctx, st.SettingRepo(), backupSvc,
		func(ev domain.Event) { bus.Publish(ctx, ev) }, log)

	errCh := make(chan error, 1)
	go func() {
		log.Info("http server listening", "addr", cfg.ListenAddr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		log.Info("shutdown signal received")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return err
	}
	log.Info("shutdown complete")
	return nil
}

// newLogger 按配置初始化结构化日志。
func newLogger(cfg config.Config) *slog.Logger {
	level := slog.LevelInfo
	switch strings.ToLower(cfg.LogLevel) {
	case "debug":
		level = slog.LevelDebug
	case "warn", "warning":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}
	opts := &slog.HandlerOptions{Level: level}
	if strings.EqualFold(cfg.LogFormat, "json") {
		return slog.New(slog.NewJSONHandler(os.Stdout, opts))
	}
	return slog.New(slog.NewTextHandler(os.Stdout, opts))
}

// resolveMasterKey 读取环境变量主密钥，否则读写数据目录中的密钥文件。
//
// 库里已经有加密数据却找不到密钥文件时直接失败：那种状态下生成一把新密钥，
// 不会让面板「恢复可用」，只会把所有私钥变成永远解不开的密文——
// 界面正常、接口正常、就是一台连不上任何客户端的僵尸面板。
func resolveMasterKey(ctx context.Context, st *store.Store, cfg config.Config) ([]byte, error) {
	if cfg.MasterKey != "" {
		return []byte(cfg.MasterKey), nil
	}
	hasSecrets, err := st.HasEncryptedSecrets(ctx)
	if err != nil {
		return nil, err
	}
	if err := authn.EnsureMasterKeyPresent(cfg.MasterKeyPath(), hasSecrets); err != nil {
		return nil, err
	}
	return authn.LoadOrCreateMasterKey(cfg.MasterKeyPath())
}

// resolveJWTSecret 读取环境变量签名密钥，否则持久化一份随机密钥。
func resolveJWTSecret(cfg config.Config) ([]byte, error) {
	if cfg.JWTSecret != "" {
		return []byte(cfg.JWTSecret), nil
	}
	return authn.LoadOrCreateSecret(cfg.SecretPath(), 48)
}

// safeDriver 在驱动缺失时返回 stub，避免各处判空。
func safeDriver(d wgdriver.Driver) wgdriver.Driver {
	if d != nil {
		return d
	}
	return wgdriver.NewStub()
}

// reconcilerHolder 让服务层在写操作后同步触发一次调谐。
type reconcilerHolder struct {
	reconciler *reconciler.Reconciler
}

func (h *reconcilerHolder) bind(r *reconciler.Reconciler) { h.reconciler = r }

// Trigger 请求尽快调谐一次，不阻塞调用方。
func (h *reconcilerHolder) Trigger() {
	if h.reconciler != nil {
		h.reconciler.Trigger()
	}
}

// Reconcile 在当前 goroutine 上执行调谐，使写接口返回时内核状态已一致。
func (h *reconcilerHolder) Reconcile(ctx context.Context) {
	if h.reconciler != nil {
		if err := h.reconciler.Reconcile(ctx); err != nil {
			slog.Debug("post-write reconcile failed", "error", err)
		}
	}
}

// LastSuccess 返回最近一次成功调谐时间。
func (h *reconcilerHolder) LastSuccess() time.Time {
	if h.reconciler == nil {
		return time.Time{}
	}
	return h.reconciler.LastSuccess()
}

// stateProvider 把服务层适配为调谐器的期望态来源。
type stateProvider struct {
	servers *service.ServerService
	peers   *service.PeerService
}

func (p *stateProvider) ServerConfig(ctx context.Context) (*domain.ServerConfig, error) {
	return p.servers.Get(ctx)
}

func (p *stateProvider) ActivePeers(ctx context.Context) ([]domain.Peer, error) {
	return p.peers.ActivePeers(ctx)
}

// snapshotProvider 为采集器提供数据源。
type snapshotProvider struct {
	servers  *service.ServerService
	peers    *service.PeerService
	endpoint func(context.Context) string
}

func (p *snapshotProvider) ServerConfig(ctx context.Context) (*domain.ServerConfig, error) {
	return p.servers.Get(ctx)
}

func (p *snapshotProvider) ListPeers(ctx context.Context) ([]domain.Peer, error) {
	return p.peers.List(ctx)
}

func (p *snapshotProvider) PublicEndpoint(ctx context.Context) string { return p.endpoint(ctx) }
