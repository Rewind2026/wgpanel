package reconciler

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"sync"
	"testing"
	"time"

	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"

	"github.com/Rewind2026/wgpanel/internal/domain"
	"github.com/Rewind2026/wgpanel/internal/netdriver"
	"github.com/Rewind2026/wgpanel/internal/wgdriver"
)

const (
	keyA = "dpmLZIKxpezJZNlZwlv6pLg8goGW8C6jasftaRxv8yQ="
	keyB = "aGVsbG9Xb3JsZEhlbGxvV29ybGRIZWxsb1dvcmxkMTI="
)

// fakeDriver 记录调谐器对控制面的调用序列，用于断言漂移处理路径。
type fakeDriver struct {
	mu sync.Mutex
	// state 为 nil 表示接口不存在。
	state *wgdriver.DeviceState

	upCalls      int
	syncCalls    int
	downCalls    int
	lastSynced   []domain.Peer
	availableErr error
}

func (f *fakeDriver) Available() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.availableErr
}

func (f *fakeDriver) Device(context.Context, string) (*wgdriver.DeviceState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.state, nil
}

func (f *fakeDriver) Up(context.Context, *domain.ServerConfig, []domain.Peer) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.upCalls++
	return nil
}

func (f *fakeDriver) Down(context.Context, string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.downCalls++
	return nil
}

func (f *fakeDriver) SyncPeers(_ context.Context, _ string, peers []domain.Peer) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.syncCalls++
	f.lastSynced = peers
	return nil
}

func (f *fakeDriver) PeerStats(string) (map[string]*domain.PeerStats, error) {
	return nil, nil
}

func (f *fakeDriver) TotalCounters(string) (int64, int64, error) { return 0, 0, nil }

// fakeNet 记录网络层调用，允许注入 NAT 失败与链路漂移。
type fakeNet struct {
	mu         sync.Mutex
	fwdCalls   int
	natCalls   int
	routeArg   string
	routeTable string
	routeCalls int
	natErr     error
	rateCalls  int
	rateLimits []netdriver.RateLimit
	rateErr    error
	// rateApplied 模拟内核里的限速 qdisc 是否还在。
	// 下发成功后置 true；模拟外部 `tc qdisc del` 时置回 false。
	rateApplied bool

	// linkInfo 描述链路实际状态，可注入漂移。
	linkInfo     netdriver.LinkInfo
	linkInfoErr  error
	linkReads    int
	linkCreates  int
	linkUps      int
	linkMTUs     int
	linkAddrAdds int
}

// healthyNet 返回一个链路状态与默认配置一致的 fake。
func healthyNet() *fakeNet {
	cfg := defaultConfig()
	// rateApplied 默认置 true：模拟「内核里的限速树与期望集合一致」这一常态。
	// 调谐器只在指纹变化或这个标记翻转为 false 时才下发 tc。
	return &fakeNet{
		rateApplied: true,
		linkInfo: netdriver.LinkInfo{
			Name:      cfg.Interface,
			Exists:    true,
			Up:        true,
			MTU:       cfg.MTU,
			Addresses: []string{cfg.AddressCIDR},
		},
	}
}

func (f *fakeNet) Probe() netdriver.Capabilities {
	return netdriver.Capabilities{Netlink: true, Iptables: true, Forwarding: true}
}

func (f *fakeNet) EnsureLink(_ context.Context, _ string, mtu int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.linkCreates++
	f.linkInfo.Exists, f.linkInfo.Up, f.linkInfo.MTU = true, true, mtu
	return nil
}

func (f *fakeNet) DeleteLink(context.Context, string) error { return nil }

func (f *fakeNet) SetLinkUp(context.Context, string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.linkUps++
	f.linkInfo.Up = true
	return nil
}

func (f *fakeNet) EnsureAddress(_ context.Context, _ string, cidrList string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.linkAddrAdds++
	for _, c := range splitList(cidrList) {
		have := false
		for _, a := range f.linkInfo.Addresses {
			if a == c {
				have = true
				break
			}
		}
		if !have {
			f.linkInfo.Addresses = append(f.linkInfo.Addresses, c)
		}
	}
	return nil
}

func (f *fakeNet) SetMTU(_ context.Context, _ string, mtu int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.linkMTUs++
	f.linkInfo.MTU = mtu
	return nil
}

func (f *fakeNet) LinkInfo(string) (netdriver.LinkInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.linkReads++
	if f.linkInfoErr != nil {
		return netdriver.LinkInfo{}, f.linkInfoErr
	}
	info := f.linkInfo
	info.Addresses = append([]string(nil), f.linkInfo.Addresses...)
	return info, nil
}

func (f *fakeNet) EnsureForwarding() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fwdCalls++
	return nil
}

func (f *fakeNet) EnsureRouting(_ context.Context, _, subnet, table string, _ int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.routeCalls++
	f.routeArg, f.routeTable = subnet, table
	return nil
}

func (f *fakeNet) EnsureNAT(context.Context, string, string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.natCalls++
	return f.natErr
}

func (f *fakeNet) RemoveNAT(context.Context, string, string) error { return nil }

