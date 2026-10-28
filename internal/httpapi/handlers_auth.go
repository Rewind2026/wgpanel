package httpapi

import (
	"net/http"
	"time"

	"github.com/Rewind2026/wgpanel/internal/authn"
	"github.com/Rewind2026/wgpanel/internal/domain"
	"github.com/Rewind2026/wgpanel/internal/netdriver"
	"github.com/Rewind2026/wgpanel/internal/service"
)

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// handleLogin 处理登录请求。
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := decodeJSON(r, &req); err != nil {
		respondError(w, s.log, err)
		return
	}
	if req.Username == "" || req.Password == "" {
		respondError(w, s.log, domain.ErrInvalid("username and password are required"))
		return
	}
	session, err := s.auth.Login(r.Context(), service.LoginRequest{
		Username:  req.Username,
		Password:  req.Password,
		IP:        clientIP(r, s.trustProxy),
		UserAgent: r.UserAgent(),
	})
	if err != nil {
		respondError(w, s.log, err)
		return
	}
	s.setRefreshCookie(w, session.RefreshToken)
	respondJSON(w, http.StatusOK, map[string]any{
		"accessToken": session.AccessToken,
		"expiresIn":   session.ExpiresIn,
		"user":        session.User,
	})
}

// handleRefresh 轮换刷新令牌。
func (s *Server) handleRefresh(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie(refreshCookieName)
	if err != nil || cookie.Value == "" {
		respondError(w, s.log, domain.ErrUnauthorized("refresh token cookie is missing"))
		return
	}
	session, err := s.auth.Refresh(r.Context(), cookie.Value)
	if err != nil {
		s.clearRefreshCookie(w)
		respondError(w, s.log, err)
		return
	}
	s.setRefreshCookie(w, session.RefreshToken)
	respondJSON(w, http.StatusOK, map[string]any{
		"accessToken": session.AccessToken,
		"expiresIn":   session.ExpiresIn,
		"user":        session.User,
	})
}

// handleLogout 撤销会话。
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(refreshCookieName); err == nil {
		if err := s.auth.Logout(r.Context(), cookie.Value, principalFrom(r.Context())); err != nil {
			respondError(w, s.log, err)
			return
		}
	}
	s.clearRefreshCookie(w)
	respondJSON(w, http.StatusOK, map[string]any{"loggedOut": true})
}

// handleIssueStreamTicket 为 SSE 换一张一次性短期票据。
//
// 存在的唯一理由是浏览器 EventSource 不能设置请求头：
// 监控页要带身份连 SSE，就得先用一个能设置 Authorization 头的
// 请求，把身份换成 URL 里可携带的短期凭据。
func (s *Server) handleIssueStreamTicket(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	if p == nil {
		respondError(w, s.log, domain.ErrUnauthorized("not authenticated"))
		return
	}
	// 换票本身就要按权限拒绝，否则等于给无权限者一条「来换票」的旁路。
	if !p.Can(domain.PermMetricsRead) {
		respondError(w, s.log, domain.ErrForbidden("permission %s is required", domain.PermMetricsRead))
		return
	}
	raw, err := s.tickets.Issue(*p)
	if err != nil {
		respondError(w, s.log, err)
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{
		"ticket":    raw,
		"expiresIn": int(authn.StreamTicketTTL.Seconds()),
	})
}

// handleMe 返回当前登录用户。
// netMode 一并返回：侧栏与诊断页要按部署形态显示「Host/IPvlan/Macvlan/自管」，
// 复用这个每页必调的接口，前端不需要为了一行文案多发一次请求。
func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	if p == nil {
		respondError(w, s.log, domain.ErrUnauthorized("not authenticated"))
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{
		"userId":      p.UserID,
		"username":    p.Username,
		"role":        p.Role,
		"permissions": p.Role.Permissions(),
		"netMode":     string(s.netMode),
	})
}

// setRefreshCookie 下发 HttpOnly 刷新 Cookie。
func (s *Server) setRefreshCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     refreshCookieName,
		Value:    token,
		Path:     "/api/v1/auth",
		Expires:  time.Now().Add(s.cfg.RefreshTTL),
		MaxAge:   int(s.cfg.RefreshTTL.Seconds()),
		HttpOnly: true,
		Secure:   s.cookieSecure,
		SameSite: http.SameSiteStrictMode,
	})
}

