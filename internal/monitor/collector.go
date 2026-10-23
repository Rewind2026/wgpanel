package monitor

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/Rewind2026/wgpanel/internal/domain"
	"github.com/Rewind2026/wgpanel/internal/store"
	"github.com/Rewind2026/wgpanel/internal/wgdriver"
)

// SnapshotProvider 提供一次监控快照所需的期望态与运行态。
type SnapshotProvider interface {
	ServerConfig(ctx context.Context) (*domain.ServerConfig, error)
	ListPeers(ctx context.Context) ([]domain.Peer, error)
	PublicEndpoint(ctx context.Context) string
}

// Collector 周期采集指标并推送给订阅者。
type Collector struct {
	provider SnapshotProvider
	driver   wgdriver.Driver
	traffic  *store.TrafficRepo
	usage    UsageSink
	events   EventPublisher
	log      *slog.Logger

	// interval 由 intervalMu 保护，可被 SetInterval 在运行期改写。
	//
	// 它同时是缓存有效期（见 fresh），所以改间隔不只是改采集节奏，
	// 也会同步改变「一份快照算不算新鲜」。这两件事必须用同一个值，
	// 否则会出现「ticker 已经按新周期在跑，但缓存仍按旧周期判定过期」
	// 从而每个请求都触发一次冷缓存重采。
	intervalMu sync.RWMutex
	interval   time.Duration
	retune     chan time.Duration

	subsMu sync.RWMutex
	subs   map[chan domain.MetricsSnapshot]struct{}

	// collectMu 串行化 collect。
	//
	// collect 会改写 cpu/net/tunnelRx 三个差分 tracker 与 lastPeers 这张 map，
	// 而调用方不止采集循环自己：SnapshotNow 由 /metrics（Prometheus 抓取）
	// 与 Peer 指标接口并发调用。少一把锁就是 data race，而 map 的并发写
	// 在 Go 里是 fatal error——进程直接终止，没有任何日志。
	collectMu sync.Mutex

	// coldMu 串行化「查缓存 → 未命中则采集 → 写回缓存」这一整段。
	//
	// 只靠 collectMu 不够：collectMu 只保证两次采集不同时跑，
	// 但两个请求可以依次通过 Last() 的检查再依次采集，冷缓存下并发请求
	// 仍然会各采一遍。仪表盘首屏恰好就是并发打出 overview / peers /
	// traffic / /metrics 四个请求，所以这个窗口是常态而非极端情况。
	coldMu sync.Mutex

	cpu      cpuTracker
	net      rateTracker
	tunnelRx rateTracker

	last      domain.MetricsSnapshot
	lastMu    sync.RWMutex
	lastAt    time.Time
	lastPeers map[string]*domain.PeerStats

	// online 记录上一拍的在线集合，用于差分出上下线事件。
	onlineMu sync.Mutex
	online   map[int64]bool
}

// New 构造采集器。
func New(p SnapshotProvider, d wgdriver.Driver, traffic *store.TrafficRepo,
	interval time.Duration, log *slog.Logger) *Collector {
	if interval < 200*time.Millisecond {
		interval = time.Second
	}
	return &Collector{
		provider:  p,
		driver:    d,
		traffic:   traffic,
		interval:  interval,
		log:       log,
		retune:    make(chan time.Duration, 1),
		subs:      map[chan domain.MetricsSnapshot]struct{}{},
		lastPeers: map[string]*domain.PeerStats{},
		online:    map[int64]bool{},
	}
}

// SetInterval 在运行期改写采集间隔与缓存有效期。
//
// 非阻塞：通知信号满时直接丢弃，缓冲区里已经有一个待生效的新间隔。
func (c *Collector) SetInterval(d time.Duration) {
	if d < 200*time.Millisecond {
		d = time.Second
	}
	c.intervalMu.Lock()
	changed := d != c.interval
	c.interval = d
	c.intervalMu.Unlock()
	if !changed {
		return
	}
	select {
	case c.retune <- d:
	default:
	}
}

// currentInterval 返回当前生效的采集间隔。
func (c *Collector) currentInterval() time.Duration {
	c.intervalMu.RLock()
	defer c.intervalMu.RUnlock()
	return c.interval
}

// Interval 返回当前生效的采集间隔。
//
// 导出给测试与运维探针：设置项「保存后立即生效」是一条可断言的承诺，
// 而 interval 是包私有字段，没有这个访问器就只能反过来验证缓存行为，
// 绕远路还测不准。
func (c *Collector) Interval() time.Duration { return c.currentInterval() }

// SetSinks 注入配额记账与事件出口。缺省时采集器只做只读统计。
func (c *Collector) SetSinks(usage UsageSink, events EventPublisher) {
	c.usage, c.events = usage, events
}

// Subscribe 注册一个快照订阅者，返回取消函数。
func (c *Collector) Subscribe() (<-chan domain.MetricsSnapshot, func()) {
	ch := make(chan domain.MetricsSnapshot, 4)
	c.subsMu.Lock()
	c.subs[ch] = struct{}{}
	c.subsMu.Unlock()

	return ch, func() {
		c.subsMu.Lock()
		if _, ok := c.subs[ch]; ok {
			delete(c.subs, ch)
			close(ch)
		}
		c.subsMu.Unlock()
	}
}