func (f *fakeNet) ApplyRateLimits(_ context.Context, _ string, limits []netdriver.RateLimit) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rateCalls++
	f.rateLimits = limits
	if f.rateErr == nil {
		f.rateApplied = true
	}
	return f.rateErr
}

func (f *fakeNet) CleanRateLimits(context.Context, string) error { return nil }

// rateApplied 控制 RateLimitApplied 的返回，用来模拟「内核里的 tc 状态还在/丢了」。
func (f *fakeNet) RateLimitApplied(string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.rateApplied
}

// counts 返回各调谐动作的调用次数快照。
func (f *fakeNet) counts() (creates, ups, mtus, addrs int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.linkCreates, f.linkUps, f.linkMTUs, f.linkAddrAdds
}

type fakeSource struct {
	cfg   *domain.ServerConfig
	peers []domain.Peer
}

func (s fakeSource) ServerConfig(context.Context) (*domain.ServerConfig, error) {
	return s.cfg, nil
}

func (s fakeSource) ActivePeers(context.Context) ([]domain.Peer, error) {
	return s.peers, nil
}

// errSource 让期望态读取失败，模拟「主密钥不匹配 → 解不开服务端私钥」这类故障。
type errSource struct{ err error }

func (s errSource) ServerConfig(context.Context) (*domain.ServerConfig, error) { return nil, s.err }
func (s errSource) ActivePeers(context.Context) ([]domain.Peer, error)         { return nil, s.err }

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func mustKey(t *testing.T, b64 string) wgtypes.Key {
	t.Helper()
	k, err := wgtypes.ParseKey(b64)
	if err != nil {
		t.Fatalf("parse key %q: %v", b64, err)
	}
	return k
}

func defaultConfig() *domain.ServerConfig {
	return &domain.ServerConfig{
		Interface:    "wg0",
		ListenPort:   51820,
		AddressCIDR:  "10.8.0.1/24",
		MTU:          1420,
		FwMark:       51820,
		Table:        "auto",
		PublicKey:    keyA,
		PresharedKey: "",
	}
}

func defaultPeer() domain.Peer {
	return domain.Peer{
		ID:                  1,
		Name:                "laptop",
		PublicKey:           keyB,
		Address:             "10.8.0.2/32",
		AllowedIPs:          "10.8.0.2/32",
		PersistentKeepalive: 25,
		Enabled:             true,
	}
}

// deviceState 构造与期望配置一致的内核态快照。
func deviceState(t *testing.T, cfg *domain.ServerConfig, peers []domain.Peer) *wgdriver.DeviceState {
	t.Helper()
	st := &wgdriver.DeviceState{
		Name:         cfg.Interface,
		ListenPort:   cfg.ListenPort,
		PublicKey:    mustKey(t, cfg.PublicKey),
		FirewallMark: cfg.FwMark,
	}
	for _, p := range peers {
		var ips []net.IPNet
		// 与 wgdriver.toPeerConfig 保持一致：服务端 [Peer] 的 AllowedIPs 来自
		// Address，不是客户端侧的 AllowedIPs。
		for _, c := range normalizeCIDRList(p.Address) {
			_, ipnet, err := net.ParseCIDR(c)
			if err != nil {
				t.Fatalf("parse allowed ip %q: %v", c, err)
			}
			ips = append(ips, *ipnet)
		}
		st.Peers = append(st.Peers, wgdriver.PeerState{
			PublicKey:                   mustKey(t, p.PublicKey),
			AllowedIPs:                  ips,
			PersistentKeepaliveInterval: time.Duration(p.PersistentKeepalive) * time.Second,
		})
	}
	return st
}

func newTestReconciler(d wgdriver.Driver, n netdriver.NetDriver, src DesiredStateProvider) *Reconciler {
	return New(d, n, src, time.Hour, testLogger())
}

func TestReconcileNoDriftIsNoOp(t *testing.T) {
	cfg := defaultConfig()
	peers := []domain.Peer{defaultPeer()}
	d := &fakeDriver{state: deviceState(t, cfg, peers)}
	n := healthyNet()
	r := newTestReconciler(d, n, fakeSource{cfg: cfg, peers: peers})

	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if d.upCalls != 0 || d.syncCalls != 0 {
		t.Errorf("无漂移时不应下发配置，up=%d sync=%d", d.upCalls, d.syncCalls)
	}
	// 首轮调谐会应用一次网络层规则。
	if n.fwdCalls != 1 || n.natCalls != 1 {
		t.Errorf("首轮应下发一次网络规则，forwarding=%d nat=%d", n.fwdCalls, n.natCalls)
	}
	if n.routeArg != "10.8.0.0/24" {
		t.Errorf("路由应使用掩码后的网段，得到 %q", n.routeArg)
	}
}

func TestReconcileCreatesMissingInterface(t *testing.T) {
	cfg := defaultConfig()
	peers := []domain.Peer{defaultPeer()}
	d := &fakeDriver{state: nil}
	r := newTestReconciler(d, healthyNet(), fakeSource{cfg: cfg, peers: peers})

	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if d.upCalls != 1 {
		t.Errorf("接口缺失时应调用一次 Up，实际 %d", d.upCalls)
	}
}

