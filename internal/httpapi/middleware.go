package httpapi

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/Rewind2026/wgpanel/internal/authn"
	"github.com/Rewind2026/wgpanel/internal/domain"
	"github.com/Rewind2026/wgpanel/internal/reqmeta"
)

// ctxKeyPrincipal 是 principal 在 context 中的键。
type ctxKeyPrincipal struct{}

// principalFrom 提取请求主体。
func principalFrom(ctx context.Context) *authn.Principal {
	p, _ := ctx.Value(ctxKeyPrincipal{}).(*authn.Principal)
	return p
}

// withPrincipal 注入请求主体。
func withPrincipal(ctx context.Context, p *authn.Principal) context.Context {
	return context.WithValue(ctx, ctxKeyPrincipal{}, p)
}

// reqMetaMiddleware 把请求来源写进 context。
//
// 位置在整条中间件链的最前面：审计写入发生在 service 层，
// 那里拿不到 *http.Request，只能靠这里传递来源 IP 与 User-Agent。
// 放在这里而不是鉴权中间件里，是为了让登录接口（不需要登录）也带上。
func reqMetaMiddleware(trustProxy bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := reqmeta.With(r.Context(), reqmeta.Info{
				IP:        clientIP(r, trustProxy),
				UserAgent: r.UserAgent(),
			})
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// clientIP 解析真实客户端 IP，信任反向代理时读取 X-Forwarded-For 首项。
func clientIP(r *http.Request, trustProxy bool) string {
	if trustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			if first, _, ok := strings.Cut(xff, ","); ok {
				return strings.TrimSpace(first)
			}
			return strings.TrimSpace(xff)
		}
		if xr := r.Header.Get("X-Real-IP"); xr != "" {
			return strings.TrimSpace(xr)
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// authnMiddleware 校验访问令牌，写入请求主体。
func authnMiddleware(svc Authenticator, trustProxy bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := bearerToken(r)
			if token == "" {
				respondError(w, slog.Default(), domain.ErrUnauthorized("missing access token"))
				return
			}
			principal, err := svc.Authenticate(r.Context(), token, clientIP(r, trustProxy))
			if err != nil {
				respondError(w, slog.Default(), err)
				return
			}
			next.ServeHTTP(w, r.WithContext(withPrincipal(r.Context(), principal)))
		})
	}
}

// bearerToken 从 Authorization 头或 access_token 查询参数读取令牌。
func bearerToken(r *http.Request) string {
	if h := r.Header.Get("Authorization"); h != "" {
		if token, ok := strings.CutPrefix(h, "Bearer "); ok {
			return strings.TrimSpace(token)
		}
	}
	return ""
}

// streamAuthnMiddleware 校验 SSE 票据。
//
// 与 authnMiddleware 的区别只有「凭据从哪来」：这里只认 URL 上的
// ?ticket=，因为浏览器 EventSource 除了 URL 什么都带不了。
// 身份仍然要还原成 Principal 并过 RBAC——票据解决的是传输方式，
// 不是权限。
func (s *Server) streamAuthnMiddleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p, ok := s.tickets.Redeem(r.URL.Query().Get("ticket"))
			if !ok {
				respondError(w, slog.Default(), domain.ErrUnauthorized("invalid or expired stream ticket"))
				return
			}
			if !p.Can(domain.PermMetricsRead) {
				respondError(w, slog.Default(), domain.ErrForbidden("permission %s is required", domain.PermMetricsRead))
				return
			}
			next.ServeHTTP(w, r.WithContext(withPrincipal(r.Context(), &p)))
		})
	}
}

// requirePerm 生成校验单个权限点的中间件。
func requirePerm(perm domain.Permission, log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p := principalFrom(r.Context())
			if !p.Can(perm) {
				respondError(w, log, domain.ErrForbidden("permission %s is required", perm))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// securityHeaders 添加基础安全响应头。
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		// SPA 产物全部同源且无内联脚本，样式因 Tailwind 运行时变量需要 unsafe-inline。
		//
		// font-src 必须显式给 data:：@fontsource 的 woff2 会被打包器内联成
		// data: URI，而 default-src 'self' 回落到 font-src 时会把它整条拦掉。
		// 表现是页面照常渲染，但展示体（Chakra Petch）静默回落成系统字体——
		// 控制台一堆 CSP 违规，界面看着「差不多」但设计意图没落地。
		h.Set("Content-Security-Policy", strings.Join([]string{
			"default-src 'self'",
			"base-uri 'self'",
			"object-src 'none'",
			"frame-ancestors 'none'",
			"img-src 'self' data:",
			"font-src 'self' data:",
			"style-src 'self' 'unsafe-inline'",
			"script-src 'self'",
			"connect-src 'self'",
			"form-action 'self'",
		}, "; "))
		next.ServeHTTP(w, r)
	})
}

// isStreamingPath 判断是否为长连接端点。
func isStreamingPath(p string) bool { return p == "/api/v1/metrics/stream" }

// timeoutMiddleware 为请求设置处理超时，但放行流式端点：
// SSE 需要保持长连接，统一超时会在固定时间点被 503 掐断。
func timeoutMiddleware(d time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		guarded := middleware.Timeout(d)(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if isStreamingPath(r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}
			guarded.ServeHTTP(w, r)
		})
	}
}

// statusRecorder 记录响应状态码，供日志中间件使用。
type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (w *statusRecorder) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusRecorder) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += n
	return n, err
}

// Flush 实现 http.Flusher，保障 SSE 可用。
func (w *statusRecorder) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// loggerMiddleware 输出结构化访问日志，跳过高频轮询端点以免噪声淹没。
func loggerMiddleware(log *slog.Logger, trustProxy bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if isQuietPath(r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}
			start := time.Now()
			rec := &statusRecorder{ResponseWriter: w}
			next.ServeHTTP(rec, r)
			log.Info("request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", rec.status,
				"bytes", rec.bytes,
				"duration", time.Since(start).String(),
				"ip", clientIP(r, trustProxy),
				"reqId", middleware.GetReqID(r.Context()),
			)
		})
	}
}

// isQuietPath 判断是否为无需记录的高频路径。
func isQuietPath(path string) bool {
	switch path {
	case "/healthz", "/readyz":
		return true
	}
	return false
}
