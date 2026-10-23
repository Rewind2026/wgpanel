package service

import (
	"context"
	"strconv"

	"github.com/Rewind2026/wgpanel/internal/authn"
	"github.com/Rewind2026/wgpanel/internal/domain"
	"github.com/Rewind2026/wgpanel/internal/store"
)

// AccountService 负责当前登录账号自己的资料维护。
//
// 面板是单人部署：不提供建号、删号与角色分配。
// 保留的只有改密这一件事——它是唯一无法从环境变量重置的凭据操作，
// 删掉它就意味着换密码只能改 WGP_ADMIN_PASSWORD 再重启容器。
type AccountService struct {
	users *store.UserRepo
	audit *store.AuditRepo
}

// NewAccountService 构造账号服务。
func NewAccountService(users *store.UserRepo, audit *store.AuditRepo) *AccountService {
	return &AccountService{users: users, audit: audit}
}

// Profile 读取当前账号的资料。
func (s *AccountService) Profile(ctx context.Context, id int64) (*domain.User, error) {
	return s.users.GetByID(ctx, id)
}

// ChangePassword 修改当前账号的密码。
//
// id 取自请求 principal，不接受调用方指定：这是「改自己的密码」，
// 不是「改别人的密码」，后者在单人部署里没有意义。
func (s *AccountService) ChangePassword(ctx context.Context, id int64, newPassword string, actor *authn.Principal) error {
	u, err := s.users.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if err := ValidatePasswordStrength(newPassword, u.Username); err != nil {
		return err
	}
	hash, err := authn.HashPassword(newPassword)
	if err != nil {
		return err
	}
	u.PasswordHash = hash

	// 改密后旧会话必须立即失效。
	//
	// 只吊销 refresh token 是不够的：access token 是无状态 JWT，
	// 服务端不留存它的任何记录，吊销 refresh 只能挡住「续期」，
	// 挡不住「已经签发、还没过期」的那一张——怀疑凭据泄露时，
	// 攻击者手里的 access token 还能继续用满一个会话 TTL（默认 15 分钟）。
	// 故这里同步提升 token_version，让此前签发的 access token 一并作废。
	u.TokenVersion++

	if err := s.users.UpdatePassword(ctx, u); err != nil {
		return err
	}
	if err := s.users.RevokeAllUserTokens(ctx, id); err != nil {
		return err
	}
	if s.audit != nil {
		// 绝不把密码本身写进审计，连哈希片段都不行。
		_ = s.audit.Write(ctx, &domain.AuditEvent{
			ActorID: ptrID(actor), ActorName: actorName(actor), Action: domain.AuditPasswordChange,
			Resource: "user", ResourceID: strconv.FormatInt(id, 10),
			Detail: "密码已修改，全部会话已失效", Result: domain.AuditSuccess,
		})
	}
	return nil
}
