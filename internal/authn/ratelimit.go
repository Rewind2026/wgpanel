package authn

import (
	"sync"
	"time"
)

// LoginLimiter 是按 key（IP 或用户名）统计失败次数的滑动窗口限流器。
type LoginLimiter struct {
	mu       sync.Mutex
	attempts map[string][]time.Time
	max      int
	window   time.Duration
}

// NewLoginLimiter 构造限流器。max 次失败后在 window 内拒绝登录。
func NewLoginLimiter(max int, window time.Duration) *LoginLimiter {
	return &LoginLimiter{
		attempts: make(map[string][]time.Time),
		max:      max,
		window:   window,
	}
}

// Allow 判断当前是否允许尝试。
func (l *LoginLimiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.prune(key, time.Now())
	return len(l.attempts[key]) < l.max
}

// Fail 记录一次失败。
func (l *LoginLimiter) Fail(key string) {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	l.prune(key, now)
	l.attempts[key] = append(l.attempts[key], now)
}

// Reset 清空某 key 的失败计数（登录成功时调用）。
func (l *LoginLimiter) Reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.attempts, key)
}

// Remaining 返回窗口内剩余可用尝试次数。
func (l *LoginLimiter) Remaining(key string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.prune(key, time.Now())
	n := l.max - len(l.attempts[key])
	if n < 0 {
		return 0
	}
	return n
}

// prune 丢弃窗口外的记录。调用方必须持有锁。
func (l *LoginLimiter) prune(key string, now time.Time) {
	cutoff := now.Add(-l.window)
	kept := l.attempts[key][:0]
	for _, ts := range l.attempts[key] {
		if ts.After(cutoff) {
			kept = append(kept, ts)
		}
	}
	if len(kept) == 0 {
		delete(l.attempts, key)
		return
	}
	l.attempts[key] = kept
}
