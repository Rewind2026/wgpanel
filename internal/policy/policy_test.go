package policy

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/Rewind2026/wgpanel/internal/domain"
	"github.com/Rewind2026/wgpanel/internal/store"
)

// collector 收集发布的事件。
type collector struct {
	mu     sync.Mutex
	events []domain.Event
}

func (c *collector) Publish(_ context.Context, ev domain.Event) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, ev)
}

func (c *collector) types() []domain.EventType {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]domain.EventType, 0, len(c.events))
	for _, e := range c.events {
		out = append(out, e.Type)
	}
	return out
}

func (c *collector) count(t domain.EventType) int {
	n := 0
	for _, got := range c.types() {
		if got == t {
			n++
		}
	}
	return n
}

type fixture struct {
	engine *Engine
	store  *store.Store
	peers  *store.PeerRepo
	bus    *collector
}

func newFixture(t *testing.T, settings domain.Settings) *fixture {
	t.Helper()
	st, err := store.OpenInMemory()
	if err != nil {
		t.Fatalf("open in-memory store: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	if err := st.SettingRepo().SaveSettings(context.Background(), settings); err != nil {
		t.Fatalf("save settings: %v", err)
	}
	bus := &collector{}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return &fixture{
		engine: New(st.PeerRepo(), st.SettingRepo(), st.AuditRepo(), bus, log),
		store:  st,
		peers:  st.PeerRepo(),
		bus:    bus,
	}
}

// addPeer 插入一个用于判定的 Peer。
func (f *fixture) addPeer(t *testing.T, p *domain.Peer) int64 {
	t.Helper()
	p.Normalize()
	id, err := f.peers.Create(context.Background(), p)
	if err != nil {
		t.Fatalf("create peer: %v", err)
	}
	return id
}

// seedPeriod 把 Peer 标记为「在 at 所在周期已经被观测过」。
//
// 用于模拟「引擎已经跑过一个周期」的前置状态：quota_period_start 落库后，
// 新建 Peer 和跨周期 Peer 的判定路径是不同的，必须能分别构造。
func (f *fixture) seedPeriod(t *testing.T, id int64, period domain.QuotaPeriod, at time.Time) {
	t.Helper()
	start := PeriodStart(period, at)
	if start.IsZero() {
		t.Fatalf("period %q has no period start", period)
	}
	if err := f.peers.AdoptQuotaPeriod(context.Background(), id, start.Unix()); err != nil {
		t.Fatalf("seed period: %v", err)
	}
}

func basePeer(name string) *domain.Peer {
	return &domain.Peer{
		Name:       name,
		PublicKey:  "dpmLZIKxpezJZNlZwlv6pLg8goGW8C6jasftaRxv8yQ=",
		PrivateKey: "4I8V/IGyK1AnVuQtPUN0nmelG0DXNPGTHDR2aRFnRlI=",
		Address:    name + "-addr",
		Enabled:    true,
	}
}

func TestQuotaExceededSuspendsPeer(t *testing.T) {
	f := newFixture(t, domain.DefaultSettings())
	now := time.Now()

	p := basePeer("laptop")
	p.TrafficQuotaBytes = 100
	p.QuotaUsedBytes = 150
	p.QuotaPeriod = domain.QuotaPeriodMonthly
	id := f.addPeer(t, p)

	res, err := f.engine.Run(context.Background(), now)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Suspended != 1 {
		t.Errorf("应停用 1 个 Peer，实际 %d", res.Suspended)
	}
	got, err := f.peers.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Enabled {
		t.Error("超配额 Peer 应被停用")
	}
	if got.SuspendedBy != domain.SuspendedQuota {
		t.Errorf("停用原因应为 quota，实际 %q", got.SuspendedBy)
	}
	if f.bus.count(domain.EventPeerQuota) != 1 {
		t.Errorf("应发布一条配额事件，实际 %#v", f.bus.types())
	}
}

func TestQuotaEnforcementCanBeDisabled(t *testing.T) {
	cfg := domain.DefaultSettings()
	cfg.QuotaEnforcement = false
	f := newFixture(t, cfg)

	p := basePeer("laptop")
	p.TrafficQuotaBytes = 100
	p.QuotaUsedBytes = 150
	f.addPeer(t, p)

	res, err := f.engine.Run(context.Background(), time.Now())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Suspended != 0 {
		t.Errorf("关闭配额执行时不应停用，实际 %d", res.Suspended)
	}
}

func TestNeverPeriodIsNotEnforced(t *testing.T) {
	f := newFixture(t, domain.DefaultSettings())

	p := basePeer("laptop")
	p.TrafficQuotaBytes = 100
	p.QuotaUsedBytes = 999
	p.QuotaPeriod = domain.QuotaPeriodNever
	id := f.addPeer(t, p)

	if _, err := f.engine.Run(context.Background(), time.Now()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got, _ := f.peers.Get(context.Background(), id)
	if !got.Enabled {
		t.Error("quotaPeriod=never 的 Peer 不应因配额被停用")
	}
}

func TestUnlimitedQuotaIsNeverSuspended(t *testing.T) {
	f := newFixture(t, domain.DefaultSettings())

	p := basePeer("laptop")
	p.TrafficQuotaBytes = 0
	p.QuotaUsedBytes = 1 << 40
	id := f.addPeer(t, p)

	if _, err := f.engine.Run(context.Background(), time.Now()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got, _ := f.peers.Get(context.Background(), id)
	if !got.Enabled {
		t.Error("未设配额的 Peer 不应被停用")
	}
}

func TestExpirySuspendsPeer(t *testing.T) {
	f := newFixture(t, domain.DefaultSettings())
	now := time.Now()

	past := now.Add(-24 * time.Hour)
	p := basePeer("laptop")
	p.ExpiresAt = &past
	id := f.addPeer(t, p)

	res, err := f.engine.Run(context.Background(), now)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Suspended != 1 {
		t.Errorf("过期 Peer 应被停用，实际 %d", res.Suspended)
	}
	got, _ := f.peers.Get(context.Background(), id)
	if got.SuspendedBy != domain.SuspendedExpired {
		t.Errorf("停用原因应为 expired，实际 %q", got.SuspendedBy)
	}
	if f.bus.count(domain.EventPeerExpired) != 1 {
		t.Errorf("应发布过期事件，实际 %#v", f.bus.types())
	}
}

func TestExpiryWarningIsSentOncePerDay(t *testing.T) {
	cfg := domain.DefaultSettings()
	cfg.ExpiryWarnDays = 7
	f := newFixture(t, cfg)
	now := time.Now()

	soon := now.Add(48 * time.Hour)
	p := basePeer("laptop")
	p.ExpiresAt = &soon
	f.addPeer(t, p)

	for i := range 3 {
		res, err := f.engine.Run(context.Background(), now)
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if i == 0 && res.Warned != 1 {
			t.Errorf("首轮应预警一次，实际 %d", res.Warned)
		}
		if i > 0 && res.Warned != 0 {
			t.Errorf("同一天不应重复预警，实际 %d", res.Warned)
		}
	}
	if n := f.bus.count(domain.EventPeerExpiring); n != 1 {
		t.Errorf("预警事件应只发一次，实际 %d", n)
	}
}

func TestExpiryWarningStopsAfterOneDay(t *testing.T) {
	cfg := domain.DefaultSettings()
	cfg.ExpiryWarnDays = 7
	f := newFixture(t, cfg)
	day1 := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)

	soon := day1.Add(48 * time.Hour)
	p := basePeer("laptop")
	p.ExpiresAt = &soon
	f.addPeer(t, p)

	if _, err := f.engine.Run(context.Background(), day1); err != nil {
		t.Fatalf("Run day1: %v", err)
	}
	res, err := f.engine.Run(context.Background(), day1.AddDate(0, 0, 1))
	if err != nil {
		t.Fatalf("Run day2: %v", err)
	}
	if res.Warned != 1 {
		t.Errorf("跨天后应再次预警，实际 %d", res.Warned)
	}
}

// 周期切换必须同时做两件事：清零用量 + 恢复因超配额停用的 Peer。
func TestQuotaPeriodRolloverRestoresSuspendedPeer(t *testing.T) {
	f := newFixture(t, domain.DefaultSettings())
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

	p := basePeer("laptop")
	p.TrafficQuotaBytes = 100
	p.QuotaUsedBytes = 150
	p.QuotaPeriod = domain.QuotaPeriodDaily
	id := f.addPeer(t, p)
	// 上个周期已经观测过，才能构造出真正的「跨周期」。
	f.seedPeriod(t, id, p.QuotaPeriod, now.AddDate(0, 0, -1))
	if err := f.peers.SetEnabledByPolicy(context.Background(), id, false, domain.SuspendedQuota); err != nil {
		t.Fatalf("suspend: %v", err)
	}

	res, err := f.engine.Run(context.Background(), now)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.ResetQuota != 1 || res.Restored != 1 {
		t.Errorf("周期切换应同时清零与恢复，实际 %+v", res)
	}
	got, _ := f.peers.Get(context.Background(), id)
	if !got.Enabled {
		t.Error("周期切换后应自动恢复 Peer")
	}
	if got.QuotaUsedBytes != 0 {
		t.Errorf("周期切换后用量应清零，实际 %d", got.QuotaUsedBytes)
	}
	if got.SuspendedBy != domain.SuspendedNone {
		t.Errorf("恢复后不应再标记停用原因，实际 %q", got.SuspendedBy)
	}
	if f.bus.count(domain.EventPeerRestored) != 1 {
		t.Errorf("应发布恢复事件，实际 %#v", f.bus.types())
	}
}

// 人工停用不是策略停用，周期重置不能把它偷偷打开。
func TestQuotaRolloverDoesNotRestoreManualSuspension(t *testing.T) {
	f := newFixture(t, domain.DefaultSettings())
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

	p := basePeer("laptop")
	p.TrafficQuotaBytes = 100
	p.QuotaUsedBytes = 150
	p.QuotaPeriod = domain.QuotaPeriodDaily
	id := f.addPeer(t, p)
	f.seedPeriod(t, id, p.QuotaPeriod, now.AddDate(0, 0, -1))
	if err := f.peers.SetEnabledByPolicy(context.Background(), id, false, domain.SuspendedNone); err != nil {
		t.Fatalf("manual disable: %v", err)
	}

	res, err := f.engine.Run(context.Background(), now)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Restored != 0 {
		t.Errorf("人工停用不应被周期重置恢复，实际 %+v", res)
	}
	got, _ := f.peers.Get(context.Background(), id)
	if got.Enabled {
		t.Error("人工停用的 Peer 仍应保持停用")
	}
	// 用量仍然要清零，否则它会永远欠费。
	if got.QuotaUsedBytes != 0 {
		t.Errorf("用量仍应清零，实际 %d", got.QuotaUsedBytes)
	}
}

// 同一周期内重复判定必须完全幂等：不清零、不发事件、不写库。
func TestQuotaRolloverIsIdempotentWithinPeriod(t *testing.T) {
	f := newFixture(t, domain.DefaultSettings())
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

	p := basePeer("laptop")
	p.TrafficQuotaBytes = 100
	p.QuotaPeriod = domain.QuotaPeriodDaily
	id := f.addPeer(t, p)

	// 上一轮先认领周期（本轮不做结算），再验证同周期内重复判定完全幂等。
	f.seedPeriod(t, id, p.QuotaPeriod, now)
	if _, err := f.engine.Run(context.Background(), now); err != nil {
		t.Fatalf("first Run: %v", err)
	}
	// 采集器在两次判定之间累加了用量。
	if _, err := f.peers.AddQuotaUsage(context.Background(), id, 42); err != nil {
		t.Fatalf("AddQuotaUsage: %v", err)
	}
	for range 3 {
		res, err := f.engine.Run(context.Background(), now.Add(90*time.Minute))
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if res.ResetQuota != 0 {
			t.Errorf("同一周期内不应重复结算，实际 %+v", res)
		}
	}
	got, _ := f.peers.Get(context.Background(), id)
	if got.QuotaUsedBytes != 42 {
		t.Errorf("同一周期内用量不应被清零，实际 %d", got.QuotaUsedBytes)
	}
}

// 引擎停机数小时后重启必须补上漏掉的结算，而不是永远丢失一整个周期。
func TestMissedRolloverIsCaughtUpOnNextRun(t *testing.T) {
	f := newFixture(t, domain.DefaultSettings())

	p := basePeer("laptop")
	p.TrafficQuotaBytes = 100
	p.QuotaUsedBytes = 150
	p.QuotaPeriod = domain.QuotaPeriodDaily
	id := f.addPeer(t, p)
	if err := f.peers.SetEnabledByPolicy(context.Background(), id, false, domain.SuspendedQuota); err != nil {
		t.Fatalf("suspend: %v", err)
	}

	// 3 月 1 日 12:00 结算过一次，引擎随后停机两天。
	settleAt := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	if _, err := f.engine.Run(context.Background(), settleAt); err != nil {
		t.Fatalf("settle: %v", err)
	}
	// 结算后 Peer 是启用的，重新停用并累加用量，模拟停机期间又被打满。
	if err := f.peers.SetEnabledByPolicy(context.Background(), id, false, domain.SuspendedQuota); err != nil {
		t.Fatalf("re-suspend: %v", err)
	}
	if _, err := f.peers.AddQuotaUsage(context.Background(), id, 200); err != nil {
		t.Fatalf("AddQuotaUsage: %v", err)
	}

	// 3 月 3 日 09:00 引擎才恢复。
	res, err := f.engine.Run(context.Background(), time.Date(2026, 3, 3, 9, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("catch-up Run: %v", err)
	}
	if res.ResetQuota != 1 || res.Restored != 1 {
		t.Errorf("补跑应完成结算与恢复，实际 %+v", res)
	}
	got, _ := f.peers.Get(context.Background(), id)
	if !got.Enabled || got.QuotaUsedBytes != 0 {
		t.Errorf("补跑后应恢复且清零，实际 enabled=%v used=%d", got.Enabled, got.QuotaUsedBytes)
	}
}

func TestNeverPeriodIsNotSettled(t *testing.T) {
	f := newFixture(t, domain.DefaultSettings())

	p := basePeer("laptop")
	p.TrafficQuotaBytes = 100
	p.QuotaUsedBytes = 77
	p.QuotaPeriod = domain.QuotaPeriodNever
	id := f.addPeer(t, p)

	for range 2 {
		if _, err := f.engine.Run(context.Background(), time.Now()); err != nil {
			t.Fatalf("Run: %v", err)
		}
	}
	got, _ := f.peers.Get(context.Background(), id)
	if got.QuotaUsedBytes != 77 {
		t.Errorf("never 周期不应清零用量，实际 %d", got.QuotaUsedBytes)
	}
}

// 首次见到一个 Peer 时只能认领周期，不能清零用量。
//
// quota_period_start 默认 0。旧版本升级上来的库里每个 Peer 都是 0，
// 如果把 0 当成「上个周期」，第一次判定就会把所有人本周期已用的流量清零，
// 相当于给全站发一次免费额度。
func TestFirstObservationOnlyAdoptsPeriodWithoutClearingUsage(t *testing.T) {
	f := newFixture(t, domain.DefaultSettings())
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

	p := basePeer("laptop")
	p.TrafficQuotaBytes = 100
	p.QuotaUsedBytes = 150
	p.QuotaPeriod = domain.QuotaPeriodDaily
	id := f.addPeer(t, p)

	res, err := f.engine.Run(context.Background(), now)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.ResetQuota != 0 {
		t.Errorf("首次观测不应结算配额，实际 %+v", res)
	}
	got, _ := f.peers.Get(context.Background(), id)
	if got.QuotaUsedBytes != 150 {
		t.Errorf("首次观测不应清零用量，实际 %d", got.QuotaUsedBytes)
	}
	// 用量还在，所以应当照常判定为超配额并停用。
	if got.Enabled || got.SuspendedBy != domain.SuspendedQuota {
		t.Errorf("超配额 Peer 应被停用，实际 enabled=%v reason=%q", got.Enabled, got.SuspendedBy)
	}

	// 认领起点已经落库，紧接着的第二次判定必须是空操作。
	res2, err := f.engine.Run(context.Background(), now.Add(time.Hour))
	if err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if res2 != (Result{}) {
		t.Errorf("同周期重复判定应为空操作，实际 %+v", res2)
	}
}

func TestPeriodStart(t *testing.T) {
	now := time.Date(2026, 3, 4, 15, 30, 0, 0, time.UTC) // 周三
	cases := []struct {
		period domain.QuotaPeriod
		want   time.Time
	}{
		{domain.QuotaPeriodDaily, time.Date(2026, 3, 4, 0, 0, 0, 0, time.UTC)},
		{domain.QuotaPeriodWeekly, time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC)}, // 周一
		{domain.QuotaPeriodMonthly, time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)},
	}
	for _, c := range cases {
		if got := PeriodStart(c.period, now); !got.Equal(c.want) {
			t.Errorf("PeriodStart(%s) = %s, want %s", c.period, got, c.want)
		}
	}
	if !PeriodStart(domain.QuotaPeriodNever, now).IsZero() {
		t.Error("never 周期应返回零值")
	}
}

func TestHumanBytes(t *testing.T) {
	cases := map[int64]string{
		0: "0 B", 512: "512 B", 1024: "1.0 KiB",
		1024 * 1024: "1.0 MiB", 1024 * 1024 * 1024: "1.0 GiB",
	}
	for in, want := range cases {
		if got := humanBytes(in); got != want {
			t.Errorf("humanBytes(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestRunIsIdempotentWhenNothingToDo(t *testing.T) {
	f := newFixture(t, domain.DefaultSettings())
	p := basePeer("laptop")
	f.addPeer(t, p)

	for range 3 {
		res, err := f.engine.Run(context.Background(), time.Now())
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if res.Suspended != 0 || res.Restored != 0 || res.ResetQuota != 0 {
			t.Errorf("无策略变更时结果应全为 0，实际 %+v", res)
		}
	}
	if len(f.bus.types()) != 0 {
		t.Errorf("无策略变更时不应发布事件，实际 %#v", f.bus.types())
	}
}
