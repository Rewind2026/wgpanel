package reconciler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"

	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"

	"github.com/Rewind2026/wgpanel/internal/domain"
	"github.com/Rewind2026/wgpanel/internal/netdriver"
	"github.com/Rewind2026/wgpanel/internal/wgdriver"
)

// netResyncInterval 网络层（转发/路由/NAT）的兜底重放周期。
// 这些操作会 spawn iptables 进程，代价高于内存态比对，因此不跟每轮调谐一起跑。
const netResyncInterval = 5 * time.Minute

// probeInterval 能力探测缓存周期，避免每 2 秒重复探测内核与容器权限。
const probeInterval = 30 * time.Second

// defaultInterval 是调谐缺省间隔，供未显式配置时推导容忍窗口。
const defaultInterval = 2 * time.Second

// DesiredStateProvider 提供期望态（来源于数据库）。
type DesiredStateProvider interface {
	ServerConfig(ctx context.Context) (*domain.ServerConfig, error)
	ActivePeers(ctx context.Context) ([]domain.Peer, error)
}

// Reconciler 周期性地把数据库中的期望态同步到内核，修复外部改动导致的漂移。
type Reconciler struct {
	driver wgdriver.Driver
	net    netdriver.NetDriver
	source DesiredStateProvider
	mode   netdriver.Mode
	log    *slog.Logger

	// interval 由 intervalMu 保护，可被 SetInterval 在运行期改写。
	//
	// 面板的「调谐间隔」是一个运行时设置项，改完必须真的生效。
	// 之前它是普通字段且 Start 里一次性建好 ticker，改设置只会
	// 改数据库——界面显示保存成功，实际要重启进程才生效。
	intervalMu sync.RWMutex
	interval   time.Duration

	trigger chan struct{}
	// retune 通知 Start 重建 ticker。缓冲 1 且写入非阻塞：
	// 连续保存多次只需重建一次。
	retune chan time.Duration

	mu        sync.Mutex
	lastOK    time.Time
	lastProbe time.Time
	lastNet   time.Time
	linkErr   error
	probeErr  error
	netErr    error
	rateFP    string
	rateErr   error
	// syncErr 是最近一次调谐的错误，syncFailSince 记录它开始持续出现的时间。
	//
	// 为什么必须记错误而不是只记成功时间：调谐失败意味着期望态没有落到内核，
	// 面板此时处于「配置看起来正常、实际隧道不通」的状态。只报 lastOK 非零的话，
	// 就绪探针会一直返回 ready——曾经实测到主密钥不匹配导致每 2 秒调谐失败，
	// 而 /readyz 始终 200，故障对编排系统完全不可见。
	syncErr       error
	syncFailSince time.Time
	lastAttempt   time.Time
}

// Option 是调谐器的可选配置。
type Option func(*Reconciler)

// WithNetworkMode 指定进程所处的网络形态。
// 默认 bridge；host 模式下不碰宿主的转发开关与防火墙规则。
func WithNetworkMode(m netdriver.Mode) Option {
	return func(r *Reconciler) { r.mode = m }
}

// New 构造调谐器。
func New(driver wgdriver.Driver, net netdriver.NetDriver, src DesiredStateProvider,
	interval time.Duration, log *slog.Logger, opts ...Option) *Reconciler {
	if interval <= 0 {
		interval = defaultInterval
	}
	if net == nil {
		net = netdriver.Platform()
	}
	r := &Reconciler{
		driver:   driver,
		net:      net,
		source:   src,
		interval: interval,
		mode:     netdriver.ModeBridge,
		log:      log,
		trigger:  make(chan struct{}, 1),
		retune:   make(chan time.Duration, 1),
	}
	for _, o := range opts {
		o(r)
	}
	return r
}

// SetInterval 在运行期改写调谐间隔。
//
// 非阻塞：通知信号满时直接丢弃，因为缓冲区里已经有一个待生效的新间隔，
// 再写一次不会让结果更接近期望，只会把 channel 排干。
func (r *Reconciler) SetInterval(d time.Duration) {
	if d <= 0 {
		return
	}
	r.intervalMu.Lock()
	changed := d != r.interval
	r.interval = d
	r.intervalMu.Unlock()
	if !changed {
		return
	}
	select {
	case r.retune <- d:
	default:
	}
}

