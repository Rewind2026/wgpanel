package store

import (
	"context"
	"sync"
	"testing"

	"github.com/Rewind2026/wgpanel/internal/domain"
)

func TestConsumeRefreshTokenIsSingleUseUnderConcurrency(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	repo := s.UserRepo()

	user := &domain.User{Username: "owner", PasswordHash: "h", Role: domain.RoleOwner, Enabled: true}
	id, err := repo.Create(ctx, user)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	const raw = "refresh-token-raw-value"
	if err := repo.StoreRefreshToken(ctx, id, raw, nowUnix()+3600); err != nil {
		t.Fatalf("store refresh token: %v", err)
	}

	// 并发轮换：只有一个 goroutine 应成功消费该令牌。
	const workers = 8
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		granted int64
	)
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := repo.ConsumeRefreshToken(ctx, raw)
			if err == nil && got == id {
				mu.Lock()
				granted++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if granted != 1 {
		t.Fatalf("同一刷新令牌应只被消费一次，实际成功 %d 次", granted)
	}
	if _, err := repo.ConsumeRefreshToken(ctx, raw); err == nil {
		t.Error("已消费的令牌再次使用应返回未授权错误")
	}
}

func TestConsumeRefreshTokenRejectsExpiredAndUnknown(t *testing.T) {
	ctx := context.Background()
	repo := newTestStore(t).UserRepo()

	user := &domain.User{Username: "owner", PasswordHash: "h", Role: domain.RoleOwner, Enabled: true}
	id, err := repo.Create(ctx, user)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	if err := repo.StoreRefreshToken(ctx, id, "expired", nowUnix()-10); err != nil {
		t.Fatalf("store expired token: %v", err)
	}
	if _, err := repo.ConsumeRefreshToken(ctx, "expired"); err == nil {
		t.Error("过期令牌不应被接受")
	}
	if _, err := repo.ConsumeRefreshToken(ctx, "never-issued"); err == nil {
		t.Error("未知令牌不应被接受")
	}
}

func TestInsertSamplesAttributesTrafficPerPeer(t *testing.T) {
	ctx := context.Background()
	repo := newTestStore(t).TrafficRepo()

	base := nowUnix()
	samples := []domain.TrafficSample{
		{TS: base, PeerID: 0, RxBytes: 1000, TxBytes: 2000},
		{TS: base, PeerID: 7, RxBytes: 300, TxBytes: 400},
		{TS: base, PeerID: 8, RxBytes: 500, TxBytes: 600},
		{TS: base + 10, PeerID: 7, RxBytes: 380, TxBytes: 460},
	}
	if err := repo.InsertSamples(ctx, samples); err != nil {
		t.Fatalf("InsertSamples: %v", err)
	}

	peerSeven := int64(7)
	points, err := repo.QueryTraffic(ctx, base-1, base+10, "raw", &peerSeven)
	if err != nil {
		t.Fatalf("QueryTraffic(peer 7): %v", err)
	}
	if len(points) != 2 {
		t.Fatalf("Peer 7 应有 2 个采样点，实际 %d", len(points))
	}
	if points[0].RxBytes != 300 || points[1].RxBytes != 380 {
		t.Errorf("Peer 7 字节序列错误：%d, %d", points[0].RxBytes, points[1].RxBytes)
	}
	// 第二个点应按 10 秒间隔差分出速率。
	if points[1].RxRate != 8 || points[1].TxRate != 6 {
		t.Errorf("Peer 7 速率应为 8/6，实际 %d/%d", points[1].RxRate, points[1].TxRate)
	}

	// 隧道汇总行不应混入任一 Peer 的数据。
	tunnel, err := repo.QueryTraffic(ctx, base-1, base+10, "raw", nil)
	if err != nil {
		t.Fatalf("QueryTraffic(tunnel): %v", err)
	}
	if len(tunnel) != 1 {
		t.Fatalf("隧道汇总应有 1 个采样点，实际 %d", len(tunnel))
	}
	if tunnel[0].RxBytes != 1000 {
		t.Errorf("隧道汇总字节应仅含汇总行，得到 %d", tunnel[0].RxBytes)
	}
}

