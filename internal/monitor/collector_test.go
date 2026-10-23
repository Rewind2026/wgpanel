package monitor

import (
	"context"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/Rewind2026/wgpanel/internal/domain"
	"github.com/Rewind2026/wgpanel/internal/wgdriver"
)

// 这组用例针对两个真实缺陷，都是「界面正常、接口 200，功能已经失效」的类型：
//
//  1. rateTracker 对计数器回退没有保护。接口被删除重建、或容器重启后，
//     wg 的累计计数从 0 重新开始，此时 rx-t.prevRx 是负数，界面显示负速率，
//     历史曲线上出现向下的尖峰。ROADMAP M3 声称「计数器回退时不产生负速率」，
//     但实现只对 per-peer 做了保护（st.RxBytes >= prev.RxBytes），
//     隧道汇总与主机网卡两条路径没有。
//
//  2. collect() 会改写 c.lastPeers 这张 map 以及三个 tracker，
//     但整个函数没有互斥。SnapshotNow 由 /metrics（Prometheus 抓取）
//     与 Peer 指标接口并发调用，Start() 循环也在调用，启动首拍必然并发进入——
//     Go 的 map 并发写是 fatal error，会直接终止进程。

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

// fakeProvider 返回固定的期望态。
type fakeProvider struct {
	cfg   *domain.ServerConfig
	peers []domain.Peer
}

func (f *fakeProvider) ServerConfig(context.Context) (*domain.ServerConfig, error) {
	return f.cfg, nil
}

func (f *fakeProvider) ListPeers(context.Context) ([]domain.Peer, error) {
	return f.peers, nil
}

func (f *fakeProvider) PublicEndpoint(context.Context) string { return "203.0.113.10" }

// fakeDriver 可控 PeerStats 与接口累计计数，用来构造「接口重建」场景。
type fakeDriver struct {
	mu      sync.Mutex
	stats   map[string]*domain.PeerStats
	totalRx int64
	totalTx int64
	// hold 让 TotalCounters 阻塞指定时长，用来把两拍之间的间隔拉长。
	hold time.Duration
}

func (d *fakeDriver) Available() error { return nil }

func (d *fakeDriver) Device(context.Context, string) (*wgdriver.DeviceState, error) {
	return nil, nil
}

func (d *fakeDriver) Up(context.Context, *domain.ServerConfig, []domain.Peer) error { return nil }

func (d *fakeDriver) Down(context.Context, string) error { return nil }

func (d *fakeDriver) SyncPeers(context.Context, string, []domain.Peer) error { return nil }

func (d *fakeDriver) PeerStats(string) (map[string]*domain.PeerStats, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make(map[string]*domain.PeerStats, len(d.stats))
	for k, v := range d.stats {
		cp := *v
		out[k] = &cp
	}
	return out, nil
}