func TestReconcileDeviceDriftTriggersUp(t *testing.T) {
	cfg := defaultConfig()
	peers := []domain.Peer{defaultPeer()}

	for _, tc := range []struct {
		name  string
		mutat func(*domain.ServerConfig, *wgdriver.DeviceState)
	}{
		{"listenPort", func(_ *domain.ServerConfig, s *wgdriver.DeviceState) { s.ListenPort = 51821 }},
		{"fwmark", func(_ *domain.ServerConfig, s *wgdriver.DeviceState) { s.FirewallMark = 1 }},
		{"publicKey", func(_ *domain.ServerConfig, s *wgdriver.DeviceState) {
			s.PublicKey = mustKey(t, "aGVsbG9Xb3JsZEhlbGxvV29ybGRIZWxsb1dvcmxkMTI=")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := deviceState(t, cfg, peers)
			tc.mutat(cfg, st)
			d := &fakeDriver{state: st}
			r := newTestReconciler(d, healthyNet(), fakeSource{cfg: cfg, peers: peers})

			if err := r.Reconcile(context.Background()); err != nil {
				t.Fatalf("Reconcile: %v", err)
			}
			if d.upCalls != 1 {
				t.Errorf("设备级漂移应触发整表下发 Up，实际 up=%d", d.upCalls)
			}
		})
	}
}

func TestReconcilePeerDriftTriggersSyncPeersOnly(t *testing.T) {
	cfg := defaultConfig()
	peers := []domain.Peer{defaultPeer()}

	t.Run("missingPeer", func(t *testing.T) {
		st := deviceState(t, cfg, nil) // 内核里没有该 Peer
		d := &fakeDriver{state: st}
		r := newTestReconciler(d, healthyNet(), fakeSource{cfg: cfg, peers: peers})

		if err := r.Reconcile(context.Background()); err != nil {
			t.Fatalf("Reconcile: %v", err)
		}
		if d.syncCalls != 1 {
			t.Errorf("Peer 漂移应触发 SyncPeers，实际 %d", d.syncCalls)
		}
		if d.upCalls != 0 {
			t.Errorf("仅 Peer 漂移时不应整表下发，实际 up=%d", d.upCalls)
		}
	})

	t.Run("stalePeer", func(t *testing.T) {
		st := deviceState(t, cfg, peers)
		st.Peers = append(st.Peers, wgdriver.PeerState{PublicKey: mustKey(t, keyA)})
		d := &fakeDriver{state: st}
		r := newTestReconciler(d, healthyNet(), fakeSource{cfg: cfg, peers: peers})

		if err := r.Reconcile(context.Background()); err != nil {
			t.Fatalf("Reconcile: %v", err)
		}
		if d.syncCalls != 1 {
			t.Errorf("残留 Peer 应触发 SyncPeers，实际 %d", d.syncCalls)
		}
	})

	t.Run("address", func(t *testing.T) {
		changed := defaultPeer()
		changed.Address = "10.8.0.9/32"
		st := deviceState(t, cfg, []domain.Peer{defaultPeer()})
		d := &fakeDriver{state: st}
		r := newTestReconciler(d, healthyNet(), fakeSource{cfg: cfg, peers: []domain.Peer{changed}})

		if err := r.Reconcile(context.Background()); err != nil {
			t.Fatalf("Reconcile: %v", err)
		}
		if d.syncCalls != 1 {
			t.Errorf("Peer 地址变化应触发 SyncPeers，实际 %d", d.syncCalls)
		}
	})

	// 客户端侧 AllowedIPs 只写进客户端配置，不参与服务端下发。
	// 若把它当成比对基准，内核永远无法满足（服务端要求唯一），
	// 调谐会每 2 秒整表重放一次并清空握手与计数。
	t.Run("clientAllowedIPsChangeDoesNotTriggerSync", func(t *testing.T) {
		changed := defaultPeer()
		changed.AllowedIPs = "0.0.0.0/0, ::/0"
		st := deviceState(t, cfg, []domain.Peer{defaultPeer()})
		d := &fakeDriver{state: st}
		r := newTestReconciler(d, healthyNet(), fakeSource{cfg: cfg, peers: []domain.Peer{changed}})

		if err := r.Reconcile(context.Background()); err != nil {
			t.Fatalf("Reconcile: %v", err)
		}
		if d.syncCalls != 0 {
			t.Errorf("客户端侧 AllowedIPs 变化不应触发服务端下发，实际 %d", d.syncCalls)
		}
	})

	t.Run("persistentKeepalive", func(t *testing.T) {
		st := deviceState(t, cfg, peers)
		st.Peers[0].PersistentKeepaliveInterval = 15 * time.Second
		d := &fakeDriver{state: st}
		r := newTestReconciler(d, healthyNet(), fakeSource{cfg: cfg, peers: peers})

		if err := r.Reconcile(context.Background()); err != nil {
			t.Fatalf("Reconcile: %v", err)
		}
		if d.syncCalls != 1 {
			t.Errorf("保活间隔变化应触发 SyncPeers，实际 %d", d.syncCalls)
		}
	})
}

