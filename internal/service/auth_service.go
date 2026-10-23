package service

import (
	"context"
	"strings"
	"time"

	"github.com/Rewind2026/wgpanel/internal/authn"
	"github.com/Rewind2026/wgpanel/internal/domain"
	"github.com/Rewind2026/wgpanel/internal/store"
)

// AuthService 负责认证、会话与 RBAC 判定。
type AuthService struct {
	users      *store.UserRepo
	issuer     *authn.TokenIssuer
	limiter    *authn.LoginLimiter
	sessionTTL time.Duration
	refreshTTL time.Duration
	audit      *store.AuditRepo
}

// NewAuthService 构造认证服务。
func NewAuthService(users *store.UserRepo, issuer *authn.TokenIssuer, limiter *authn.LoginLimiter,
	sessionTTL, refreshTTL time.Duration, audit *store.AuditRepo) *AuthService {
	return &AuthService{
		users:      users,
		issuer:     issuer,
		limiter:    limiter,
		sessionTTL: sessionTTL,
		refreshTTL: refreshTTL,
		audit:      audit,
	}
}

// EnsureOwner 在首次启动时创建 owner 用户。
// 返回生成的管理员初始密码（仅在随机生成时非空），供启动日志打印一次。
func (s *AuthService) EnsureOwner(ctx context.Context, username, password string) (string, error) {
	count, err := s.users.CountAll(ctx)
	if err != nil {
		return "", err
	}
	if count > 0 {
		return "", nil
	}

	initialPassword := password
	if initialPassword == "" {
		generated, err := authn.RandomToken(12)
		if err != nil {
			return "", err
		}
		initialPassword = generated
	}
	if err := ValidatePasswordStrength(initialPassword, username); err != nil {
		return "", err
	}
	hash, err := authn.HashPassword(initialPassword)
	if err != nil {
		return "", err
	}
	owner := &domain.User{
		Username:     username,
		PasswordHash: hash,
		Role:         domain.RoleOwner,
		Enabled:      true,
	}
	if _, err := s.users.Create(ctx, owner); err != nil {
		return "", err
	}
	if password == "" {
		return initialPassword, nil
	}
	return "", nil
}

// LoginRequest 描述一次登录尝试。
type LoginRequest struct {
	Username  string
	Password  string
	IP        string
	UserAgent string
}

// Login 校验凭据并签发令牌对。
func (s *AuthService) Login(ctx context.Context, req LoginRequest) (*domain.Session, error) {
	if s.limiter != nil {
		for _, key := range []string{"ip:" + req.IP, "user:" + req.Username} {
			if !s.limiter.Allow(key) {
				s.record(ctx, nil, req.Username, domain.AuditLoginFailed, req, domain.AuditFailure, "rate limited")
				return nil, domain.ErrRateLimited("too many login attempts, please retry later")
			}
		}
	}

	u, err := s.users.GetByUsername(ctx, req.Username)
	if err != nil {
		s.record(ctx, nil, req.Username, domain.AuditLoginFailed, req, domain.AuditFailure, "user not found")
		s.countFailure(req)
		return nil, domain.ErrUnauthorized("invalid username or password")
	}
	if !u.Enabled {
		s.record(ctx, &u.ID, u.Username, domain.AuditLoginFailed, req, domain.AuditFailure, "user disabled")
		return nil, domain.ErrForbidden("account is disabled")
	}
	if !authn.VerifyPassword(req.Password, u.PasswordHash) {
		s.record(ctx, &u.ID, u.Username, domain.AuditLoginFailed, req, domain.AuditFailure, "bad password")
		s.countFailure(req)
		return nil, domain.ErrUnauthorized("invalid username or password")
	}

	if s.limiter != nil {
		s.limiter.Reset("ip:" + req.IP)
		s.limiter.Reset("user:" + req.Username)
	}

	now := time.Now()
	access, err := s.issuer.Issue(u, now)
	if err != nil {
		return nil, err
	}
	refresh, err := authn.RandomToken(32)
	if err != nil {
		return nil, err
	}
	if err := s.users.StoreRefreshToken(ctx, u.ID, refresh, now.Add(s.refreshTTL).Unix()); err != nil {
		return nil, err
	}
	if err := s.users.TouchLastLogin(ctx, u.ID); err != nil {
		return nil, err
	}

	s.record(ctx, &u.ID, u.Username, domain.AuditLogin, req, domain.AuditSuccess, "")
	return &domain.Session{
		AccessToken:  access,
		RefreshToken: refresh,
		ExpiresIn:    int64(s.sessionTTL.Seconds()),
		User:         u,
		IssuedAt:     now,
	}, nil
}