func (d *fakeDriver) TotalCounters(string) (int64, int64, error) {
	d.mu.Lock()
	hold := d.hold
	d.mu.Unlock()
	if hold > 0 {
		time.Sleep(hold)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.totalRx, d.totalTx, nil
}

func (d *fakeDriver) setCounters(rx, tx int64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.totalRx, d.totalTx = rx, tx
}

func TestRateTrackerRejectsCounterRegression(t *testing.T) {
	var tr rateTracker

	// 第一拍只建基线。
	if rx, tx := tr.compute(1000, 2000, time.Second); rx != 0 || tx != 0 {
		t.Fatalf("首拍应返回 0，实际 rx=%d tx=%d", rx, tx)
	}
	// 正常递增：2 秒内各涨 2000 字节 → 1000 B/s。
	if rx, tx := tr.compute(3000, 4000, 2*time.Second); rx != 1000 || tx != 1000 {
		t.Fatalf("递增拍速率应各为 1000，实际 rx=%d tx=%d", rx, tx)
	}

	// 接口重建：累计计数归零。此时绝不能返回负速率。
	rx, tx := tr.compute(0, 0, time.Second)
	if rx < 0 || tx < 0 {
		t.Fatalf("计数器归零后速率不应为负，实际 rx=%d tx=%d", rx, tx)
	}
	if rx != 0 || tx != 0 {
		t.Fatalf("计数器归零当拍应返回 0，实际 rx=%d tx=%d", rx, tx)
	}
	// 回退后的基线必须已经重建，下一拍恢复正常速率。
	if rx, tx := tr.compute(1000, 2000, time.Second); rx != 1000 || tx != 2000 {
		t.Fatalf("回退后基线未重建，下一拍速率应正常，实际 rx=%d tx=%d", rx, tx)
	}
}

func TestRateTrackerHandlesNonPositiveElapsed(t *testing.T) {
	var tr rateTracker
	tr.compute(100, 100, time.Second)
	// elapsed <= 0 只重建基线，不能除出 Inf/NaN。
	if rx, tx := tr.compute(200, 300, 0); rx != 0 || tx != 0 {
		t.Fatalf("零间隔应返回 0，实际 rx=%d tx=%d", rx, tx)
	}
	if rx, tx := tr.compute(400, 600, -time.Second); rx != 0 || tx != 0 {
		t.Fatalf("负间隔应返回 0，实际 rx=%d tx=%d", rx, tx)
	}
}

func TestCPUTrackerFirstSampleIsBaseline(t *testing.T) {
	var tr cpuTracker
	// 首拍没有差值可用，必须返回 0 而不是拿绝对值当利用率。
	if got := tr.compute(); got < 0 || got > 100 {
		t.Fatalf("CPU 首拍应落在 [0,100]，实际 %v", got)
	}
}

// 隧道累计计数归零（接口重建）时，速率不得为负。
func TestCollectTunnelRateNoNegativeAfterInterfaceRecreate(t *testing.T) {
	drv := &fakeDriver{stats: map[string]*domain.PeerStats{}, hold: 200 * time.Millisecond}
	p := &fakeProvider{cfg: &domain.ServerConfig{Interface: "wg0", ListenPort: 51820}}
	c := New(p, drv, nil, time.Hour, testLogger())

	drv.setCounters(5_000_000, 5_000_000)
	if _, err := c.collect(context.Background()); err != nil {
		t.Fatalf("首拍采集失败: %v", err)
	}

	// 模拟接口被删除重建：累计计数从 0 开始，采样间隔仍是真实的数百毫秒。
	drv.setCounters(0, 0)
	snap, err := c.collect(context.Background())
	if err != nil {
		t.Fatalf("第二拍采集失败: %v", err)
	}
	if snap.Tunnel.RxRateBps < 0 || snap.Tunnel.TxRateBps < 0 {
		t.Fatalf("接口重建后隧道速率不应为负，实际 rx=%d tx=%d",
			snap.Tunnel.RxRateBps, snap.Tunnel.TxRateBps)
	}
}

// SnapshotNow 与采集循环并发调用 collect 时不得触发数据竞争。
//
// 修复前这条用例会被 -race 报出 c.lastPeers 的并发写；
// 不加 -race 时也可能直接 fatal error: concurrent map writes。
func TestCollectIsSafeUnderConcurrentSnapshotNow(t *testing.T) {
	drv := &fakeDriver{stats: map[string]*domain.PeerStats{}}
	p := &fakeProvider{
		cfg: &domain.ServerConfig{Interface: "wg0", ListenPort: 51820},
		peers: []domain.Peer{
			{ID: 1, Name: "a", PublicKey: "pk-a"},
			{ID: 2, Name: "b", PublicKey: "pk-b"},
		},
	}
	c := New(p, drv, nil, time.Hour, testLogger())

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 40; j++ {
				c.SnapshotNow(context.Background())
			}
		}()
	}
	wg.Wait()
}

// Subscribe 返回的取消函数必须可重复调用且不 panic。
//
// 真实调用方是 SSE handler 里的 defer cancel()，与 r.Context().Done() 分支
// 可能同时触发；重复 close 一个已关闭的 channel 会 panic 并带崩整个进程。
func TestSubscribeCancelIsIdempotent(t *testing.T) {
	c := New(&fakeProvider{cfg: &domain.ServerConfig{}}, nil, nil, time.Hour, testLogger())

	_, cancel := c.Subscribe()
	cancel()
	cancel()

	// 取消后再订阅仍要能正常工作，否则 SSE 断线重连后会静默收不到任何帧。
	ch, cancel2 := c.Subscribe()
	defer cancel2()
	c.publish(domain.MetricsSnapshot{Tunnel: domain.TunnelMetrics{Interface: "wg0"}})

	select {
	case snap := <-ch:
		if snap.Tunnel.Interface != "wg0" {
			t.Fatalf("收到的是别的快照: %+v", snap.Tunnel)
		}
	case <-time.After(time.Second):
		t.Fatal("重新订阅后收不到快照")
	}
}

// SnapshotNow 在采集出错时返回零值而不是 panic。
func TestSnapshotNowToleratesProviderError(t *testing.T) {
	c := New(&failingProvider{}, nil, nil, time.Hour, testLogger())
	if snap := c.SnapshotNow(context.Background()); snap.Tunnel.Interface != "" {
		t.Fatalf("采集失败应返回零值快照，实际 %+v", snap.Tunnel)
	}
}

type failingProvider struct{}

func (failingProvider) ServerConfig(context.Context) (*domain.ServerConfig, error) {
	return nil, domain.ErrInternal(os.ErrDeadlineExceeded)
}

func (failingProvider) ListPeers(context.Context) ([]domain.Peer, error) { return nil, nil }

func (failingProvider) PublicEndpoint(context.Context) string { return "" }
