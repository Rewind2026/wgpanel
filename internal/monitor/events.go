package monitor

import (
	"context"
	"time"

	"github.com/Rewind2026/wgpanel/internal/domain"
)

// UsageSink 是采集结果的额外消费者：流量配额记账。
//
// 做成接口而不是把 PeerRepo 塞进 Collector，是为了让采集循环的测试
// 不需要数据库——采集器只负责算出「这一拍每个 Peer 用了多少字节」。
type UsageSink interface {
	// AddQuotaUsage 把字节增量累加到 Peer 的周期用量，返回累加后的总量。
	AddQuotaUsage(ctx context.Context, peerID int64, delta int64) (int64, error)
	// TouchLastSeen 记录 Peer 最近活跃时间。
	TouchLastSeen(ctx context.Context, peerID int64, ts int64) error
}

// EventPublisher 广播领域事件。
type EventPublisher interface {
	Publish(ctx context.Context, ev domain.Event)
}

// onlineWindow 是「在线」的判定窗口。
//
// 取 3 分钟：WireGuard 在 keepalive 25s 时握手非常频繁，3 分钟足够容忍
// 偶发丢包；再长就会让真正的离线迟迟不被发现。
const onlineWindow = 3 * time.Minute

// detectPresence 比对上一拍的在线集合，广播上下线事件。
//
// 在线判定用「最近握手是否落在窗口内」，而不是直接取内核的 Online 字段：
// 内核对一个已断开但 keepalive 尚未超时的 Peer 仍会报在线，
// 直接用它会让通知在边界上反复横跳。
func (c *Collector) detectPresence(ctx context.Context, peers []domain.PeerStats, now time.Time) {
	if c.events == nil {
		return
	}
	next := make(map[int64]bool, len(peers))
	for i := range peers {
		next[peers[i].PeerID] = peers[i].LastHandshake.After(now.Add(-onlineWindow))
	}

	c.onlineMu.Lock()
	prev := c.online
	c.online = next
	c.onlineMu.Unlock()

	for i := range peers {
		p := peers[i]
		was, seen := prev[p.PeerID]
		if !next[p.PeerID] {
			if seen && was {
				c.events.Publish(ctx, domain.Event{
					Type: domain.EventPeerOffline, PeerID: p.PeerID,
					Message: "客户端已离线", OccurredAt: now,
				})
			}
			continue
		}
		if !seen || !was {
			c.events.Publish(ctx, domain.Event{
				Type: domain.EventPeerOnline, PeerID: p.PeerID,
				Message: "客户端已上线", OccurredAt: now,
			})
		}
	}
	for id, was := range prev {
		if was && !next[id] {
			// 上一拍在线、这一拍连采样结果里都没有：Peer 已被删除或停用。
			c.events.Publish(ctx, domain.Event{
				Type: domain.EventPeerOffline, PeerID: id,
				Message: "客户端已下线", OccurredAt: now,
			})
		}
	}
}

// accountQuota 把本拍的字节增量累加进 Peer 的周期用量。
//
// 只对「这一拍确实产生了流量且设了配额」的 Peer 写库：
// 每秒给全量 Peer 写一次会把 SQLite 写连接占满，而这些写全是零增量。
func (c *Collector) accountQuota(ctx context.Context, deltas map[int64]int64) {
	if c.usage == nil || len(deltas) == 0 {
		return
	}
	for id, delta := range deltas {
		if delta <= 0 {
			continue
		}
		if _, err := c.usage.AddQuotaUsage(ctx, id, delta); err != nil {
			c.log.Debug("add quota usage failed", "peerId", id, "error", err)
		}
	}
}

// markActive 批量刷新最近活跃时间。
func (c *Collector) markActive(ctx context.Context, ids []int64, ts int64) {
	if c.usage == nil {
		return
	}
	for _, id := range ids {
		if err := c.usage.TouchLastSeen(ctx, id, ts); err != nil {
			c.log.Debug("touch last seen failed", "peerId", id, "error", err)
			return
		}
	}
}