func TestPeerFieldDiffIgnoresLearnedEndpoint(t *testing.T) {
	// 未显式配置 Endpoint 时，内核记录的是握手学到的地址，漫游后会变，不能算漂移。
	p := defaultPeer()
	p.Endpoint = ""
	_, ipnet, err := net.ParseCIDR("10.8.0.2/32")
	if err != nil {
		t.Fatal(err)
	}
	actual := wgdriver.PeerState{
		PublicKey:                   mustKey(t, keyB),
		AllowedIPs:                  []net.IPNet{*ipnet},
		Endpoint:                    &net.UDPAddr{IP: net.ParseIP("203.0.113.7"), Port: 51820},
		PersistentKeepaliveInterval: 25 * time.Second,
	}
	if got := peerFieldDiff(actual, p); got != "" {
		t.Errorf("漫游导致的内核端点变化不应判为漂移，得到 %q", got)
	}
}

func TestPeerFieldDiffDetectsConfiguredEndpointChange(t *testing.T) {
	p := defaultPeer()
	p.Endpoint = "198.51.100.9:51820"
	_, ipnet, err := net.ParseCIDR("10.8.0.2/32")
	if err != nil {
		t.Fatal(err)
	}
	actual := wgdriver.PeerState{
		PublicKey:                   mustKey(t, keyB),
		AllowedIPs:                  []net.IPNet{*ipnet},
		Endpoint:                    &net.UDPAddr{IP: net.ParseIP("203.0.113.7"), Port: 51820},
		PersistentKeepaliveInterval: 25 * time.Second,
	}
	if got := peerFieldDiff(actual, p); got != "endpoint" {
		t.Errorf("显式端点被改动应判为 endpoint 漂移，得到 %q", got)
	}
}

func TestNetworkFailureIsReportedButNotBlocking(t *testing.T) {
	cfg := defaultConfig()
	peers := []domain.Peer{defaultPeer()}
	d := &fakeDriver{state: deviceState(t, cfg, peers)}
	n := healthyNet()
	n.natErr = netdriver.ErrNATUnavailable
	r := newTestReconciler(d, n, fakeSource{cfg: cfg, peers: peers})

	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("网络下发失败不应中断调谐返回错误，得到 %v", err)
	}
	err := r.LastNetworkError()
	if err == nil {
		t.Fatal("应记录最近一次网络下发错误")
	}
	if !errors.Is(err, netdriver.ErrNATUnavailable) {
		t.Errorf("应保留 ErrNATUnavailable 以便上层按模式判断，得到 %v", err)
	}
}

func TestNetworkResyncIsThrottled(t *testing.T) {
	cfg := defaultConfig()
	d := &fakeDriver{state: deviceState(t, cfg, nil)}
	n := healthyNet()
	r := newTestReconciler(d, n, fakeSource{cfg: cfg})

	ctx := context.Background()
	for range 3 {
		if err := r.Reconcile(ctx); err != nil {
			t.Fatalf("Reconcile: %v", err)
		}
	}
	if n.natCalls != 1 {
		t.Errorf("网络层应按周期兜底而非每轮重放，实际调用 %d 次", n.natCalls)
	}
	// 链路修复不走节流：外部把接口拉 down 必须在下一轮就被发现。
	if n.linkReads != 3 {
		t.Errorf("链路状态应每轮检查，实际检查 %d 次", n.linkReads)
	}
}

func TestUnavailableDriverShortCircuits(t *testing.T) {
	cfg := defaultConfig()
	d := &fakeDriver{state: nil, availableErr: errors.New("no permission")}
	r := newTestReconciler(d, healthyNet(), fakeSource{cfg: cfg})

	if err := r.Reconcile(context.Background()); err == nil {
		t.Fatal("驱动不可用时应返回错误")
	}
	if d.upCalls != 0 {
		t.Errorf("驱动不可用时不应下发配置，实际 up=%d", d.upCalls)
	}
}

func TestLastSuccessRecorded(t *testing.T) {
	cfg := defaultConfig()
	d := &fakeDriver{state: deviceState(t, cfg, nil)}
	r := newTestReconciler(d, healthyNet(), fakeSource{cfg: cfg})

	if !r.LastSuccess().IsZero() {
		t.Fatal("调谐前 LastSuccess 应为零值")
	}
	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if r.LastSuccess().IsZero() {
		t.Error("调谐成功后应记录时间")
	}
}

