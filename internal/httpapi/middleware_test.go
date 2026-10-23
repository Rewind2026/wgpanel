package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Rewind2026/wgpanel/internal/authn"
	"github.com/Rewind2026/wgpanel/internal/domain"
)

// recordingAuth 记录中间件传进来的凭据。
type recordingAuth struct {
	gotAccess string
	gotIP     string
	principal authn.Principal
}

func (r *recordingAuth) Authenticate(_ context.Context, accessToken, ip string) (*authn.Principal, error) {
	r.gotAccess, r.gotIP = accessToken, ip
	p := r.principal
	return &p, nil
}

// 带 Bearer 时应放行，并把令牌与来源 IP 一起交给 Authenticate。
func TestAuthnMiddlewareAcceptsBearerToken(t *testing.T) {
	rec := &recordingAuth{principal: authn.Principal{UserID: 1, Username: "ops", Role: domain.RoleOwner}}

	var reached bool
	h := authnMiddleware(rec, false)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/peers", nil)
	req.Header.Set("Authorization", "Bearer jwt-value")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("带 Bearer 应放行，实际 %d：%s", w.Code, w.Body.String())
	}
	if !reached {
		t.Fatal("请求未到达下游 handler")
	}
	if rec.gotAccess != "jwt-value" {
		t.Fatalf("Bearer 未透传给 Authenticate，实际 %q", rec.gotAccess)
	}
}

// 无凭据时须 401，且不应白跑一次 Authenticate。
func TestAuthnMiddlewareRejectsMissingCredentials(t *testing.T) {
	called := false
	h := authnMiddleware(authFunc(func(context.Context, string, string) (*authn.Principal, error) {
		called = true
		return nil, nil
	}), false)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("无凭据不应到达下游")
	}))

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/peers", nil))

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("无凭据应 401，实际 %d", w.Code)
	}
	if called {
		t.Fatal("无凭据时不应调用 Authenticate")
	}
}

// 鉴权失败时不能把请求放给下游。
func TestAuthnMiddlewareRejectsBadToken(t *testing.T) {
	h := authnMiddleware(authFunc(func(context.Context, string, string) (*authn.Principal, error) {
		return nil, domain.ErrUnauthorized("invalid access token")
	}), false)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("凭据无效不应到达下游")
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/peers", nil)
	req.Header.Set("Authorization", "Bearer expired-value")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("无效令牌应 401，实际 %d", w.Code)
	}
}

// clientIP 必须把 RemoteAddr 的端口剥掉，只留 IP 部分。
func TestClientIPStripsPort(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/peers", nil)
	req.RemoteAddr = "203.0.113.7:54321"

	if got := clientIP(req, false); got != "203.0.113.7" {
		t.Fatalf("应剥掉端口得到 IP，实际 %q", got)
	}
}

// authFunc 把函数适配成 Authenticator。
type authFunc func(context.Context, string, string) (*authn.Principal, error)

func (f authFunc) Authenticate(ctx context.Context, access, ip string) (*authn.Principal, error) {
	return f(ctx, access, ip)
}
