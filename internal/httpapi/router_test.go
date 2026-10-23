package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/Rewind2026/wgpanel/internal/authn"
	"github.com/Rewind2026/wgpanel/internal/domain"
	"github.com/Rewind2026/wgpanel/internal/eventbus"
	"github.com/Rewind2026/wgpanel/internal/monitor"
)

// SSE 路由的鉴权路径必须独立于通用鉴权中间件。
//
// 真机上暴露过的缺陷：这条路由一度注册在 /api/v1 子路由内部，
// 路径被二次拼接而匹配不到，请求落回通用鉴权组并报
// missing access token——票据明明已经换到手了。
// 这组用例把「路由注册位置」和「凭据语义」一起钉住。
func TestStreamRouteUsesTicketNotBearer(t *testing.T) {
	s := newStreamTestServer(t)

	t.Run("无票据被拒", func(t *testing.T) {
		if rec := serveStream(t, s.Handler(), "/api/v1/metrics/stream"); rec.Code != http.StatusUnauthorized {
			t.Fatalf("无票据应 401，实际 %d", rec.Code)
		}
	})

	t.Run("非法票据被拒", func(t *testing.T) {
		if rec := serveStream(t, s.Handler(), "/api/v1/metrics/stream?ticket=garbage"); rec.Code != http.StatusUnauthorized {
			t.Fatalf("非法票据应 401，实际 %d", rec.Code)
		}
	})

	t.Run("访问令牌不能当票据用", func(t *testing.T) {
		// JWT 拼 URL 会落进反向代理访问日志与浏览器历史，
		// 因此后端绝不能接受它作为 SSE 凭据。
		rec := serveStream(t, s.Handler(), "/api/v1/metrics/stream?token=some.jwt.value")
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("JWT 不应被当作 SSE 凭据，实际 %d", rec.Code)
		}
	})
}

func TestStreamTicketIsSingleUseOverHTTP(t *testing.T) {
	s := newStreamTestServer(t)
	raw, err := s.tickets.Issue(authn.Principal{UserID: 1, Username: "ops", Role: domain.RoleAdmin})
	if err != nil {
		t.Fatalf("issue ticket: %v", err)
	}

	// 首次消费会进入 SSE 长循环；serveStream 取消 context 让它立即返回。
	if rec := serveStream(t, s.Handler(), "/api/v1/metrics/stream?ticket="+raw); rec.Code == http.StatusUnauthorized {
		t.Fatal("首次消费票据不应被拒")
	}

	rec := serveStream(t, s.Handler(), "/api/v1/metrics/stream?ticket="+raw)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("已消费的票据重放应 401，实际 %d", rec.Code)
	}
}

// 票据解决的是「凭据怎么带上」，不是「有没有权限看」。
// 这条用例保证将来不会有人顺手把 RBAC 检查删掉换便利。
func TestStreamTicketStillEnforcesRBAC(t *testing.T) {
	s := newStreamTestServer(t)
	raw, err := s.tickets.Issue(authn.Principal{UserID: 9, Username: "nobody"})
	if err != nil {
		t.Fatalf("issue ticket: %v", err)
	}
	rec := serveStream(t, s.Handler(), "/api/v1/metrics/stream?ticket="+raw)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("无角色主体应 403，实际 %d", rec.Code)
	}
}

// newStreamTestServer 构造只够跑 SSE 鉴权的最小 Server。
//
// Collector 不启动 Run()：订阅能注册成功、但不会有快照推送，
// 因此测试完全由 context 取消驱动结束，不依赖任何时间假设。
func newStreamTestServer(t *testing.T) *Server {
	t.Helper()
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	return &Server{
		log:       log,
		tickets:   authn.NewStreamTicketStore(),
		collector: monitor.New(nil, nil, nil, time.Hour, log),
		events:    eventbus.New(log, 8),
	}
}

// serveStream 发起一次 SSE 请求并在拿到响应头后立刻断开。
func serveStream(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, path, nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		h.ServeHTTP(rec, req)
		close(done)
	}()
	// 鉴权失败会立即返回；鉴权通过则进入长连接，靠 cancel 收尾。
	select {
	case <-done:
	case <-time.After(2 * time.Second):
	}
	cancel()
	<-done
	return rec
}