// TestSyncErrorExposesSustainedFailure 锁定就绪探针的真实判据。
//
// 背景：真机上换错主密钥后，期望态解不开，每 2 秒调谐失败一次，
// 隧道实际完全不通，但 /readyz 因为只看「跑过没有」而一直返回 ready。
// 这个测试确保 SyncError 能把持续失败暴露出来，供探针判 503。
func TestSyncErrorExposesSustainedFailure(t *testing.T) {
	boom := errors.New("service: decrypt server private key: invalid ciphertext")
	r := New(&fakeDriver{}, healthyNet(), errSource{err: boom}, 2*time.Millisecond, testLogger())

	if err, sustained := r.SyncError(); err != nil || sustained {
		t.Fatalf("调谐前不应有错误，err=%v sustained=%v", err, sustained)
	}

	// 第一轮失败：刚失败不算持续故障，避免瞬时错误把实例判死。
	if err := r.Reconcile(context.Background()); err == nil {
		t.Fatal("期望态读取失败时 Reconcile 应返回错误")
	}
	err, sustained := r.SyncError()
	if err == nil || err.Error() != boom.Error() {
		t.Fatalf("SyncError 应返回原始错误，得到 %v", err)
	}
	if sustained {
		t.Error("刚失败一次不应判定为持续故障")
	}

	// 持续超过容忍窗口：必须升级为持续故障。
	time.Sleep(20 * time.Millisecond)
	if _, sustained := r.SyncError(); !sustained {
		t.Error("持续失败超过 3 个周期后应判定为持续故障")
	}
	if r.LastAttempt().IsZero() {
		t.Error("LastAttempt 应记录失败轮次的时间")
	}
	if !r.LastSuccess().IsZero() {
		t.Error("从未成功过则 LastSuccess 应保持零值")
	}

	// 故障排除后必须自愈，否则一次抖动会永久拉黑实例。
	d := &fakeDriver{}
	r2 := New(d, healthyNet(), fakeSource{cfg: defaultConfig()}, 2*time.Millisecond, testLogger())
	if err := r2.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if err, sustained := r2.SyncError(); err != nil || sustained {
		t.Errorf("成功后 SyncError 应清空，err=%v sustained=%v", err, sustained)
	}
}

// TestSyncErrorTracksRecoveredThenFailedAgain 确认连续失败窗口在成功后重置，
// 不会把「很久以前失败过一次」一直算进当前的持续故障判定里。
func TestSyncErrorTracksRecoveredThenFailedAgain(t *testing.T) {
	boom := errors.New("boom")
	r := New(&fakeDriver{}, healthyNet(), fakeSource{cfg: defaultConfig()},
		time.Millisecond, testLogger())
	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	r.source = errSource{err: boom}
	if err := r.Reconcile(context.Background()); err == nil {
		t.Fatal("期望读取失败时应返回错误")
	}
	time.Sleep(20 * time.Millisecond)
	if _, sustained := r.SyncError(); !sustained {
		t.Error("再次持续失败应重新判定为持续故障")
	}
}

// TestLinkDriftIsRepaired 覆盖「控制面正常、链路被外部改坏」这一类漂移。
// wgctrl 看不到这些属性，不单独修就会静默失联。
func TestLinkDriftIsRepaired(t *testing.T) {
	cfg := defaultConfig()
	peers := []domain.Peer{defaultPeer()}

	t.Run("linkDown", func(t *testing.T) {
		n := healthyNet()
		n.linkInfo.Up = false
		d := &fakeDriver{state: deviceState(t, cfg, peers)}
		r := newTestReconciler(d, n, fakeSource{cfg: cfg, peers: peers})

		if err := r.Reconcile(context.Background()); err != nil {
			t.Fatalf("Reconcile: %v", err)
		}
		if _, ups, _, _ := n.counts(); ups != 1 {
			t.Errorf("接口被拉 down 应被拉起，实际 SetLinkUp %d 次", ups)
		}
	})

	t.Run("mtuChanged", func(t *testing.T) {
		n := healthyNet()
		n.linkInfo.MTU = 1500
		d := &fakeDriver{state: deviceState(t, cfg, peers)}
		r := newTestReconciler(d, n, fakeSource{cfg: cfg, peers: peers})

		if err := r.Reconcile(context.Background()); err != nil {
			t.Fatalf("Reconcile: %v", err)
		}
		_, _, mtus, _ := n.counts()
		if mtus != 1 || n.linkInfo.MTU != cfg.MTU {
			t.Errorf("MTU 应被纠正为 %d，实际 SetMTU %d 次且当前 %d", cfg.MTU, mtus, n.linkInfo.MTU)
		}
	})

	t.Run("addressRemoved", func(t *testing.T) {
		n := healthyNet()
		n.linkInfo.Addresses = nil
		d := &fakeDriver{state: deviceState(t, cfg, peers)}
		r := newTestReconciler(d, n, fakeSource{cfg: cfg, peers: peers})

		if err := r.Reconcile(context.Background()); err != nil {
			t.Fatalf("Reconcile: %v", err)
		}
		if _, _, _, addrs := n.counts(); addrs != 1 {
			t.Errorf("地址被删应重新绑定，实际 EnsureAddress %d 次", addrs)
		}
		if len(n.linkInfo.Addresses) != 1 || n.linkInfo.Addresses[0] != cfg.AddressCIDR {
			t.Errorf("地址应恢复为 %q，得到 %#v", cfg.AddressCIDR, n.linkInfo.Addresses)
		}
	})

	t.Run("linkDeleted", func(t *testing.T) {
		n := healthyNet()
		n.linkInfo.Exists = false
		d := &fakeDriver{state: deviceState(t, cfg, peers)}
		r := newTestReconciler(d, n, fakeSource{cfg: cfg, peers: peers})

		if err := r.Reconcile(context.Background()); err != nil {
			t.Fatalf("Reconcile: %v", err)
		}
		creates, ups, _, addrs := n.counts()
		if creates != 1 || ups != 1 || addrs != 1 {
			t.Errorf("接口被删应重建（含地址与拉起），create=%d up=%d addr=%d", creates, ups, addrs)
		}
	})
}