// Refresh 轮换刷新令牌并签发新的访问令牌。
func (s *AuthService) Refresh(ctx context.Context, refreshToken string) (*domain.Session, error) {
	userID, err := s.users.ConsumeRefreshToken(ctx, refreshToken)
	if err != nil {
		return nil, err
	}
	u, err := s.users.GetByID(ctx, userID)
	if err != nil {
		return nil, err
	}
	if !u.Enabled {
		return nil, domain.ErrForbidden("account is disabled")
	}
	now := time.Now()
	access, err := s.issuer.Issue(u, now)
	if err != nil {
		return nil, err
	}
	next, err := authn.RandomToken(32)
	if err != nil {
		return nil, err
	}
	if err := s.users.StoreRefreshToken(ctx, u.ID, next, now.Add(s.refreshTTL).Unix()); err != nil {
		return nil, err
	}
	return &domain.Session{
		AccessToken:  access,
		RefreshToken: next,
		ExpiresIn:    int64(s.sessionTTL.Seconds()),
		User:         u,
		IssuedAt:     now,
	}, nil
}

// Logout 撤销刷新令牌。
//
// 身份从被撤销的刷新令牌反查，而不是从请求 principal：
// 登出路由不挂鉴权中间件——会话可能已过期，硬要求 Bearer 反而让
// 用户在令牌失效时永远登不出去，拿不到 principal 就没有审计主体。
func (s *AuthService) Logout(ctx context.Context, refreshToken string, p *authn.Principal) error {
	if refreshToken == "" {
		return nil
	}
	userID, err := s.users.ConsumeRefreshToken(ctx, refreshToken)
	if err != nil {
		return nil // 幂等：令牌已失效时登出仍视为成功
	}
	// 登出同样是一次身份事件：排查「谁在什么时候登进了面板」时，
	// 只有登录没有登出，等于只记录了门被打开却没记录门被关上。
	actor := p
	if actor == nil {
		actor = &authn.Principal{UserID: userID}
		if u, err := s.users.GetByID(ctx, userID); err == nil {
			actor.Username = u.Username
			actor.Role = u.Role
		}
	}
	s.record(ctx, &actor.UserID, actor.Username, domain.AuditLogout, LoginRequest{}, domain.AuditSuccess, "")
	return nil
}

// AuthenticateAccessToken 校验访问令牌并返回主体。
func (s *AuthService) AuthenticateAccessToken(ctx context.Context, token string) (*authn.Principal, error) {
	claims, err := s.issuer.Parse(token)
	if err != nil {
		return nil, err
	}
	u, err := s.users.GetByID(ctx, claims.UserID)
	if err != nil {
		return nil, domain.ErrUnauthorized("user no longer exists")
	}
	if !u.Enabled {
		return nil, domain.ErrForbidden("account is disabled")
	}
	// 改密、停用或强制下线都会提升 token_version。
	// JWT 本身无状态、服务端不留存，靠 ver 比对才能让「已签发但尚未过期」
	// 的那一张令牌当场失效——否则「强制下线」只挡住了续期，没挡住在用会话。
	if claims.Ver != u.TokenVersion {
		return nil, domain.ErrUnauthorized("session has been revoked")
	}
	return &authn.Principal{UserID: u.ID, Username: u.Username, Role: u.Role}, nil
}

// RevokeUserSessions 强制下线某用户的全部会话。
func (s *AuthService) RevokeUserSessions(ctx context.Context, userID int64) error {
	return s.users.RevokeAllUserTokens(ctx, userID)
}

func (s *AuthService) countFailure(req LoginRequest) {
	if s.limiter == nil {
		return
	}
	s.limiter.Fail("ip:" + req.IP)
	s.limiter.Fail("user:" + req.Username)
}

func (s *AuthService) record(ctx context.Context, actorID *int64, actorName string,
	action domain.AuditAction, req LoginRequest, result domain.AuditResult, detail string) {
	if s.audit == nil {
		return
	}
	_ = s.audit.Write(ctx, &domain.AuditEvent{
		ActorID:   actorID,
		ActorName: actorName,
		Action:    action,
		Resource:  "auth",
		Detail:    detail,
		IP:        req.IP,
		UserAgent: req.UserAgent,
		Result:    result,
	})
}

// ValidatePasswordStrength 校验密码强度。
func ValidatePasswordStrength(password, username string) error {
	if len(password) < 10 {
		return domain.ErrInvalid("password must be at least 10 characters")
	}
	if len(password) > 128 {
		return domain.ErrInvalid("password must not exceed 128 characters")
	}
	if strings.EqualFold(password, username) {
		return domain.ErrInvalid("password must not equal username")
	}
	var hasLetter, hasOther bool
	for _, r := range password {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r >= 0x4e00:
			hasLetter = true
		default:
			hasOther = true
		}
	}
	if !hasLetter || !hasOther {
		return domain.ErrInvalid("password must mix letters with digits or symbols")
	}
	return nil
}