// Last 返回最近一次快照，供 REST 接口直接读取。
func (c *Collector) Last() (domain.MetricsSnapshot, bool) {
	c.lastMu.RLock()
	defer c.lastMu.RUnlock()
	if c.lastAt.IsZero() {
		return domain.MetricsSnapshot{}, false
	}
	return c.last, true
}

// SnapshotNow 返回一份可直接用于响应的快照。
//
// 缓存新鲜（年龄小于一个采集周期）时直接返回；否则采集一次并写回缓存。
// 这条路径是 WGP_METRICS_ENABLED=false 或启动首拍时的唯一数据来源，
// 所以它必须既新鲜又便宜：
//
//   - 写回缓存。旧实现采完就丢，于是 Last() 恒为空，每个请求都重采一遍。
//     collect 不只是读 /proc 和全表 Peer，它还会往 traffic_sample 写
//     「1 行隧道汇总 + 每 Peer 1 行」，于是 N 个 Peer 的一次页面刷新
//     直接放大成 (N+1)×请求数 行的写入。SQLite 写池只有一条连接，
//     写放大最终把所有读请求一起拖慢。同秒重复写还会让原始流量曲线
//     出现同一时刻的重合点——traffic_sample 没有 (ts, peer_id) 唯一约束。
//
//   - 但不广播。广播是采集循环的职责；若按需采集也 publish，
//     每刷新一次页面就往所有 SSE 客户端推一帧重复快照。
//
//   - 采集失败不写缓存。否则会把一份零值快照钉在缓存里，
//     指标页从此空白且再也刷不出来。
func (c *Collector) SnapshotNow(ctx context.Context) domain.MetricsSnapshot {
	if snap, ok := c.fresh(); ok {
		return snap
	}

	c.coldMu.Lock()
	defer c.coldMu.Unlock()

	// 等锁期间可能已有别的请求或采集循环填好了缓存，重新判断一次。
	if snap, ok := c.fresh(); ok {
		return snap
	}
	snap, err := c.collect(ctx)
	if err != nil {
		c.log.Debug("on-demand collect failed", "error", err)
		return domain.MetricsSnapshot{}
	}
	c.cache(snap)
	return snap
}

// fresh 返回仍在有效期内的缓存快照。
//
// 有效期取采集周期：缓存比一个周期还旧，说明要么采集循环没启动
// （WGP_METRICS_ENABLED=false），要么它已经落后了——两种情况都需要重采。
func (c *Collector) fresh() (domain.MetricsSnapshot, bool) {
	c.lastMu.RLock()
	defer c.lastMu.RUnlock()
	if c.lastAt.IsZero() || time.Since(c.lastAt) >= c.currentInterval() {
		return domain.MetricsSnapshot{}, false
	}
	return c.last, true
}

// Start 运行采集循环，直到 ctx 取消。间隔变化时重建 ticker。
func (c *Collector) Start(ctx context.Context) {
	c.log.Info("metrics collector started", "interval", c.currentInterval())

	interval := c.currentInterval()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			c.log.Info("metrics collector stopped")
			return
		case <-ticker.C:
			snap, err := c.collect(ctx)
			if err != nil {
				c.log.Debug("collect failed", "error", err)
				continue
			}
			c.publish(snap)
		case d := <-c.retune:
			// 必须用新间隔重建 ticker：Reset 只归零倒计时，间隔不变，
			// 那样「保存设置」就只是一个成功的假象。
			ticker.Reset(d)
			c.log.Info("metrics interval updated", "interval", d)
		}
	}
}

// cache 只更新缓存，不广播。供按需采集路径使用。
func (c *Collector) cache(snap domain.MetricsSnapshot) {
	c.lastMu.Lock()
	c.last, c.lastAt = snap, time.Now()
	c.lastMu.Unlock()
}

// publish 更新缓存并向订阅者广播，慢消费者被丢弃而非阻塞采集。
func (c *Collector) publish(snap domain.MetricsSnapshot) {
	c.cache(snap)

	c.subsMu.RLock()
	defer c.subsMu.RUnlock()
	for ch := range c.subs {
		select {
		case ch <- snap:
		default:
		}
	}
}

