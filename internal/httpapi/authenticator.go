package httpapi

import (
	"context"

	"github.com/Rewind2026/wgpanel/internal/authn"
	"github.com/Rewind2026/wgpanel/internal/domain"
	"github.com/Rewind2026/wgpanel/internal/service"
)

// Authenticator 抽象认证来源，便于测试替身注入。
type Authenticator interface {
	// Authenticate 按 access token 校验身份。
	Authenticate(ctx context.Context, accessToken, ip string) (*authn.Principal, error)
}

// AuthServiceAdapter 把 AuthService 适配为 Authenticator。
type AuthServiceAdapter struct {
	svc *service.AuthService
}

// NewAuthenticator 返回默认认证实现。
func NewAuthenticator(svc *service.AuthService) Authenticator {
	return &AuthServiceAdapter{svc: svc}
}

// Authenticate 校验访问令牌。
func (a *AuthServiceAdapter) Authenticate(ctx context.Context, accessToken, ip string) (*authn.Principal, error) {
	if accessToken == "" {
		return nil, domain.ErrUnauthorized("missing access token")
	}
	return a.svc.AuthenticateAccessToken(ctx, accessToken)
}
