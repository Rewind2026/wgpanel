package eventbus

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/Rewind2026/wgpanel/internal/domain"
)

func newBus(buffer int) *Bus {
	return New(slog.New(slog.NewTextHandler(io.Discard, nil)), buffer)
}

func ev(t domain.EventType) domain.Event {
	return domain.Event{Type: t, PeerID: 1, PeerName: "laptop", Message: "hello"}
}

func TestPublishReachesEverySubscriber(t *testing.T) {
	b := newBus(4)
	ch1, cancel1 := b.Subscribe()
	defer cancel1()
	ch2, cancel2 := b.Subscribe()
	defer cancel2()
	if b.Subscribers() != 2 {
		t.Fatalf("订阅者数应为 2，实际 %d", b.Subscribers())
	}

	b.Publish(context.Background(), ev(domain.EventPeerOnline))

	for i, ch := range []<-chan domain.Event{ch1, ch2} {
		select {
		case got := <-ch:
			if got.Type != domain.EventPeerOnline {
				t.Errorf("订阅者 %d 收到 %q，期望 peer.up", i, got.Type)
			}
			if got.OccurredAt.IsZero() {
				t.Errorf("订阅者 %d 收到的事件缺少 OccurredAt", i)
			}
		default:
			t.Fatalf("订阅者 %d 没收到事件", i)
		}
	}
}

// 慢订阅者（浏览器标签页卡住、网络断开）不能拖住发布方。
// SSE 的写入是同步的，一旦在这里阻塞，采集协程就会跟着停摆。
func TestSlowSubscriberDropsInsteadOfBlocking(t *testing.T) {
	b := newBus(2)
	slow, cancelSlow := b.Subscribe()
	defer cancelSlow()
	fast, cancelFast := b.Subscribe()
	defer cancelFast()

	const total = 50
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range total {
			b.Publish(context.Background(), ev(domain.EventPeerOnline))
		}
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("发布方被慢订阅者阻塞")
	}

	// 快订阅者没有被读取，buffer 为 2，因此也只收到 2 条——
	// 关键在于发布方 50 条全部发完，没有丢在发布路径上。
	if got := len(fast); got > 2 {
		t.Errorf("订阅通道不应超过 buffer，实际 %d", got)
	}
	if got := len(slow); got > 2 {
		t.Errorf("慢订阅者积压不应超过 buffer，实际 %d", got)
	}
}

func TestSubscribeCancelIsIdempotentAndClosesChannel(t *testing.T) {
	b := newBus(2)
	ch, cancel := b.Subscribe()

	cancel()
	cancel() // 重复取消必须安全，否则会重复 close 同一个 channel 而 panic
	if b.Subscribers() != 0 {
		t.Errorf("取消后订阅者数应为 0，实际 %d", b.Subscribers())
	}
	if _, open := <-ch; open {
		t.Error("取消后通道应被关闭")
	}

	// 取消后再发布不应 panic（向已关闭通道发送会 panic）。
	b.Publish(context.Background(), ev(domain.EventPeerOnline))
}

func TestPublishIsConcurrencySafe(t *testing.T) {
	b := newBus(64)
	var wg sync.WaitGroup

	const subs = 8
	chans := make([]<-chan domain.Event, subs)
	cancels := make([]func(), subs)
	for i := range chans {
		ch, cancel := b.Subscribe()
		cancels[i] = cancel
		chans[i] = ch
	}
	defer func() {
		for _, c := range cancels {
			c()
		}
	}()

	const publishers = 8
	const perPublisher = 25
	for range publishers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range perPublisher {
				b.Publish(context.Background(), ev(domain.EventPeerOnline))
			}
		}()
	}
	// 发布进行中取消订阅，检验 Publish 与 Subscribe 的并发正确性：
	// 向已关闭通道发送会 panic，这里不出 panic 才是这条测试的目的。
	wg.Add(1)
	go func() {
		defer wg.Done()
		time.Sleep(2 * time.Millisecond)
		for _, c := range cancels {
			c()
		}
	}()

	wg.Wait()

	// 订阅期间投递过的每个订阅者都应至少留下一条——全空说明
	// 并发下事件被整体吞掉，而不是按设计对慢订阅者丢弃。
	// 具体条数不定：通道满时 Publish 会主动丢弃，这是既定行为。
	for i, ch := range chans {
		if len(ch) == 0 {
			t.Errorf("订阅者 %d 一条都没收到，事件在并发下被整体吞掉了", i)
		}
	}
}