// clearRefreshCookie 立即失效刷新 Cookie。
func (s *Server) clearRefreshCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     refreshCookieName,
		Value:    "",
		Path:     "/api/v1/auth",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   s.cookieSecure,
		SameSite: http.SameSiteStrictMode,
	})
}

// handleHealth 是存活探针，仅反映进程存活。
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	respondJSON(w, http.StatusOK, map[string]any{
		"status":  "ok",
		"version": s.version,
		"uptime":  time.Since(s.startedAt).Round(time.Second).String(),
	})
}

// handleReady 是就绪探针，反映数据库、内核驱动与网络下发的真实状态。
// 网络能力不可用时 WireGuard 控制面无法工作，因此与数据库同级参与 503 判定。
func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	checks := map[string]string{"database": "ok"}
	caps := map[string]bool{}
	code := http.StatusOK

	if err := s.store.Health(r.Context()); err != nil {
		checks["database"] = err.Error()
		code = http.StatusServiceUnavailable
	}
	if err := s.driver.Available(); err != nil {
		// 控制面不可用意味着接口根本建不起来：配置能读写，但没有任何流量能走。
		// 这种状态报 200 会让编排系统把一台废掉的实例继续留在负载后面，
		// 因此与数据库同级参与 503 判定。
		checks["wireguard"] = err.Error()
		code = http.StatusServiceUnavailable
	} else {
		checks["wireguard"] = "ok"
	}
	// host 模式下转发开关与防火墙归属宿主，面板不接管，
	// 因此不能因为「面板没开转发」就把实例判为不就绪。
	hostMode := s.netMode == netdriver.ModeHost
	if s.net != nil {
		c := s.net.Probe()
		caps = map[string]bool{
			"netlink":    c.Netlink,
			"tun":        c.TUNDevice,
			"iptables":   c.Iptables,
			"tc":         c.Tc,
			"forwarding": c.Forwarding,
			"hostMode":   hostMode,
		}
		if !c.Netlink {
			checks["netlink"] = "unavailable: 容器缺少 NET_ADMIN 或内核不支持 wireguard link"
			code = http.StatusServiceUnavailable
		} else {
			checks["netlink"] = "ok"
		}
		switch {
		case !c.Netlink:
			// netlink 都不可用时，forwarding 的结论没有意义。
		case hostMode:
			checks["forwarding"] = "managed by host"
		case !c.Forwarding:
			checks["forwarding"] = "unavailable: net.ipv4.ip_forward 未开启且不可写"
			code = http.StatusServiceUnavailable
		default:
			checks["forwarding"] = "ok"
		}
	}
	if s.netErr != nil {
		if err := s.netErr(); err != nil {
			checks["network"] = err.Error()
			code = http.StatusServiceUnavailable
		}
	}
	if s.rateErr != nil {
		// 限速失败不影响连通性，只是带宽策略没生效，因此不参与 503。
		if err := s.rateErr(); err != nil {
			checks["rateLimit"] = "degraded: " + err.Error()
		} else {
			checks["rateLimit"] = "ok"
		}
	}
	if s.lastReconcile != nil && s.lastReconcile().IsZero() {
		checks["reconcile"] = "pending"
	} else if s.lastReconcile != nil {
		checks["reconcile"] = "ok"
	}
	// 调谐错误优先于上面的「ok」：只要最近一次调谐失败，就说明期望态可能没落到内核。
	// 持续失败（本轮返回 sustained=true）直接判 503——隧道不通的实例留在负载后面，
	// 比启动失败更难排查：界面正常、数据正常，只有流量是死的。
	if s.syncStatus != nil {
		if err, sustained := s.syncStatus(); err != nil {
			checks["reconcile"] = "degraded: " + err.Error()
			if sustained {
				code = http.StatusServiceUnavailable
			}
		}
	}

	respondJSON(w, code, map[string]any{
		"status":       checks,
		"capabilities": caps,
		"ready":        code == http.StatusOK,
	})
}
