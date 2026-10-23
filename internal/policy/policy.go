// Package policy 实现流量配额与生命周期策略。
//
// 策略引擎与调谐器的分工：
//   - 调谐器负责「数据库里启用的 Peer 应当出现在内核里」。
//   - 策略引擎负责「哪些 Peer 现在应该处于启用状态」。
//
// 因此策略引擎只改数据库的 enabled 字段，随后触发一次调谐即可让内核收敛，
// 不需要自己碰内核，避免两处逻辑各自下发导致状态打架。
package policy

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/Rewind2026/wgpanel/internal/domain"
	"github.com/Rewind2026/wgpanel/internal/store"
)

// Publisher 是事件出口，由事件总线实现。
type Publisher interface {
	Publish(ctx context.Context, ev domain.Event)
}

// Engine 周期性地判定配额与有效期。
type Engine struct {
	peers     *store.PeerRepo
	settings  *store.SettingRepo
	audit     *store.AuditRepo
	publisher Publisher
	log       *slog.Logger

	// mu 保护 warned。策略单协程运行，加锁是为了让 Run 可被测试并发调用。
	mu     sync.Mutex
	warned map[warnedKey]struct{}

	reconcile func()
}

// New 构造策略引擎。
func New(peers *store.PeerRepo, settings *store.SettingRepo, audit *store.AuditRepo,
	pub Publisher, log *slog.Logger) *Engine {
	return &Engine{
		peers: peers, settings: settings, audit: audit, publisher: pub, log: log,
		warned: map[warnedKey]struct{}{},
	}
}

// SetReconcile 注入「触发一次内核调谐」的回调。
//
// 策略引擎只改数据库的 enabled 字段；如果不在改完后立刻调谐，
// 超配额/过期的 Peer 要等下一个调谐周期才从内核消失，中间这段是可用的流量。
func (e *Engine) SetReconcile(fn func()) { e.reconcile = fn }

// Result 汇总一次判定的结果，便于日志与测试断言。
type Result struct {
	Suspended  int
	Restored   int
	ResetQuota int
	Warned     int
}

// Run 判定一次策略并返回结果。
func (e *Engine) Run(ctx context.Context, now time.Time) (Result, error) {
	peers, err := e.peers.ListForPolicy(ctx)
	if err != nil {
		return Result{}, err
	}
	cfg, err := e.settings.LoadSettings(ctx)
	if err != nil {
		return Result{}, err
	}

	res := Result{}
	for i := range peers {
		p := peers[i]

		// 周期结算先于其它判定：用量不清零的话，上个周期超配额的 Peer 在新周期里
		// 会立刻又被判为超额，永远等不到恢复。
		switch settled, err := settlePeriod(ctx, e.peers, &p, now); {
		case err != nil:
			return res, err
		case settled:
			res.ResetQuota++
			p.QuotaUsed = 0
			// 只有「因超配额停用」才跟着周期重置回来。
			// 人工停用和过期停用都必须保持原样：管理员手动关掉的 Peer 若被周期重置
			// 偷偷打开，等于给了一个绕过审计的开关。
			if !p.Enabled && p.SuspendedBy == domain.SuspendedQuota {
				if err := e.peers.SetEnabledByPolicy(ctx, p.ID, true, domain.SuspendedNone); err != nil {
					return res, err
				}
				p.Enabled = true
				p.SuspendedBy = domain.SuspendedNone
				res.Restored++
				e.publish(ctx, domain.Event{
					Type: domain.EventPeerRestored, PeerID: p.ID, PeerName: p.Name,
					Message:    "配额周期已重置，Peer 自动恢复",
					OccurredAt: now,
				})
			}
			// 继续往下走：本轮刚清零的用量必然不超配额，但「已过期」和「即将到期」
			// 与用量无关，结算完仍然要判定。
		}

		switch {
		case cfg.QuotaEnforcement && p.TrafficQuota > 0 && p.QuotaPeriod != domain.QuotaPeriodNever &&
			p.QuotaUsed >= p.TrafficQuota && p.Enabled:
			if err := e.peers.SetEnabledByPolicy(ctx, p.ID, false, domain.SuspendedQuota); err != nil {
				return res, err
			}
			res.Suspended++
			e.auditWrite(ctx, domain.AuditPeerUpdate, p.ID, p.Name,
				fmt.Sprintf("traffic quota exceeded: used=%d quota=%d", p.QuotaUsed, p.TrafficQuota))
			e.publish(ctx, domain.Event{
				Type: domain.EventPeerQuota, PeerID: p.ID, PeerName: p.Name,
				Message:    fmt.Sprintf("流量配额已用尽（%s / %s）", humanBytes(p.QuotaUsed), humanBytes(p.TrafficQuota)),
				OccurredAt: now,
			})

		case cfg.ExpiryEnforcement && p.ExpiresAt != nil && !p.ExpiresAt.After(now) && p.Enabled:
			if err := e.peers.SetEnabledByPolicy(ctx, p.ID, false, domain.SuspendedExpired); err != nil {
				return res, err
			}
			res.Suspended++
			e.auditWrite(ctx, domain.AuditPeerUpdate, p.ID, p.Name, "peer expired")
			e.publish(ctx, domain.Event{
				Type: domain.EventPeerExpired, PeerID: p.ID, PeerName: p.Name,
				Message: "有效期已过，Peer 已停用", OccurredAt: now,
			})

		case cfg.ExpiryWarnDays > 0 && p.ExpiresAt != nil && p.Enabled &&
			p.ExpiresAt.After(now) && p.ExpiresAt.Before(now.AddDate(0, 0, cfg.ExpiryWarnDays)):
			// 预警每天最多一次：审计表按 detail 去重代价高，这里用「自然日」粒度在内存里抑制。
			if !e.warnedToday(p.ID, now) {
				e.markWarned(p.ID, now)
				res.Warned++
				e.publish(ctx, domain.Event{
					Type: domain.EventPeerExpiring, PeerID: p.ID, PeerName: p.Name,
					Message:    fmt.Sprintf("将在 %s 后到期", p.ExpiresAt.Format("2006-01-02")),
					OccurredAt: now,
				})
			}
		}
	}
	return res, nil
}

