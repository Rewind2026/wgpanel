package monitor

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/Rewind2026/wgpanel/internal/domain"
	"github.com/Rewind2026/wgpanel/internal/store"
)

// 这组用例针对按需采集路径上的写放大缺陷。
//
// 生产形态是采集循环每 interval 采一次并写缓存，REST 只读缓存。
// 但 collect() 的结果不回写缓存：只要缓存是空的，每个请求都会完整采一遍，
// 并顺带把「隧道汇总 1 行 + 每 Peer 1 行」的 traffic_sample 写进库。
//
// 缓存为空在生产里不是假想——WGP_METRICS_ENABLED=false 时采集循环根本不启动
// （main.go 里 collector.Start 被 MetricsEnabled 门控），此时每个指标请求都
// 走这条路径。于是「打开一次仪表盘」的代价随 Peer 数线性放大成磁盘写入，
// 页面刷新越频繁放大越严重；而 SQLite 写池只有一条连接
// （store.Open 里 db.SetMaxOpenConns(1)），写放大最终把所有读请求一起拖慢。
//
// 附带被这条路径放大的还有两个问题：
//   - 同一秒内的多个请求各写一遍样本，traffic_sample 没有 (ts, peer_id)
//     唯一约束，于是出现同一时刻的重复行，原始曲线多出重合点；
//   - 并发请求同时未命中缓存时，会并发进入 collect 并发跑 N 遍全量采集。

// countingProvider 记录 ListPeers 被调用的次数，即完整采集发生了几次。
type countingProvider struct {
	mu      sync.Mutex
	calls   int
	cfg     *domain.ServerConfig
	peers   []domain.Peer
	failing bool
}

func (p *countingProvider) ServerConfig(context.Context) (*domain.ServerConfig, error) {
	p.mu.Lock()
	failing := p.failing
	p.mu.Unlock()
	if failing {
		return nil, domain.ErrInternal(os.ErrDeadlineExceeded)
	}
	return p.cfg, nil
}

func (p *countingProvider) ListPeers(context.Context) ([]domain.Peer, error) {
	p.mu.Lock()
	p.calls++
	p.mu.Unlock()
	return p.peers, nil
}

func (p *countingProvider) PublicEndpoint(context.Context) string { return "203.0.113.10" }

func (p *countingProvider) collectCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

func benchPeers(n int) []domain.Peer {
	peers := make([]domain.Peer, 0, n)
	for i := 1; i <= n; i++ {
		peers = append(peers, domain.Peer{
			ID:        int64(i),
			Name:      "p",
			PublicKey: "pk",
			Address:   fmt.Sprintf("10.8.0.%d", i%250+2),
			Enabled:   true,
		})
	}
	return peers
}

// 连续多次按需采集只能触发一次全量采集，其余命中缓存。
func TestSnapshotNowCollectsOnceThenServesCache(t *testing.T) {
	p := &countingProvider{
		cfg:   &domain.ServerConfig{Interface: "wg0", ListenPort: 51820},
		peers: benchPeers(8),
	}
	drv := &fakeDriver{stats: map[string]*domain.PeerStats{}}
	c := New(p, drv, nil, time.Hour, testLogger())

	for i := 0; i < 20; i++ {
		snap := c.SnapshotNow(context.Background())
		if len(snap.Peers) != 8 {
			t.Fatalf("第 %d 次快照 Peer 数应仍为 8，实际 %d", i, len(snap.Peers))
		}
	}

	if got := p.collectCount(); got != 1 {
		t.Fatalf("20 次按需采集应只触发 1 次全量采集，实际 %d 次——每次请求都在重采", got)
	}
}

// 仪表盘首屏会并发打出 overview / peers / traffic / /metrics 四个请求，
// 缓存冷的时候这四个请求必须合并成一次采集，而不是各采一遍。
func TestSnapshotNowCollapsesConcurrentBurstIntoOneCollect(t *testing.T) {
	p := &countingProvider{
		cfg:   &domain.ServerConfig{Interface: "wg0", ListenPort: 51820},
		peers: benchPeers(8),
	}
	drv := &fakeDriver{stats: map[string]*domain.PeerStats{}}
	// hold 把单次采集拉长到 50ms，确保并发请求真的撞在一起，
	// 否则它们可能只是恰好排在前一个请求写缓存之后而测不出问题。
	drv.hold = 50 * time.Millisecond
	c := New(p, drv, nil, time.Hour, testLogger())

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if snap := c.SnapshotNow(context.Background()); len(snap.Peers) != 8 {
				t.Errorf("并发快照 Peer 数应仍为 8，实际 %d", len(snap.Peers))
			}
		}()
	}
	wg.Wait()

	if got := p.collectCount(); got != 1 {
		t.Fatalf("8 个并发冷缓存请求应合并成 1 次采集，实际 %d 次", got)
	}
}