// currentInterval 返回当前生效的调谐间隔。
func (r *Reconciler) currentInterval() time.Duration {
	r.intervalMu.RLock()
	defer r.intervalMu.RUnlock()
	return r.interval
}

// Start 运行调谐循环，直到 ctx 取消。
func (r *Reconciler) Start(ctx context.Context) {
	r.log.Info("reconciler started", "interval", r.currentInterval())
	if err := r.Reconcile(ctx); err != nil {
		r.log.Error("initial reconcile failed", "error", err)
	}

	interval := r.currentInterval()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			r.log.Info("reconciler stopped")
			return
		case <-ticker.C:
			if err := r.Reconcile(ctx); err != nil {
				r.log.Error("reconcile failed", "error", err)
			}
		case <-r.trigger:
			if err := r.Reconcile(ctx); err != nil {
				r.log.Error("triggered reconcile failed", "error", err)
			}
		case d := <-r.retune:
			// 重建 ticker 而不是 Reset：Reset 只是把倒计时归零，
			// 间隔本身没变，那样改设置等于没改。
			ticker.Reset(d)
			r.log.Info("reconcile interval updated", "interval", d)
		}
	}
}

// Trigger 请求尽快执行一次调谐。多次调用会合并为一次。
func (r *Reconciler) Trigger() {
	select {
	case r.trigger <- struct{}{}:
	default:
	}
}

// Reconcile 同步执行一次调谐，供同步路径（如写操作后）直接调用。
func (r *Reconciler) Reconcile(ctx context.Context) error {
	err := r.reconcile(ctx)
	r.recordAttempt(err)
	return err
}

// recordAttempt 记录一次调谐的结果，供就绪探针暴露真实状态。
func (r *Reconciler) recordAttempt(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lastAttempt = time.Now()
	if err == nil {
		r.syncErr, r.syncFailSince = nil, time.Time{}
		return
	}
	if r.syncErr == nil {
		r.syncFailSince = r.lastAttempt
	}
	r.syncErr = err
}

func (r *Reconciler) reconcile(ctx context.Context) error {
	if err := r.available(); err != nil {
		return err
	}
	cfg, err := r.source.ServerConfig(ctx)
	if err != nil {
		return err
	}
	if cfg == nil {
		return nil
	}
	peers, err := r.source.ActivePeers(ctx)
	if err != nil {
		return err
	}

	state, err := r.driver.Device(ctx, cfg.Interface)
	if err != nil {
		return err
	}
	if state == nil {
		r.log.Info("interface missing, recreating", "interface", cfg.Interface)
		if err := r.driver.Up(ctx, cfg, peers); err != nil {
			return err
		}
		r.applyNetwork(ctx, cfg, peers)
		r.markSuccess()
		return nil
	}

	// 设备级属性（端口/fwmark）漂移必须整表下发；仅 Peer 漂移才走增量同步。
	if reason, drifted := deviceDiff(state, cfg); drifted {
		r.log.Info("device state drift detected, applying", "interface", cfg.Interface, "diff", reason)
		if err := r.driver.Up(ctx, cfg, peers); err != nil {
			return err
		}
		r.applyNetwork(ctx, cfg, peers)
		r.markSuccess()
		return nil
	}

	if reason := peerDiff(state, peers); reason != "" {
		r.log.Info("peer state drift detected, syncing", "interface", cfg.Interface, "diff", reason)
		if err := r.driver.SyncPeers(ctx, cfg.Interface, peers); err != nil {
			return err
		}
	}

	r.applyNetwork(ctx, cfg, peers)
	r.markSuccess()
	return nil
}

// applyNetwork 幂等下发链路修复、转发、路由、NAT 与限速；失败只记录，不阻断 Peer 同步。
func (r *Reconciler) applyNetwork(ctx context.Context, cfg *domain.ServerConfig, peers []domain.Peer) {
	r.applyRateLimits(ctx, cfg, peers)
	r.applyLink(ctx, cfg)

	r.mu.Lock()
	due := time.Since(r.lastNet) >= netResyncInterval
	if due {
		r.lastNet = time.Now()
	}
	r.mu.Unlock()
	if !due {
		return
	}

	err := r.ensureNetwork(ctx, cfg)
	r.mu.Lock()
	changed := (err == nil) != (r.netErr == nil)
	r.netErr = err
	r.mu.Unlock()

	switch {
	case err != nil && changed:
		r.log.Warn("network setup incomplete", "interface", cfg.Interface, "error", err)
	case err != nil:
		r.log.Debug("network setup still failing", "error", err)
	case changed:
		r.log.Info("network setup applied", "interface", cfg.Interface, "mode", r.mode, "table", cfg.Table)
	}
}