// Start 运行策略循环，直到 ctx 取消。
func (e *Engine) Start(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	e.log.Info("policy engine started", "interval", interval)
	for {
		select {
		case <-ctx.Done():
			e.log.Info("policy engine stopped")
			return
		case <-ticker.C:
			res, err := e.Run(ctx, time.Now())
			if err != nil {
				e.log.Error("policy run failed", "error", err)
				continue
			}
			if res.Suspended > 0 || res.Restored > 0 {
				e.log.Info("policy applied",
					"suspended", res.Suspended, "restored", res.Restored, "quotaReset", res.ResetQuota)
				if e.reconcile != nil {
					e.reconcile()
				}
			}
		}
	}
}

// warnedKey 是预警抑制的键。
type warnedKey struct {
	peerID int64
	day    string
}

func (e *Engine) warnedToday(peerID int64, now time.Time) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	_, ok := e.warned[warnedKey{peerID, now.Format("2006-01-02")}]
	return ok
}

func (e *Engine) markWarned(peerID int64, now time.Time) {
	e.mu.Lock()
	defer e.mu.Unlock()
	day := now.Format("2006-01-02")
	// 只保留当天的记录，避免无限增长。
	for k := range e.warned {
		if k.day != day {
			delete(e.warned, k)
		}
	}
	e.warned[warnedKey{peerID, day}] = struct{}{}
}

func (e *Engine) publish(ctx context.Context, ev domain.Event) {
	if e.publisher == nil {
		return
	}
	e.publisher.Publish(ctx, ev)
}

func (e *Engine) auditWrite(ctx context.Context, action domain.AuditAction, peerID int64, name, detail string) {
	if e.audit == nil {
		return
	}
	_ = e.audit.Write(ctx, &domain.AuditEvent{
		ActorName: "policy-engine", Action: action, Resource: "peer",
		ResourceID: fmt.Sprint(peerID), Detail: fmt.Sprintf("%s: %s", name, detail),
		Result: domain.AuditSuccess,
	})
}

// settlePeriod 在周期起点前进时清零用量，返回本轮是否真的做了清零结算。
//
// 判据是「当前周期起点晚于库里记录的已结算起点」，而不是「两者不相等」。
// 用「不相等」会在起点倒退时误结算，而起点倒退在生产里真的会发生：
//   - 宿主机时钟被 NTP 步进回拨、虚拟机从快照恢复；
//   - 容器时区被改（compose 里的 TZ 是可改的运维参数），PeriodStart 按新
//     时区算出来的值会比库里记录的小若干小时。
//
// 这两种情况下若照旧结算，全站 Peer 会在同一秒内被清零用量并自动恢复——
// 超配额停用形同虚设，用户还凭空多拿一份额度。
//
// 起点相同时（同一周期内重复判定，或漏跑一次后补跑）返回 false：
//   - 漏跑一次判定（进程重启、数据库繁忙、调度抖动）不会永久漏掉整个周期，
//     下一次判定看到起点前进就会补上；
//   - 同一周期内重复跑一百次，也只有第一次会真正写库。
//
// quota_period_start 为 0 表示「从未观测过这个 Peer 的周期」，走认领而不是结算：
// 此时 quota_used_bytes 里的数字属于当前周期（新建的 Peer，或从没有该列的旧版本
// 升级而来），把它清零等于凭空送一份免费额度。
//
// quotaPeriod=never 直接跳过：它没有周期结算语义。
func settlePeriod(ctx context.Context, peers *store.PeerRepo, p *store.PolicyPeer,
	now time.Time) (bool, error) {
	start := PeriodStart(p.QuotaPeriod, now)
	if start.IsZero() {
		return false, nil
	}
	if p.QuotaPeriodStart == 0 {
		return false, peers.AdoptQuotaPeriod(ctx, p.ID, start.Unix())
	}
	// 起点没有前进（含倒退）：本轮不结算，也不改写已记录的起点。
	if start.Unix() <= p.QuotaPeriodStart {
		return false, nil
	}
	if err := peers.ResetQuotaUsage(ctx, p.ID, start.Unix()); err != nil {
		return false, err
	}
	return true, nil
}

// PeriodStart 返回当前配额周期的起点。never 返回零值表示不参与周期结算。
func PeriodStart(p domain.QuotaPeriod, now time.Time) time.Time {
	y, m, d := now.Date()
	switch p {
	case domain.QuotaPeriodDaily:
		return time.Date(y, m, d, 0, 0, 0, 0, now.Location())
	case domain.QuotaPeriodWeekly:
		// ISO 周：以周一为一周起点。
		offset := (int(now.Weekday()) + 6) % 7
		day := time.Date(y, m, d, 0, 0, 0, 0, now.Location()).AddDate(0, 0, -offset)
		return day
	case domain.QuotaPeriodMonthly:
		return time.Date(y, m, 1, 0, 0, 0, 0, now.Location())
	default:
		return time.Time{}
	}
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit && exp < 4; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTP"[exp])
}
