package authn

import (
	"fmt"
	"strconv"
	"sync"
	"time"
)

// StreamTicketTTL 是 SSE 一次性票据的有效期。
//
// 取 60 秒不是为了「短」，而是为了刚好够用：换票是一次 POST，
// 浏览器拿到后立刻建 EventSource，1 秒内完成。给得太短会让
// 反向代理或慢网络下的重连频繁失败；给得太长则意味着一条
// 访问日志里的截图就能重放一次监控流。
const StreamTicketTTL = 60 * time.Second

// StreamTicketStore 是进程内的 SSE 票据表。
//
// 为什么不让 EventSource 直接带 JWT：浏览器 EventSource 构造器不接受
// 自定义请求头（这是规范限制，不是实现偷懒），于是只剩三条路——
// 把 JWT 塞进 URL、塞进 Cookie、或换一张短期票据。
// 塞 URL 会让令牌出现在反向代理访问日志和浏览器历史里；
// 塞 Cookie 要额外处理 CSRF 与 SameSite，并且把「会话凭据」的
// 攻击面扩大到每一条请求。票据把这两类风险都挡在门外：URL 里
// 出现的只是一个 60 秒、只能用一次、泄露后无法提权的随机串。
//
// 票据存在内存而不是数据库：它是纯易失的连接凭据，
// 进程重启后让客户端重新换票即可，落库反而增加清理负担。
type StreamTicketStore struct {
	mu      sync.Mutex
	tickets map[string]streamTicket
	ttl     time.Duration
	now     func() time.Time
}

type streamTicket struct {
	raw       string
	principal Principal
	expiresAt time.Time
}

// NewStreamTicketStore 构造票据表。
func NewStreamTicketStore() *StreamTicketStore {
	return &StreamTicketStore{
		tickets: map[string]streamTicket{},
		ttl:     StreamTicketTTL,
		now:     time.Now,
	}
}

// Issue 为一个主体签发票据，并把该主体绑进票据里。
//
// 票据必须带身份而不只是「通过」标记：SSE 路由靠票据还原
// Principal 后还要做权限判定，凭票放行等于给所有登录用户
// 开放监控数据。
//
// 同一主体重复换票会覆盖旧票，避免一个人开多个监控页
// 就在表里堆几十条永远等不到过期的记录。
func (s *StreamTicketStore) Issue(p Principal) (string, error) {
	raw, err := RandomToken(32)
	if err != nil {
		return "", fmt.Errorf("authn: issue stream ticket: %w", err)
	}
	key := strconv.FormatInt(p.UserID, 10)

	s.mu.Lock()
	defer s.mu.Unlock()
	s.evictLocked()
	s.tickets[key] = streamTicket{
		raw: raw, principal: p, expiresAt: s.now().Add(s.ttl),
	}
	return raw, nil
}

// Redeem 校验并消费一张票据，返回其绑定的身份。
//
// 一次性消费是关键：即使票据在 URL 里被日志记录下来，
// 事后重放也只能换到一条已经被用掉的连接。
func (s *StreamTicketStore) Redeem(raw string) (Principal, bool) {
	if raw == "" {
		return Principal{}, false
	}
	now := s.now()

	s.mu.Lock()
	defer s.mu.Unlock()
	for k, t := range s.tickets {
		if now.After(t.expiresAt) {
			delete(s.tickets, k)
			continue
		}
		if t.raw == raw {
			delete(s.tickets, k)
			return t.principal, true
		}
	}
	return Principal{}, false
}

// evictLocked 清掉过期票据，调用方必须持有锁。
func (s *StreamTicketStore) evictLocked() {
	now := s.now()
	for k, t := range s.tickets {
		if now.After(t.expiresAt) {
			delete(s.tickets, k)
		}
	}
}
