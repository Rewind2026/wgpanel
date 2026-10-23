package service

import (
	"testing"
	"time"

	"github.com/Rewind2026/wgpanel/internal/authn"
	"github.com/Rewind2026/wgpanel/internal/domain"
	"github.com/Rewind2026/wgpanel/internal/store"
)

// 改密必须作废旧会话：密码改了而旧令牌还能用，等于没改。
//
// 这是 token_version 机制唯一的活体验证。access token 是无状态 JWT，
// 服务端不留存它的任何记录，只吊销 refresh token 只能挡住「续期」，
// 挡不住「已经签发、还没过期」的那一张——怀疑凭据泄露时，
// 攻击者手里的 access token 还能继续用满一个会话 TTL。
func TestPasswordChangeInvalidatesIssuedAccessToken(t *testing.T) {
	ctx := t.Context()
	st, err := store.OpenInMemory()
	if err != nil {
		t.Fatalf("open in-memory store: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	issuer := authn.NewTokenIssuer([]byte("test-secret-for-password-change"), 15*time.Minute)
	auth := NewAuthService(st.UserRepo(), issuer, nil, 15*time.Minute, 24*time.Hour, st.AuditRepo())
	account := NewAccountService(st.UserRepo(), st.AuditRepo())

	// 面板是单人部署，没有建号入口，测试只能走仓储层造账号。
	hash, err := authn.HashPassword("Aa1234567!")
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	uid, err := st.UserRepo().Create(ctx, &domain.User{
		Username:     "pwuser",
		PasswordHash: hash,
		Role:         domain.RoleOwner,
		Enabled:      true,
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	session, err := auth.Login(ctx, LoginRequest{Username: "pwuser", Password: "Aa1234567!"})
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if _, err := auth.AuthenticateAccessToken(ctx, session.AccessToken); err != nil {
		t.Fatalf("刚签发的 access token 应可用：%v", err)
	}

	actor := &authn.Principal{UserID: uid, Username: "pwuser", Role: domain.RoleOwner}
	if err := account.ChangePassword(ctx, uid, "Bb7654321!", actor); err != nil {
		t.Fatalf("change password: %v", err)
	}

	if _, err := auth.AuthenticateAccessToken(ctx, session.AccessToken); err == nil {
		t.Fatal("改密后，已签发的 access token 必须立即失效")
	}

	// 旧口令必须登不进去，新口令必须能登进去。
	if _, err := auth.Login(ctx, LoginRequest{Username: "pwuser", Password: "Aa1234567!"}); err == nil {
		t.Fatal("改密后旧口令必须失效")
	}
	fresh, err := auth.Login(ctx, LoginRequest{Username: "pwuser", Password: "Bb7654321!"})
	if err != nil {
		t.Fatalf("用新口令登录应成功: %v", err)
	}
	if _, err := auth.AuthenticateAccessToken(ctx, fresh.AccessToken); err != nil {
		t.Fatalf("改密后重新登录拿到的 access token 应可用：%v", err)
	}
}