// collect 采集一次完整快照。
//
// 全程持 collectMu：函数内的差分计算与 lastPeers 比对都是「读-改-写」，
// 必须整体串行，否则并发调用会读到彼此的中间状态。
// 注意不要在持锁期间调用 publish——publish 自己要拿 lastMu，
// 而 elapsed() 又要拿 lastMu.RLock，锁序会形成死锁。
func (c *Collector) collect(ctx context.Context) (domain.MetricsSnapshot, error) {
	c.collectMu.Lock()
	defer c.collectMu.Unlock()

	snap := domain.MetricsSnapshot{}

	// 系统资源
	memUsed, memPct, _ := readMem()
	diskUsed, diskPct, _ := diskUsage("/")
	rx, tx, _ := readNetCounters()
	now := time.Now()
	netRxRate, netTxRate := c.net.compute(rx, tx, c.elapsed())

	snap.System = domain.SystemMetrics{
		CPUPercent:    c.cpu.compute(),
		MemUsedBytes:  memUsed,
		MemPercent:    memPct,
		DiskUsedBytes: diskUsed,
		DiskPercent:   diskPct,
		NetRxBps:      netRxRate,
		NetTxBps:      netTxRate,
		NetRxBytes:    rx,
		NetTxBytes:    tx,
		UptimeSeconds: readUptime(),
		Goroutines:    goroutineCount(),
	}

	// 隧道与 Peer 运行态
	cfg, err := c.provider.ServerConfig(ctx)
	if err != nil {
		return snap, err
	}
	peers, err := c.provider.ListPeers(ctx)
	if err != nil {
		return snap, err
	}

	stats, _ := c.driver.PeerStats(cfg.Interface)
	totalRx, totalTx, _ := c.driver.TotalCounters(cfg.Interface)
	tunnelRxRate, tunnelTxRate := c.tunnelRx.compute(totalRx, totalTx, c.elapsed())

	snap.Tunnel = domain.TunnelMetrics{
		Interface:      cfg.Interface,
		ListenPort:     cfg.ListenPort,
		PublicEndpoint: c.provider.PublicEndpoint(ctx),
		RxBytes:        totalRx,
		TxBytes:        totalTx,
		RxRateBps:      tunnelRxRate,
		TxRateBps:      tunnelTxRate,
		TotalPeers:     len(peers),
		InterfaceUp:    c.driver != nil && stats != nil,
		CollectedAt:    now,
	}

	snap.Peers = make([]domain.PeerStats, 0, len(peers))
	deltas := make(map[int64]int64, len(peers))
	quotaIDs := make([]int64, 0, len(peers))
	for i := range peers {
		p := peers[i]
		st := &domain.PeerStats{PeerID: p.ID, PublicKey: p.PublicKey, AllowedIPs: []string{}}
		if s, ok := stats[p.PublicKey]; ok {
			st.Online = s.Online
			st.AllowedIPs = s.AllowedIPs
			st.RxBytes = s.RxBytes
			st.TxBytes = s.TxBytes
			st.LastHandshake = s.LastHandshake
			st.ConnectedSince = s.ConnectedSince
			st.ConnectedSeconds = s.ConnectedSeconds
		}
		if prev, ok := c.lastPeers[p.PublicKey]; ok && st.Online && st.RxBytes >= prev.RxBytes && st.TxBytes >= prev.TxBytes {
			delta := (st.RxBytes - prev.RxBytes) + (st.TxBytes - prev.TxBytes)
			secs := int64(c.elapsed().Seconds())
			if secs > 0 {
				st.RxRateBps = (st.RxBytes - prev.RxBytes) / secs
				st.TxRateBps = (st.TxBytes - prev.TxBytes) / secs
			}
			if p.TrafficQuotaBytes > 0 {
				deltas[p.ID] = delta
			}
		}
		snap.Peers = append(snap.Peers, *st)
		if st.Online {
			snap.Tunnel.OnlinePeers++
			quotaIDs = append(quotaIDs, p.ID)
		}
	}
	c.lastPeers = stats

	c.persist(ctx, snap)
	c.detectPresence(ctx, snap.Peers, now)
	c.accountQuota(ctx, deltas)
	c.markActive(ctx, quotaIDs, now.Unix())
	return snap, nil
}

// elapsed 返回距上次采集的时间间隔，首次为 0。
func (c *Collector) elapsed() time.Duration {
	c.lastMu.RLock()
	defer c.lastMu.RUnlock()
	if c.lastAt.IsZero() {
		return 0
	}
	return time.Since(c.lastAt)
}

// persist 写入流量采样与系统指标，写失败只记日志不阻断采集。
func (c *Collector) persist(ctx context.Context, snap domain.MetricsSnapshot) {
	if c.traffic == nil {
		return
	}
	ts := snap.Tunnel.CollectedAt.Unix()
	samples := make([]domain.TrafficSample, 0, len(snap.Peers)+1)
	// PeerID = 0 是隧道汇总行；每 Peer 单独成行，历史曲线才能按客户端归因。
	samples = append(samples, domain.TrafficSample{
		TS: ts, PeerID: 0, RxBytes: snap.Tunnel.RxBytes, TxBytes: snap.Tunnel.TxBytes,
	})
	for i := range snap.Peers {
		p := &snap.Peers[i]
		samples = append(samples, domain.TrafficSample{
			TS: ts, PeerID: p.PeerID, RxBytes: p.RxBytes, TxBytes: p.TxBytes,
			HandshakeTS: p.LastHandshake.Unix(),
		})
	}
	if err := c.traffic.InsertSamples(ctx, samples); err != nil {
		c.log.Debug("persist traffic samples failed", "error", err)
	}
	if err := c.traffic.InsertSystemMetric(ctx, snap.System); err != nil {
		c.log.Debug("persist system metric failed", "error", err)
	}
}