// ensureNetwork 按网络模式下发容器专属的网络层配置。
func (r *Reconciler) ensureNetwork(ctx context.Context, cfg *domain.ServerConfig) error {
	subnet, err := netdriver.SubnetOf(cfg.AddressCIDR)
	if err != nil {
		return err
	}
	if r.mode == netdriver.ModeHost {
		return r.ensureHostNetwork(ctx, cfg, subnet)
	}
	if err := r.net.EnsureForwarding(); err != nil {
		return err
	}
	if err := r.net.EnsureRouting(ctx, cfg.Interface, subnet, cfg.Table, cfg.FwMark); err != nil {
		return err
	}
	if err := r.net.EnsureNAT(ctx, cfg.Interface, subnet); err != nil {
		if errors.Is(err, netdriver.ErrNATUnavailable) {
			return fmt.Errorf("%w（宿主网络模式下可忽略）", err)
		}
		return err
	}
	return nil
}

// ensureHostNetwork 在 host 模式下只处理显式路由表。
//
// host 模式下有三件事不能做：
//   - 写 /proc/sys/net/ipv4/ip_forward：那是宿主的全局开关，面板无权替宿主决策；
//   - 下发 MASQUERADE / FORWARD 规则：改的是宿主防火墙，容器退出后规则还留着；
//   - 往主表塞接口网段路由：绑定地址时内核已经自动装了直连路由，多写一条只会覆盖它的协议标记。
//
// 唯一需要下发的是用户显式指定了路由表时（配合 fwmark 做策略路由），
// 那属于「按面板配置转发」的明确意图，不是面板替宿主做的默认选择。
func (r *Reconciler) ensureHostNetwork(ctx context.Context, cfg *domain.ServerConfig, subnet string) error {
	if !explicitTable(cfg.Table) {
		return nil
	}
	return r.net.EnsureRouting(ctx, cfg.Interface, subnet, cfg.Table, cfg.FwMark)
}

// explicitTable 判断是否配置了显式路由表（auto/off 都不算）。
func explicitTable(table string) bool {
	switch strings.ToLower(strings.TrimSpace(table)) {
	case "", "auto", "off":
		return false
	}
	return true
}

// available 带缓存地探测驱动可用性。
func (r *Reconciler) available() error {
	r.mu.Lock()
	if !r.lastProbe.IsZero() && time.Since(r.lastProbe) < probeInterval {
		err := r.probeErr
		r.mu.Unlock()
		return err
	}
	r.mu.Unlock()

	err := r.driver.Available()

	r.mu.Lock()
	r.lastProbe, r.probeErr = time.Now(), err
	r.mu.Unlock()
	return err
}

// LastSuccess 返回最近一次成功调谐时间。
func (r *Reconciler) LastSuccess() time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastOK
}

// syncFailureGrace 是连续失败多久之后才把调谐故障算作「持续故障」。
//
// 单次失败不该让实例被判死：调谐每 2 秒一轮，链路重建、NAT 检查、限速下发
// 都可能撞上一次瞬时错误。超过这个窗口还失败，才说明期望态真的落不下去。
var syncFailureGrace = 3 * defaultInterval

// SyncError 返回最近一次调谐的错误，以及它是否已持续超过容忍窗口。
//
// 就绪探针用第二个返回值决定是否降级：偶发失败照常 ready，
// 持续失败说明期望态没落到内核，实例不该继续被当作健康。
func (r *Reconciler) SyncError() (error, bool) {
	// 先在 mu 之外读 interval：intervalMu 与 mu 是两把独立的锁，
	// 持着 mu 再去拿 intervalMu 会给未来的改动埋一个死锁的顺序。
	grace := syncFailureGrace
	if d := r.currentInterval(); d > 0 {
		grace = 3 * d
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.syncErr == nil {
		return nil, false
	}
	return r.syncErr, time.Since(r.syncFailSince) >= grace
}

// LastAttempt 返回最近一次调谐尝试时间（含失败）。
func (r *Reconciler) LastAttempt() time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastAttempt
}