// 采集循环没启动时数据不能被永久冻结：缓存过期后必须重新采集。
func TestSnapshotNowRefreshesOnceIntervalElapsed(t *testing.T) {
	p := &countingProvider{
		cfg:   &domain.ServerConfig{Interface: "wg0", ListenPort: 51820},
		peers: benchPeers(4),
	}
	drv := &fakeDriver{stats: map[string]*domain.PeerStats{}}
	// 200ms 是 New 允许的最小间隔，再小会被抬到 1s。
	c := New(p, drv, nil, 200*time.Millisecond, testLogger())

	c.SnapshotNow(context.Background())
	if got := p.collectCount(); got != 1 {
		t.Fatalf("首次按需采集应为 1 次，实际 %d", got)
	}

	// 缓存仍在有效期内：不得重复采集。
	c.SnapshotNow(context.Background())
	if got := p.collectCount(); got != 1 {
		t.Fatalf("缓存仍在有效期内时不应重新采集，实际已采集 %d 次", got)
	}

	time.Sleep(250 * time.Millisecond)

	snap := c.SnapshotNow(context.Background())
	if got := p.collectCount(); got != 2 {
		t.Fatalf("缓存过期后应重新采集，累计应为 2 次，实际 %d——数据被永久冻结了", got)
	}
	if len(snap.Peers) != 4 {
		t.Fatalf("刷新后的快照 Peer 数应仍为 4，实际 %d", len(snap.Peers))
	}
}

// 按需采集不广播：广播是采集循环的职责。
// 若按需采集也广播，页面每刷新一次就往所有 SSE 客户端推一帧重复快照。
//
// 这条在修复前后都是绿的——它不是缺陷的证明，而是防止修复过度纠正：
// 把结果写回缓存时很容易顺手也调 publish。
func TestSnapshotNowDoesNotBroadcastToSubscribers(t *testing.T) {
	p := &countingProvider{
		cfg:   &domain.ServerConfig{Interface: "wg0", ListenPort: 51820},
		peers: benchPeers(4),
	}
	drv := &fakeDriver{stats: map[string]*domain.PeerStats{}}
	c := New(p, drv, nil, time.Hour, testLogger())

	ch, cancel := c.Subscribe()
	defer cancel()

	c.SnapshotNow(context.Background())

	select {
	case snap := <-ch:
		t.Fatalf("按需采集不应向订阅者广播，实际收到 %d 个 Peer 的快照", len(snap.Peers))
	case <-time.After(200 * time.Millisecond):
	}
}

// 写放大最直接的度量：样本表行数。20 次请求不能写出 20 份样本。
func TestSnapshotNowDoesNotWriteSamplesPerRequest(t *testing.T) {
	st, err := store.OpenInMemory()
	if err != nil {
		t.Fatalf("open in-memory store: %v", err)
	}
	defer st.Close()

	const n = 20
	p := &countingProvider{
		cfg:   &domain.ServerConfig{Interface: "wg0", ListenPort: 51820},
		peers: benchPeers(6),
	}
	drv := &fakeDriver{stats: map[string]*domain.PeerStats{}}
	c := New(p, drv, st.TrafficRepo(), time.Hour, testLogger())

	for i := 0; i < n; i++ {
		c.SnapshotNow(context.Background())
	}

	var samples int
	row := st.DB().QueryRowContext(context.Background(), `SELECT COUNT(*) FROM traffic_sample`)
	if err := row.Scan(&samples); err != nil {
		t.Fatalf("count traffic_sample: %v", err)
	}

	// 一次采集写 1 行隧道汇总 + 6 行 Peer，共 7 行。
	const want = 7
	if samples != want {
		t.Fatalf("%d 次请求写出了 %d 行样本，期望 %d 行——每个请求都在写一遍样本表",
			n, samples, want)
	}
}

// 采集失败不能把缓存写脏：失败后缓存必须仍为空，下一个请求要能重试。
//
// 这条防的是修复引入的新故障模式——若失败路径也写缓存，就会把一份零值快照
// 钉在缓存里，指标页从此空白且再也刷新不出来。
func TestSnapshotNowFailureLeavesCacheCold(t *testing.T) {
	p := &countingProvider{
		cfg:     &domain.ServerConfig{Interface: "wg0", ListenPort: 51820},
		peers:   benchPeers(4),
		failing: true,
	}
	drv := &fakeDriver{stats: map[string]*domain.PeerStats{}}
	c := New(p, drv, nil, time.Hour, testLogger())

	if snap := c.SnapshotNow(context.Background()); snap.Tunnel.Interface != "" {
		t.Fatalf("采集失败应返回零值快照，实际 %+v", snap.Tunnel)
	}
	if _, ok := c.Last(); ok {
		t.Fatal("采集失败不得写入缓存，否则指标页会永久停在零值快照")
	}

	// provider 恢复后必须能重新采到，而不是被失败结果钉死。
	p.mu.Lock()
	p.failing = false
	p.mu.Unlock()

	if snap := c.SnapshotNow(context.Background()); len(snap.Peers) != 4 {
		t.Fatalf("provider 恢复后应采到 4 个 Peer，实际 %d", len(snap.Peers))
	}
}
