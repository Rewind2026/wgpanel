// Package eventbus 提供进程内事件分发，供 SSE 实时推送使用。
//
// 为什么不用 channel-of-channels 而不引入消息中间件：单机单进程部署，
// 事件量与订阅者数量都极小；引入外部依赖会让「一条命令部署」这个核心卖点失效。
// 代价是进程重启会丢事件——对 Peer 上下线这类瞬时状态可以接受，
// 需要留痕的写操作由 audit_log 承担。
package eventbus

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/Rewind2026/wgpanel/internal/domain"
)

// Bus 是事件总线。
type Bus struct {
	log    *slog.Logger
	mu     sync.RWMutex
	subs   map[int]chan domain.Event
	nextID int
	buffer int
}

// New 构造事件总线。buffer 决定慢订阅者能积压多少条事件。
func New(log *slog.Logger, buffer int) *Bus {
	if buffer < 1 {
		buffer = 16
	}
	return &Bus{
		log:    log,
		subs:   map[int]chan domain.Event{},
		buffer: buffer,
	}
}

// Subscribe 注册订阅者，返回事件通道与取消函数。
func (b *Bus) Subscribe() (<-chan domain.Event, func()) {
	ch := make(chan domain.Event, b.buffer)

	b.mu.Lock()
	b.nextID++
	id := b.nextID
	b.subs[id] = ch
	b.mu.Unlock()

	var once sync.Once
	return ch, func() {
		once.Do(func() {
			b.mu.Lock()
			if c, ok := b.subs[id]; ok {
				delete(b.subs, id)
				close(c)
			}
			b.mu.Unlock()
		})
	}
}

// Publish 广播事件。慢订阅者的通道写满时丢弃该条事件而不是阻塞发布方：
// 一个卡住的浏览器标签页不应拖住采集协程。
func (b *Bus) Publish(_ context.Context, ev domain.Event) {
	if ev.OccurredAt.IsZero() {
		ev.OccurredAt = time.Now()
	}

	b.mu.RLock()
	defer b.mu.RUnlock()
	for id, ch := range b.subs {
		select {
		case ch <- ev:
		default:
			b.log.Debug("event dropped for slow subscriber", "event", ev.Type, "subscriber", id)
		}
	}
}

// Subscribers 返回当前订阅者数量，用于健康检查与测试。
func (b *Bus) Subscribers() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.subs)
}
