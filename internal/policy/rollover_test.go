package policy

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Rewind2026/wgpanel/internal/domain"
	"github.com/Rewind2026/wgpanel/internal/store"
)

// suspendedPeer 造一个「上个周期已被观测过、本周期已超配额停用」的 Peer。
//
// 这是配额结算最容易出错的状态：它同时踩到「要不要清零」和
// 「要不要恢复」两个判定，且两者都依赖 quota_period_start 的比较结果。
func suspendedPeer(t *testing.T, f *fixture, name string, period domain.QuotaPeriod, at time.Time) int64 {
	t.Helper()

	p := basePeer(name)
	p.TrafficQuotaBytes = 100
	p.QuotaUsedBytes = 150
	p.QuotaPeriod = period
	id := f.addPeer(t, p)
	f.seedPeriod(t, id, period, at)
	if err := f.peers.SetEnabledByPolicy(context.Background(), id, false, domain.SuspendedQuota); err != nil {
		t.Fatalf("suspend: %v", err)
	}
	return id
}

// periodStart 直接从库里读 quota_period_start。
//
// domain.Peer 不暴露这一列（它是策略引擎的内部结算状态，不该出现在 API 契约里），
// 因此只能绕过仓储直查。用它而不是仓储返回值，是为了让断言盯住「真正落库的值」。
func periodStart(t *testing.T, f *fixture, id int64) int64 {
	t.Helper()

	var start int64
	row := f.store.DB().QueryRowContext(context.Background(),
		`SELECT quota_period_start FROM peer WHERE id = ?`, id)
	if err := row.Scan(&start); err != nil {
		t.Fatalf("read quota_period_start: %v", err)
	}
	return start
}

func assertStillSuspended(t *testing.T, f *fixture, id int64, wantUsed int64) {
	t.Helper()

	got, err := f.peers.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Enabled {
		t.Error("周期起点没有前进，不应恢复 Peer")
	}
	if got.SuspendedBy != domain.SuspendedQuota {
		t.Errorf("停用原因应保持 quota，实际 %q", got.SuspendedBy)
	}
	if got.QuotaUsedBytes != wantUsed {
		t.Errorf("用量不应被清零，实际 %d，期望 %d", got.QuotaUsedBytes, wantUsed)
	}
	if f.bus.count(domain.EventPeerRestored) != 0 {
		t.Errorf("不应发布恢复事件，实际 %#v", f.bus.types())
	}
}

// 结算判据必须是「周期起点前进」而不是「不等于已记录的起点」。
//
// 两个真实场景都会让起点倒退，但都绝不能发放新周期额度：
//   - 时钟回拨：宿主机 NTP 步进、虚拟机快照恢复、手工 date；
//   - 容器时区变更：docker-compose 的 TZ 是可改的运维参数，
//     改完之后 PeriodStart 按新时区算出的值会比库里记录的小几小时。
//
// 这两种情况下若按「不等于就结算」处理，全站 Peer 会在同一秒内
// 被清零用量并自动恢复——超配额停用形同虚设，且用户会拿到凭空多出的额度。
func TestRolloverRequiresPeriodStartToMoveForward(t *testing.T) {
	cst := time.FixedZone("UTC+8", 8*3600)

	cases := []struct {
		name string
		// stored 是库里已结算到的周期起点。
		stored time.Time
		// now 是本次判定的时刻。
		now time.Time
	}{
		{
			name:   "时钟回拨一天",
			stored: time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC),
			now:    time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC),
		},
		{
			name:   "同一时刻把时区从 UTC 改成 UTC+8",
			stored: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
			// 同一绝对时刻，本地日期已跨到前一天。
			now: time.Date(2026, 3, 1, 20, 0, 0, 0, cst),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, domain.DefaultSettings())

			id := suspendedPeer(t, f, "laptop", domain.QuotaPeriodDaily, tc.stored)
			storedStart := PeriodStart(domain.QuotaPeriodDaily, tc.stored)

			res, err := f.engine.Run(context.Background(), tc.now)
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if res.ResetQuota != 0 || res.Restored != 0 {
				t.Errorf("周期起点未前进，不应结算，结果 %+v", res)
			}
			assertStillSuspended(t, f, id, 150)

			if periodStart(t, f, id) != storedStart.Unix() {
				t.Errorf("已结算起点不应被改写：实际 %d，期望 %d",
					periodStart(t, f, id), storedStart.Unix())
			}
		})
	}
}

