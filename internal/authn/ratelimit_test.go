package authn

import (
	"testing"
	"time"
)

func TestLoginLimiterBlocksAfterMaxAttempts(t *testing.T) {
	limiter := NewLoginLimiter(3, time.Minute)

	for i := 0; i < 3; i++ {
		if !limiter.Allow("ip:1.2.3.4") {
			t.Fatalf("attempt %d should be allowed", i+1)
		}
		limiter.Fail("ip:1.2.3.4")
	}
	if limiter.Allow("ip:1.2.3.4") {
		t.Fatal("limiter should block after 3 failures")
	}
	if !limiter.Allow("ip:5.6.7.8") {
		t.Fatal("other keys must not be affected")
	}
}

func TestLoginLimiterResetClearsCounter(t *testing.T) {
	limiter := NewLoginLimiter(2, time.Minute)
	limiter.Fail("user:admin")
	limiter.Fail("user:admin")
	if limiter.Allow("user:admin") {
		t.Fatal("expected lockout")
	}
	limiter.Reset("user:admin")
	if !limiter.Allow("user:admin") {
		t.Fatal("reset should restore attempts")
	}
}

func TestLoginLimiterExpiresWindow(t *testing.T) {
	limiter := NewLoginLimiter(1, 20*time.Millisecond)
	limiter.Fail("user:admin")
	if limiter.Allow("user:admin") {
		t.Fatal("expected lockout inside window")
	}
	time.Sleep(40 * time.Millisecond)
	if !limiter.Allow("user:admin") {
		t.Fatal("counter should expire after window")
	}
}