// LastNetworkError 返回最近一次网络层下发结果，便于就绪探针暴露降级状态。
// 链路漂移优先上报：接口不存在或地址缺失是更致命的问题，
// 转发/NAT 失败往往只是它的下游表现。
func (r *Reconciler) LastNetworkError() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.linkErr != nil {
		return r.linkErr
	}
	return r.netErr
}

// deviceDiff 比对设备级属性。返回 (原因, 是否漂移)。
func deviceDiff(state *wgdriver.DeviceState, cfg *domain.ServerConfig) (string, bool) {
	if state.ListenPort != cfg.ListenPort {
		return fmt.Sprintf("listenPort %d→%d", state.ListenPort, cfg.ListenPort), true
	}
	if state.FirewallMark != cfg.FwMark {
		return fmt.Sprintf("fwmark %d→%d", state.FirewallMark, cfg.FwMark), true
	}
	if cfg.PublicKey != "" && state.PublicKey.String() != cfg.PublicKey {
		return "serverPublicKey", true
	}
	return "", false
}

// peerDiff 比对 Peer 集合及其关键属性，返回首个差异描述。
func peerDiff(state *wgdriver.DeviceState, peers []domain.Peer) string {
	existing := make(map[string]wgdriver.PeerState, len(state.Peers))
	for _, sp := range state.Peers {
		existing[sp.PublicKey.String()] = sp
	}
	seen := make(map[string]struct{}, len(peers))

	for i := range peers {
		p := peers[i]
		key, err := wgtypes.ParseKey(p.PublicKey)
		if err != nil {
			return "invalidPublicKey:" + p.Name
		}
		seen[key.String()] = struct{}{}
		sp, ok := existing[key.String()]
		if !ok {
			return "missingPeer:" + p.Name
		}
		if d := peerFieldDiff(sp, p); d != "" {
			return p.Name + ":" + d
		}
	}
	for _, sp := range state.Peers {
		if _, ok := seen[sp.PublicKey.String()]; !ok {
			return "stalePeer:" + shortKey(sp.PublicKey.String())
		}
	}
	return ""
}

// peerFieldDiff 比对单个 Peer 的地址、端点与保活间隔。
//
// 注意：未显式配置 Endpoint 时内核侧会记录握手学到的端点（漫游后地址会变），
// 这属于正常状态，不能判定为漂移，否则会周期性整表下发打断漫游。
func peerFieldDiff(actual wgdriver.PeerState, want domain.Peer) string {
	// 基准必须与 wgdriver.toPeerConfig 一致：服务端 [Peer] 的 AllowedIPs 取自
	// Address，而不是客户端侧的 AllowedIPs。两者混用会导致每轮调谐都判定漂移，
	// 而整表下发会清空握手与流量计数。
	wantIPs := normalizeCIDRList(want.Address)
	gotIPs := make([]string, 0, len(actual.AllowedIPs))
	for _, n := range actual.AllowedIPs {
		gotIPs = append(gotIPs, n.String())
	}
	if !equalStringSets(wantIPs, gotIPs) {
		return "allowedIPs"
	}
	if want.Endpoint != "" {
		gotEndpoint := ""
		if actual.Endpoint != nil {
			gotEndpoint = actual.Endpoint.String()
		}
		if resolveEndpoint(want.Endpoint) != gotEndpoint {
			return "endpoint"
		}
	}
	if actual.PersistentKeepaliveInterval.Seconds() != float64(want.PersistentKeepalive) {
		return "persistentKeepalive"
	}
	return ""
}

// resolveEndpoint 把 host:port 解析为内核回显的 ip:port 形式。
func resolveEndpoint(endpoint string) string {
	addr, err := net.ResolveUDPAddr("udp", endpoint)
	if err != nil {
		return endpoint
	}
	return addr.String()
}

func shortKey(k string) string {
	if len(k) <= 8 {
		return k
	}
	return k[:8]
}

func normalizeCIDRList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func equalStringSets(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	set := make(map[string]int, len(a))
	for _, v := range a {
		set[v]++
	}
	for _, v := range b {
		set[v]--
		if set[v] < 0 {
			return false
		}
	}
	return true
}

func (r *Reconciler) markSuccess() {
	r.mu.Lock()
	r.lastOK = time.Now()
	r.mu.Unlock()
}