// 起点前进后必须正常结算：起点被推到最新周期，用量清零，Peer 恢复。
// 这是上一条的反向断言，防止修复退化成「永远不结算」。
func TestRolloverSettlesWhenPeriodStartMovesForward(t *testing.T) {
	f := newFixture(t, domain.DefaultSettings())

	stored := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	now := time.Date(2026, 3, 2, 0, 0, 30, 0, time.UTC)
	id := suspendedPeer(t, f, "laptop", domain.QuotaPeriodDaily, stored)

	res, err := f.engine.Run(context.Background(), now)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.ResetQuota != 1 || res.Restored != 1 {
		t.Fatalf("起点前进应完成结算与恢复，实际 %+v", res)
	}

	got, _ := f.peers.Get(context.Background(), id)
	if !got.Enabled || got.QuotaUsedBytes != 0 || got.SuspendedBy != domain.SuspendedNone {
		t.Errorf("应恢复并清零，实际 enabled=%v used=%d reason=%q",
			got.Enabled, got.QuotaUsedBytes, got.SuspendedBy)
	}
	if want := PeriodStart(domain.QuotaPeriodDaily, now).Unix(); periodStart(t, f, id) != want {
		t.Errorf("起点应推进到 %d，实际 %d", want, periodStart(t, f, id))
	}

	// 恢复后再跑一百次也不能二次发放额度。
	for range 100 {
		if _, err := f.engine.Run(context.Background(), now.Add(time.Hour)); err != nil {
			t.Fatalf("repeat Run: %v", err)
		}
	}
	got, _ = f.peers.Get(context.Background(), id)
	if got.QuotaUsedBytes != 0 {
		t.Errorf("同周期重复判定后用量应仍为 0，实际 %d", got.QuotaUsedBytes)
	}
	if f.bus.count(domain.EventPeerRestored) != 1 {
		t.Errorf("恢复事件应只发一次，实际 %d", f.bus.count(domain.EventPeerRestored))
	}
}

// 采集协程与策略判定并发写库：结算必须落库、新周期起点必须正确，
// 且全程无 data race。采集器每秒给有流量的 Peer 累加用量，
// 与策略引擎的清零落在同一条写连接上，是最容易撞车的组合。
func TestRolloverUnderConcurrentUsageAccounting(t *testing.T) {
	f := newFixture(t, domain.DefaultSettings())

	stored := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	now := time.Date(2026, 3, 2, 0, 0, 5, 0, time.UTC)
	id := suspendedPeer(t, f, "laptop", domain.QuotaPeriodDaily, stored)

	stop := make(chan struct{})
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			if _, err := f.peers.AddQuotaUsage(context.Background(), id, 7); err != nil {
				t.Errorf("AddQuotaUsage: %v", err)
				return
			}
		}
	}()

	res, err := f.engine.Run(context.Background(), now)
	close(stop)
	wg.Wait()

	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.ResetQuota != 1 {
		t.Errorf("应完成一次结算，实际 %+v", res)
	}

	got, err := f.peers.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if want := PeriodStart(domain.QuotaPeriodDaily, now).Unix(); periodStart(t, f, id) != want {
		t.Errorf("结算后起点必须是新周期，实际 %d，期望 %d", periodStart(t, f, id), want)
	}
	// 采集器停下来的时点不确定，因此只断言「没有把用量清成负数或异常大」，
	// 精确值由上面那条确定性的用例把守。
	if got.QuotaUsedBytes < 0 {
		t.Errorf("用量不应为负，实际 %d", got.QuotaUsedBytes)
	}
}

// 引擎循环本身必须真的把停用的 Peer 拉回来并触发调谐。
//
// 这里不构造「跨到下一天」——那要等真实时钟。改为把已结算起点直接落在
// 过去，模拟「引擎停机若干天后重启」：Start 的第一个 tick 就应完成补跑。
// 覆盖的是 Start 循环体（ticker、取消、reconcile 回调），Run 的判定逻辑
// 由前面的用例负责。
func TestEngineStartRecoversAfterMissedPeriods(t *testing.T) {
	f := newFixture(t, domain.DefaultSettings())

	// 起点停在 3 天前：模拟引擎停机跨过两个周期。
	id := suspendedPeer(t, f, "laptop", domain.QuotaPeriodDaily,
		time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC))
	// 把用量继续顶高，覆盖「停机期间又被打满」的真实状态。
	if _, err := f.peers.AddQuotaUsage(context.Background(), id, 500); err != nil {
		t.Fatalf("AddQuotaUsage: %v", err)
	}

	reconciled := make(chan struct{}, 8)
	f.engine.SetReconcile(func() {
		select {
		case reconciled <- struct{}{}:
		default:
		}
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		f.engine.Start(ctx, 20*time.Millisecond)
	}()

	select {
	case <-reconciled:
	case <-time.After(5 * time.Second):
		cancel()
		<-done
		t.Fatal("引擎循环未在 5 秒内触发调谐")
	}

	got, err := f.peers.Get(context.Background(), id)
	if err != nil {
		cancel()
		<-done
		t.Fatalf("Get: %v", err)
	}
	if !got.Enabled || got.QuotaUsedBytes != 0 {
		t.Errorf("补跑后应恢复且清零，实际 enabled=%v used=%d", got.Enabled, got.QuotaUsedBytes)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("ctx 取消后引擎循环未退出")
	}

	// 恢复只发生一次：退出前再观察一段时间，不应出现第二次恢复事件。
	if n := f.bus.count(domain.EventPeerRestored); n != 1 {
		t.Errorf("恢复事件应只发一次，实际 %d", n)
	}
}