func TestHealthyLinkIsNotTouched(t *testing.T) {
	cfg := defaultConfig()
	n := healthyNet()
	d := &fakeDriver{state: deviceState(t, cfg, nil)}
	r := newTestReconciler(d, n, fakeSource{cfg: cfg})

	for range 3 {
		if err := r.Reconcile(context.Background()); err != nil {
			t.Fatalf("Reconcile: %v", err)
		}
	}
	if creates, ups, mtus, addrs := n.counts(); creates|ups|mtus|addrs != 0 {
		t.Errorf("链路健康时不应有写操作，create=%d up=%d mtu=%d addr=%d", creates, ups, mtus, addrs)
	}
}

func TestLinkRepairFailureSurfacesInNetworkError(t *testing.T) {
	cfg := defaultConfig()
	n := healthyNet()
	n.linkInfo.Up = false
	n.linkInfoErr = errors.New("netlink: operation not permitted")
	d := &fakeDriver{state: deviceState(t, cfg, nil)}
	r := newTestReconciler(d, n, fakeSource{cfg: cfg})

	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("链路修复失败不应中断调谐，得到 %v", err)
	}
	if r.LastNetworkError() == nil {
		t.Error("链路修复失败应被就绪探针看到")
	}
}

func TestMissingAddressesIgnoresExtras(t *testing.T) {
	if got := missingAddresses("10.8.0.1/24", []string{"10.8.0.1/24", "10.9.0.1/24"}); len(got) != 0 {
		t.Errorf("额外地址不算漂移，得到 %#v", got)
	}
	got := missingAddresses("10.8.0.1/24,10.9.0.1/24", []string{"10.8.0.1/24"})
	if len(got) != 1 || got[0] != "10.9.0.1/24" {
		t.Errorf("缺失的地址应被列出，得到 %#v", got)
	}
	if got := missingAddresses("", nil); len(got) != 0 {
		t.Errorf("未配置地址时不应报缺失，得到 %#v", got)
	}
}

// TestBridgeModeOwnsHostNetworking 是 bridge 模式的默认行为：
// 容器有独立 netns，转发、路由、NAT 都必须由面板下发。
func TestBridgeModeOwnsHostNetworking(t *testing.T) {
	cfg := defaultConfig()
	n := healthyNet()
	d := &fakeDriver{state: deviceState(t, cfg, nil)}
	r := New(d, n, fakeSource{cfg: cfg}, time.Hour, testLogger(),
		WithNetworkMode(netdriver.ModeBridge))

	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if n.fwdCalls != 1 || n.routeCalls != 1 || n.natCalls != 1 {
		t.Errorf("bridge 模式应下发转发/路由/NAT，得到 fwd=%d route=%d nat=%d",
			n.fwdCalls, n.routeCalls, n.natCalls)
	}
	if r.LastNetworkError() != nil {
		t.Errorf("bridge 模式正常时不应有网络错误，得到 %v", r.LastNetworkError())
	}
}

// TestHostModeLeavesHostAlone 锁定 host 模式的红线：
// 不改宿主转发开关、不动宿主防火墙、不往主表塞路由。
func TestHostModeLeavesHostAlone(t *testing.T) {
	cfg := defaultConfig()
	cfg.FwMark = 0 // host + auto 表在 netlink 层会被判为非法配置，这里只验证分流
	n := healthyNet()
	d := &fakeDriver{state: deviceState(t, cfg, nil)}
	r := New(d, n, fakeSource{cfg: cfg}, time.Hour, testLogger(),
		WithNetworkMode(netdriver.ModeHost))

	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if n.fwdCalls != 0 {
		t.Errorf("host 模式不应写宿主 ip_forward，实际 %d 次", n.fwdCalls)
	}
	if n.natCalls != 0 {
		t.Errorf("host 模式不应改宿主防火墙，实际 %d 次", n.natCalls)
	}
	if n.routeCalls != 0 {
		t.Errorf("host 模式且未显式配置路由表时不应下发路由，实际 %d 次", n.routeCalls)
	}
	if err := r.LastNetworkError(); err != nil {
		t.Errorf("host 模式跳过网络下发不应被记为错误，得到 %v", err)
	}
}

// TestHostModeStillHonorsExplicitTable：显式路由表是用户的明确意图，host 模式也要照做。
func TestHostModeStillHonorsExplicitTable(t *testing.T) {
	cfg := defaultConfig()
	cfg.Table = "51820"
	n := healthyNet()
	d := &fakeDriver{state: deviceState(t, cfg, nil)}
	r := New(d, n, fakeSource{cfg: cfg}, time.Hour, testLogger(),
		WithNetworkMode(netdriver.ModeHost))

	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if n.routeCalls != 1 || n.routeTable != "51820" {
		t.Errorf("host 模式应下发显式路由表，得到 route=%d table=%q", n.routeCalls, n.routeTable)
	}
	if n.natCalls != 0 || n.fwdCalls != 0 {
		t.Errorf("host 模式仍不应碰转发与防火墙，得到 fwd=%d nat=%d", n.fwdCalls, n.natCalls)
	}
}