func TestRollupKeepsPeerAndTunnelSeparate(t *testing.T) {
	ctx := context.Background()
	repo := newTestStore(t).TrafficRepo()

	// Rollup 是逐级聚合：raw → 10m → hour → day，故先聚合 10m 再聚合 hour。
	base := (nowUnix() - 7200) / 3600 * 3600
	samples := []domain.TrafficSample{
		{TS: base, PeerID: 0, RxBytes: 100, TxBytes: 100},
		{TS: base + 60, PeerID: 3, RxBytes: 50, TxBytes: 60},
		{TS: base + 120, PeerID: 3, RxBytes: 70, TxBytes: 80},
	}
	if err := repo.InsertSamples(ctx, samples); err != nil {
		t.Fatalf("InsertSamples: %v", err)
	}
	if err := repo.Rollup(ctx, "10m", base-1, 600); err != nil {
		t.Fatalf("Rollup 10m: %v", err)
	}
	if err := repo.Rollup(ctx, "hour", base-1, 3600); err != nil {
		t.Fatalf("Rollup hour: %v", err)
	}

	peerThree := int64(3)
	points, err := repo.QueryTraffic(ctx, base-1, base+600, "hour", &peerThree)
	if err != nil {
		t.Fatalf("QueryTraffic(peer 3 rollup): %v", err)
	}
	if len(points) == 0 {
		t.Fatal("降采样后 Peer 3 应仍有数据")
	}

	tunnel, err := repo.QueryTraffic(ctx, base-1, base+600, "hour", nil)
	if err != nil {
		t.Fatalf("QueryTraffic(tunnel rollup): %v", err)
	}
	if len(tunnel) == 0 {
		t.Fatal("降采样后隧道汇总应仍有数据")
	}
	for _, p := range tunnel {
		if p.RxBytes == 120 {
			t.Errorf("隧道汇总不应包含 Peer 3 的字节：%+v", p)
		}
	}
}

func TestInsertSamplesEmptyIsNoop(t *testing.T) {
	if err := newTestStore(t).TrafficRepo().InsertSamples(context.Background(), nil); err != nil {
		t.Fatalf("空批次不应报错：%v", err)
	}
}

// 累计计数器在接口重建 / 容器重启后归零，差分会出现负值。
// 这些区间的真实速率无从得知，但绝不能把负值送到界面上：
// 曲线上会出现向下的尖峰，前端还会累加出负的「合计流量」。
func TestComputeRatesClampsCounterResetToZero(t *testing.T) {
	points := []domain.TrafficPoint{
		{TS: 100, RxBytes: 5_000_000_000, TxBytes: 4_000_000_000},
		// 正常增长段。
		{TS: 160, RxBytes: 5_600_000_000, TxBytes: 4_400_000_000},
		// 计数器归零重建，差分为负。
		{TS: 220, RxBytes: 0, TxBytes: 0},
		// 归零后重新累计。
		{TS: 280, RxBytes: 600_000_000, TxBytes: 400_000_000},
	}

	computeRates(points)

	// 正常增长段：rx +600MB/60s，tx +400MB/60s。
	const (
		wantRx = 10_000_000
		wantTx = 6_666_666
	)
	if points[1].RxRate != wantRx {
		t.Errorf("正常增长段下行速率 = %d，期望 %d", points[1].RxRate, wantRx)
	}
	if points[1].TxRate != wantTx {
		t.Errorf("正常增长段上行速率 = %d，期望 %d", points[1].TxRate, wantTx)
	}
	for _, i := range []int{2} {
		if points[i].RxRate != 0 || points[i].TxRate != 0 {
			t.Errorf("计数器归零点 %d 的速率 = (rx=%d, tx=%d)，期望 (0, 0)",
				i, points[i].RxRate, points[i].TxRate)
		}
	}
	// 归零后重新累计的段仍然要算出正常速率，不能被前一拍的归零连累。
	if points[3].RxRate != wantRx {
		t.Errorf("归零后下行速率 = %d，期望 %d", points[3].RxRate, wantRx)
	}
	if points[3].TxRate != wantTx {
		t.Errorf("归零后上行速率 = %d，期望 %d", points[3].TxRate, wantTx)
	}
	// 首点没有前序采样，速率必须为 0。
	if points[0].RxRate != 0 || points[0].TxRate != 0 {
		t.Errorf("首点速率应为 0，实际 (rx=%d, tx=%d)", points[0].RxRate, points[0].TxRate)
	}
	for i, p := range points {
		if p.RxRate < 0 || p.TxRate < 0 {
			t.Errorf("第 %d 点出现负速率 (rx=%d, tx=%d)", i, p.RxRate, p.TxRate)
		}
	}
}