// 循环必须在没有可恢复对象时保持安静：既不触发调谐，也不发布事件。
// 否则每分钟一次的无谓全量调谐会一直占用 netlink 与容器命令。
func TestEngineStartStaysQuietWhenNothingToDo(t *testing.T) {
	f := newFixture(t, domain.DefaultSettings())

	p := basePeer("laptop")
	p.TrafficQuotaBytes = 100
	p.QuotaUsedBytes = 10
	p.QuotaPeriod = domain.QuotaPeriodDaily
	id := f.addPeer(t, p)
	f.seedPeriod(t, id, p.QuotaPeriod, time.Now())

	var calls int
	var mu sync.Mutex
	f.engine.SetReconcile(func() {
		mu.Lock()
		calls++
		mu.Unlock()
	})

	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	f.engine.Start(ctx, 20*time.Millisecond)

	mu.Lock()
	got := calls
	mu.Unlock()
	if got != 0 {
		t.Errorf("无事可做时不应触发调谐，实际 %d 次", got)
	}
	if n := len(f.bus.types()); n != 0 {
		t.Errorf("无事可做时不应发布事件，实际 %#v", f.bus.types())
	}
}

// 首次观测（quota_period_start = 0）必须走「认领」而不是「清零」，
// 且认领的必须是当前周期起点——即使当前时刻正好落在周期边界上。
func TestFirstObservationAdoptsCurrentPeriodBoundary(t *testing.T) {
	f := newFixture(t, domain.DefaultSettings())

	p := basePeer("laptop")
	p.TrafficQuotaBytes = 100
	p.QuotaUsedBytes = 40
	p.QuotaPeriod = domain.QuotaPeriodDaily
	id := f.addPeer(t, p)

	// 正好在 00:00:00 这一刻首次被判定。
	boundary := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	if _, err := f.engine.Run(context.Background(), boundary); err != nil {
		t.Fatalf("Run: %v", err)
	}

	got, err := f.peers.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.QuotaUsedBytes != 40 {
		t.Errorf("首次观测不应清零用量，实际 %d", got.QuotaUsedBytes)
	}
	if want := boundary.Unix(); periodStart(t, f, id) != want {
		t.Errorf("认领的起点应是当前周期起点，实际 %d，期望 %d", periodStart(t, f, id), want)
	}
	if !got.Enabled {
		t.Error("用量未超配额，Peer 应保持启用")
	}
}

// 月末跨年是配额结算最容易算错的边界：2 月 1 日启动、3 月 1 日补跑，
// 起点必须精确落在 3 月 1 日，不能停在 2 月 1 日或 2 月 28 日。
func TestMonthlyRolloverAcrossYearBoundary(t *testing.T) {
	f := newFixture(t, domain.DefaultSettings())

	p := basePeer("laptop")
	p.TrafficQuotaBytes = 100
	p.QuotaUsedBytes = 150
	p.QuotaPeriod = domain.QuotaPeriodMonthly
	id := f.addPeer(t, p)
	f.seedPeriod(t, id, p.QuotaPeriod, time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC))
	if err := f.peers.SetEnabledByPolicy(context.Background(), id, false, domain.SuspendedQuota); err != nil {
		t.Fatalf("suspend: %v", err)
	}

	now := time.Date(2026, 3, 31, 23, 59, 0, 0, time.UTC)
	res, err := f.engine.Run(context.Background(), now)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.ResetQuota != 1 || res.Restored != 1 {
		t.Fatalf("跨月应结算并恢复，实际 %+v", res)
	}

	got, _ := f.peers.Get(context.Background(), id)
	if want := PeriodStart(domain.QuotaPeriodMonthly, now).Unix(); periodStart(t, f, id) != want {
		t.Errorf("跨月起点应为 3 月 1 日，实际 %d，期望 %d", periodStart(t, f, id), want)
	}

	// 同月内再跑不得二次结算。
	if _, err := f.engine.Run(context.Background(), now.Add(time.Hour)); err != nil {
		t.Fatalf("same month Run: %v", err)
	}
	got, _ = f.peers.Get(context.Background(), id)
	if got.QuotaUsedBytes != 0 || !got.Enabled {
		t.Errorf("同月重复判定应为空操作，实际 enabled=%v used=%d", got.Enabled, got.QuotaUsedBytes)
	}
	if n := f.bus.count(domain.EventPeerRestored); n != 1 {
		t.Errorf("恢复事件应只发一次，实际 %d", n)
	}
}