func TestExplicitTable(t *testing.T) {
	for in, want := range map[string]bool{
		"": false, "auto": false, "AUTO": false, " off ": false,
		"51820": true, "wgpanel": true, "100": true,
	} {
		if got := explicitTable(in); got != want {
			t.Errorf("explicitTable(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestRateLimitsOfSkipsDisabledAndUnlimited(t *testing.T) {
	disabled := defaultPeer()
	disabled.ID, disabled.Enabled, disabled.RateLimitMbps = 2, false, 50
	unlimited := defaultPeer()
	unlimited.ID, unlimited.RateLimitMbps = 3, 0
	limited := defaultPeer()
	limited.ID, limited.RateLimitMbps = 4, 10

	got := rateLimitsOf([]domain.Peer{disabled, unlimited, limited})
	if len(got) != 1 {
		t.Fatalf("只应保留一个限速 Peer，得到 %#v", got)
	}
	if got[0].ID != 4 || got[0].Address != "10.8.0.2" || got[0].Mbps != 10 {
		t.Errorf("限速项内容不对：%#v", got[0])
	}
}

func TestRateLimitAppliedOnlyWhenFingerprintChanges(t *testing.T) {
	cfg := defaultConfig()
	peer := defaultPeer()
	peer.RateLimitMbps = 10
	peers := []domain.Peer{peer}

	n := healthyNet()
	d := &fakeDriver{state: deviceState(t, cfg, peers)}
	r := newTestReconciler(d, n, fakeSource{cfg: cfg, peers: peers})

	ctx := context.Background()
	for range 3 {
		if err := r.Reconcile(ctx); err != nil {
			t.Fatalf("Reconcile: %v", err)
		}
	}
	if n.rateCalls != 1 {
		t.Errorf("期望集合不变时不应重复执行 tc，实际 %d 次", n.rateCalls)
	}

	peers[0].RateLimitMbps = 20
	if err := r.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if n.rateCalls != 2 || n.rateLimits[0].Mbps != 20 {
		t.Errorf("限速变化应重新下发，实际调用 %d 次且值 %#v", n.rateCalls, n.rateLimits)
	}
}

// TestRateLimitReappliedAfterKernelWipe 锁定「内核侧状态消失后要能自愈」。
//
// 真机上暴露的缺陷：tc 状态只存在于内核内存，一次 `tc qdisc del`、
// 接口重建或容器迁移就会让它凭空消失，而数据库里的期望集合纹丝不动。
// 期望集合指纹因此始终判定「无需下发」，限速静默失效，
// 且只有等用户碰巧改了带宽才会被重新下发。
func TestRateLimitReappliedAfterKernelWipe(t *testing.T) {
	cfg := defaultConfig()
	peer := defaultPeer()
	peer.RateLimitMbps = 10
	peers := []domain.Peer{peer}

	n := healthyNet()
	d := &fakeDriver{state: deviceState(t, cfg, peers)}
	r := newTestReconciler(d, n, fakeSource{cfg: cfg, peers: peers})

	ctx := context.Background()
	for range 3 {
		if err := r.Reconcile(ctx); err != nil {
			t.Fatalf("Reconcile: %v", err)
		}
	}
	if n.rateCalls != 1 {
		t.Fatalf("初次下发后不应重复执行 tc，实际 %d 次", n.rateCalls)
	}

	// 模拟外部把限速树删掉：期望集合没变，但内核状态没了。
	n.mu.Lock()
	n.rateApplied = false
	n.mu.Unlock()

	if err := r.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if n.rateCalls != 2 {
		t.Errorf("内核限速状态消失后应自动重新下发，实际调用 %d 次", n.rateCalls)
	}
	if !n.rateApplied {
		t.Error("重新下发后内核状态应恢复为已应用")
	}

	// 自愈之后回到稳态，不应继续每 2 秒重放。
	for range 3 {
		if err := r.Reconcile(ctx); err != nil {
			t.Fatalf("Reconcile: %v", err)
		}
	}
	if n.rateCalls != 2 {
		t.Errorf("自愈后应回到稳态，实际调用 %d 次", n.rateCalls)
	}
}

// 没有任何 Peer 限速时必须一次下发后就静默。
//
// 真机上暴露的缺陷：期望集合为空时仍然每 2 秒执行一次 tc 清理。
// 存在性检查在这种场景下必然返回 false（内核里本来就不该有 class），
// 于是每一轮都被判成「需要重新下发」，日志被刷满，CPU 白白消耗，
// 真正出问题时反而找不到有效信息。
func TestNoRateLimitSettlesWithoutRepeatedReplay(t *testing.T) {
	cfg := defaultConfig()
	peers := []domain.Peer{defaultPeer()} // RateLimitMbps 为 0

	n := healthyNet()
	n.mu.Lock()
	n.rateApplied = false // 期望为空时内核里本来就不该有 class
	n.mu.Unlock()

	d := &fakeDriver{state: deviceState(t, cfg, peers)}
	r := newTestReconciler(d, n, fakeSource{cfg: cfg, peers: peers})

	ctx := context.Background()
	for range 5 {
		if err := r.Reconcile(ctx); err != nil {
			t.Fatalf("Reconcile: %v", err)
		}
	}
	if n.rateCalls != 1 {
		t.Errorf("无人限速时只应下发一次（清理残留），实际 %d 次", n.rateCalls)
	}
}

// 期望集合从非空变为空时，必须下发一次把内核清干净，然后回到静默。
func TestClearingLastRateLimitStopsReplay(t *testing.T) {
	cfg := defaultConfig()
	peer := defaultPeer()
	peer.RateLimitMbps = 10
	peers := []domain.Peer{peer}

	n := healthyNet()
	d := &fakeDriver{state: deviceState(t, cfg, peers)}
	src := &fakeSource{cfg: cfg, peers: peers}
	r := newTestReconciler(d, n, src)

	ctx := context.Background()
	for range 3 {
		if err := r.Reconcile(ctx); err != nil {
			t.Fatalf("Reconcile: %v", err)
		}
	}
	if n.rateCalls != 1 {
		t.Fatalf("初次下发应执行 1 次，实际 %d 次", n.rateCalls)
	}

	// 取消所有限速。
	src.peers = []domain.Peer{defaultPeer()}
	n.mu.Lock()
	n.rateApplied = false
	n.mu.Unlock()

	if err := r.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if n.rateCalls != 2 {
		t.Fatalf("取消限速应清理一次，实际调用 %d 次", n.rateCalls)
	}

	for range 5 {
		if err := r.Reconcile(ctx); err != nil {
			t.Fatalf("Reconcile: %v", err)
		}
	}
	if n.rateCalls != 2 {
		t.Errorf("清理后应回到静默，实际调用 %d 次", n.rateCalls)
	}
}

func TestRateFingerprintIgnoresOrderNoise(t *testing.T) {
	a := netdriver.RateLimit{ID: 1, Address: "10.8.0.2", Mbps: 10}
	b := netdriver.RateLimit{ID: 2, Address: "10.8.0.3", Mbps: 20}
	if rateFingerprint("wg0", []netdriver.RateLimit{a, b}) != rateFingerprint("wg0", []netdriver.RateLimit{b, a}) {
		t.Error("同一集合的不同顺序应得到同一指纹")
	}
	if rateFingerprint("wg0", []netdriver.RateLimit{a}) == rateFingerprint("wg1", []netdriver.RateLimit{a}) {
		t.Error("不同接口应得到不同指纹")
	}
}

func TestRateLimitErrorIsExposed(t *testing.T) {
	cfg := defaultConfig()
	peer := defaultPeer()
	peer.RateLimitMbps = 10
	peers := []domain.Peer{peer}

	n := healthyNet()
	n.rateErr = netdriver.ErrRateLimitUnsupported
	d := &fakeDriver{state: deviceState(t, cfg, peers)}
	r := newTestReconciler(d, n, fakeSource{cfg: cfg, peers: peers})

	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("限速失败不应中断调谐，得到 %v", err)
	}
	if !errors.Is(r.RateLimitError(), netdriver.ErrRateLimitUnsupported) {
		t.Errorf("限速错误应被记录，得到 %v", r.RateLimitError())
	}
	// 失败的期望集合仍然记指纹，避免每 2 秒重试一次注定失败的 tc。
	before := n.rateCalls
	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if n.rateCalls != before {
		t.Errorf("限速失败后不应每轮重试，调用次数 %d→%d", before, n.rateCalls)
	}
}

// TestRateLimitRetriesAfterTransientFailure 锁定"失败不记指纹"。
//
// 真机上暴露的缺陷：一次 tc 失败就把期望集合记成已同步，
// 之后每轮调谐都认为无需重试，限速永远不生效——
// 而失败只记在 DEBUG 级，日志里看不出任何异常。
// ErrRateLimitUnsupported（镜像缺 tc，重试也无意义）仍然记指纹。
func TestRateLimitRetriesAfterTransientFailure(t *testing.T) {
	cfg := defaultConfig()
	peer := defaultPeer()
	peer.RateLimitMbps = 10
	peers := []domain.Peer{peer}

	n := healthyNet()
	n.rateErr = errors.New("tc: RTNETLINK answers: Operation not supported")
	d := &fakeDriver{state: deviceState(t, cfg, peers)}
	r := newTestReconciler(d, n, fakeSource{cfg: cfg, peers: peers})

	ctx := context.Background()
	if err := r.Reconcile(ctx); err != nil {
		t.Fatalf("限速失败不应中断调谐，得到 %v", err)
	}
	if r.RateLimitError() == nil {
		t.Fatal("限速错误应被记录")
	}

	// 期望集合没变，但内核没跟上，必须继续重试。
	before := n.rateCalls
	if err := r.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if n.rateCalls != before+1 {
		t.Fatalf("临时失败后应重试，调用次数 %d→%d", before, n.rateCalls)
	}

	// 故障排除后应恢复，且此后不再重复下发。
	n.rateErr = nil
	if err := r.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if r.RateLimitError() != nil {
		t.Errorf("恢复后不应残留错误，得到 %v", r.RateLimitError())
	}
	settled := n.rateCalls
	if err := r.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if n.rateCalls != settled {
		t.Errorf("恢复后期望集合不变，不应再下发，调用次数 %d→%d", settled, n.rateCalls)
	}
}

var _ wgdriver.Driver = (*fakeDriver)(nil)
var _ netdriver.NetDriver = (*fakeNet)(nil)