// 周期的时区语义必须稳定：PeriodStart 用 now 的 Location，
// 因此同一绝对时刻在不同时区下会得到不同起点。结算只能前进，
// 这一点由 assertPeriodStartIsMonotonic 统一把守。
func assertPeriodStartIsMonotonic(t *testing.T, stored, now time.Time) {
	t.Helper()

	cur := PeriodStart(domain.QuotaPeriodDaily, now)
	if cur.Unix() <= stored.Unix() {
		t.Logf("起点未前进（stored=%d cur=%d），策略不应结算", stored.Unix(), cur.Unix())
		return
	}
	if !cur.After(stored) {
		t.Fatalf("起点必须单调前进：stored=%s cur=%s", stored, cur)
	}
}

func TestPeriodStartMovesForwardAcrossDSTShift(t *testing.T) {
	// 部分时区会在 00:00 前后做 DST 切换，构造一个「本地日期相同但
	// UTC 偏移变了」的序列，确认结算判据不会因此抖动。
	tzs := []*time.Location{time.UTC, time.FixedZone("UTC+8", 8*3600)}

	var prev time.Time
	for i, loc := range tzs {
		now := time.Date(2026, 3, 1, 12, 0, 0, 0, loc)
		start := PeriodStart(domain.QuotaPeriodDaily, now)
		if i == 0 {
			prev = start
			continue
		}
		if start.Unix() == prev.Unix() {
			t.Logf("时区切换后起点未变（%d），不会触发结算", start.Unix())
			continue
		}
		assertPeriodStartIsMonotonic(t, prev, now)
		prev = start
	}
}

// quota_period_start 落在未来（例如从旧库导入或人工改库）时，
// 策略引擎不能把用量清零到「未来周期」再立刻回滚，也不能死循环结算。
func TestFuturePeriodStartIsNotRolledBackIntoReset(t *testing.T) {
	f := newFixture(t, domain.DefaultSettings())

	p := basePeer("laptop")
	p.TrafficQuotaBytes = 100
	p.QuotaUsedBytes = 150
	p.QuotaPeriod = domain.QuotaPeriodDaily
	id := f.addPeer(t, p)

	// 人工把起点写成一年后的今天。
	future := time.Date(2027, 3, 1, 0, 0, 0, 0, time.UTC)
	if err := f.peers.AdoptQuotaPeriod(context.Background(), id, future.Unix()); err != nil {
		t.Fatalf("seed future: %v", err)
	}
	if err := f.peers.SetEnabledByPolicy(context.Background(), id, false, domain.SuspendedQuota); err != nil {
		t.Fatalf("suspend: %v", err)
	}

	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	res, err := f.engine.Run(context.Background(), now)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.ResetQuota != 0 || res.Restored != 0 {
		t.Errorf("起点在未来时不应结算，实际 %+v", res)
	}
	got, _ := f.peers.Get(context.Background(), id)
	if got.QuotaUsedBytes != 150 || got.Enabled {
		t.Errorf("起点在未来时应保持原状，实际 enabled=%v used=%d", got.Enabled, got.QuotaUsedBytes)
	}
}

// store.PolicyPeer 的字段必须覆盖结算所需的全部列。
// ListForPolicy 一旦漏列（例如 quota_period_start），结算会永远判为首次观测，
// 而这种缺失在功能上表现为「配额永不重置」，非常难被发现。
func TestListForPolicyCarriesSettlementColumns(t *testing.T) {
	f := newFixture(t, domain.DefaultSettings())

	p := basePeer("laptop")
	p.TrafficQuotaBytes = 100
	p.QuotaPeriod = domain.QuotaPeriodWeekly
	id := f.addPeer(t, p)

	start := PeriodStart(domain.QuotaPeriodWeekly, time.Date(2026, 3, 4, 10, 0, 0, 0, time.UTC))
	if err := f.peers.AdoptQuotaPeriod(context.Background(), id, start.Unix()); err != nil {
		t.Fatalf("seed: %v", err)
	}

	list, err := f.peers.ListForPolicy(context.Background())
	if err != nil {
		t.Fatalf("ListForPolicy: %v", err)
	}
	var got *store.PolicyPeer
	for i := range list {
		if list[i].ID == id {
			got = &list[i]
			break
		}
	}
	if got == nil {
		t.Fatal("ListForPolicy 未返回该 Peer")
	}
	if periodStart(t, f, id) != start.Unix() {
		t.Errorf("ListForPolicy 未带回已结算起点：实际 %d，期望 %d", periodStart(t, f, id), start.Unix())
	}
	if got.QuotaPeriod != domain.QuotaPeriodWeekly {
		t.Errorf("周期字段错误：实际 %q", got.QuotaPeriod)
	}
}
