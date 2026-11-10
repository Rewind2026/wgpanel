package service

import (
	"context"
	"strings"

	"github.com/Rewind2026/wgpanel/internal/authn"
	"github.com/Rewind2026/wgpanel/internal/domain"
	"github.com/Rewind2026/wgpanel/internal/store"
)

// UserService 负责账号管理：建号、角色分配、启停、重置密码与删除。
//
// 面板从单人部署升级为多账号后，这里承接所有「管别人」的操作；
// 「管自己」（读资料、改自己的密码）仍走 AccountService，
// 那条路径的身份取自请求 principal，不接受指定目标。
type UserService struct {
	users *store.UserRepo
	audit *store.AuditRepo
}

// NewUserService 构造账号管理服务。
func NewUserService(users *store.UserRepo, audit *store.AuditRepo) *UserService {
	return &UserService{users: users, audit: audit}
}

// roleRank 是角色层级：数值越大管辖面越大。
// 管理操作一律要求操作者层级高于目标，owner 对 owner 的管理是唯一的同级例外。
func roleRank(r domain.Role) int {
	switch r {
	case domain.RoleOwner:
		return 3
	case domain.RoleAdmin:
		return 2
	case domain.RoleOperator:
		return 1
	case domain.RoleViewer:
		return 0
	}
	return -1
}

// canManage 判断 actor 是否有资格对 target 角色的账号执行管理动作。
//
// 三条规则：操作者必须是 user:manage 的持有者（owner/admin），
// 层级不够的人连读列表都被中间件挡住，业务层不能只依赖中间件；
// owner 之外不得操作同级——admin 互删等于权限自我扩张；
// owner 管理 owner 是唯一的同级例外，否则多 owner 部署里互相锁死。
func canManage(actor domain.Role, target domain.Role) bool {
	if actor != domain.RoleOwner && actor != domain.RoleAdmin {
		return false
	}
	if actor != domain.RoleOwner && roleRank(actor) <= roleRank(target) {
		return false
	}
	return roleRank(actor) >= roleRank(target)
}

// List 返回全部账号。
func (s *UserService) List(ctx context.Context) ([]domain.User, error) {
	return s.users.List(ctx)
}

// CreateUserRequest 描述一次建号请求。
type CreateUserRequest struct {
	Username string
	Email    string
	Password string
	Role     domain.Role
}

// Create 新建账号。
//
// owner 角色不开放建号入口：部署的根身份只在首次启动时由
// EnsureOwner 生成，让建号接口能产出第二个 owner 等于给了
// 一条绕过「最后 owner 保护」的通路——先建新 owner 再自删旧 owner。
func (s *UserService) Create(ctx context.Context, req CreateUserRequest, actor *authn.Principal) (*domain.User, error) {
	// 先归一化再校验、再落库：校验函数内部也 trim，但若这里不归一化，
	// 「  bob  」会通过校验却带着空格入库，之后只能靠含空格的原样用户名登录，
	// 审计与界面里也会出现一串看不见的空白。三处必须用同一个值。
	//
	// 归一化同时做大小写折叠：用户名不区分大小写，落库统一小写，
	// 「Admin」与「admin」是同一个账号。否则库里能同时存在两个只差大小写的账号，
	// 审计日志里出现两条肉眼分不开的记录，「谁改的」永远说不清。
	username := domain.CanonicalUsername(req.Username)
	req.Username = username
	if !req.Role.Valid() || req.Role == domain.RoleOwner {
		return nil, domain.ErrInvalid("role must be one of admin, operator, viewer")
	}
	if !canManage(actor.Role, req.Role) {
		return nil, domain.ErrForbidden("role %s exceeds your authority", req.Role)
	}
	if err := validateUsername(username); err != nil {
		return nil, err
	}
	if err := ValidatePasswordStrength(req.Password, username); err != nil {
		return nil, err
	}
	hash, err := authn.HashPassword(req.Password)
	if err != nil {
		return nil, err
	}
	u := &domain.User{
		Username:     req.Username,
		Email:        strings.TrimSpace(req.Email),
		PasswordHash: hash,
		Role:         req.Role,
		Enabled:      true,
	}
	if _, err := s.users.Create(ctx, u); err != nil {
		return nil, err
	}
	s.writeAudit(ctx, actor, domain.AuditUserCreate, u.Username, "创建账号，角色 "+string(req.Role), domain.AuditSuccess)
	return u, nil
}

// SetRole 调整账号角色。
func (s *UserService) SetRole(ctx context.Context, targetID int64, role domain.Role, actor *authn.Principal) error {
	target, err := s.users.GetByID(ctx, targetID)
	if err != nil {
		return err
	}
	if err := s.checkAccess(actor, target, "改角色"); err != nil {
		return err
	}
	if !role.Valid() {
		return domain.ErrInvalid("unknown role %s", role)
	}
	// 改成本来就是的角色不是变更：早点返回，避免白白提升 token_version
	// 把对方踢下线，也避免审计里出现「operator → operator」这种噪声。
	if target.Role == role {
		return nil
	}
	// 目标角色本身也要过权限：admin 把 operator 提拔成 admin，
	// 等于借管理接口完成了一次同级账号生产，与建号路径同等对待。
	// owner 同样不开放——建号入口不许产出 owner，改角色也不许。
	if role == domain.RoleOwner || !canManage(actor.Role, role) {
		return domain.ErrForbidden("role %s exceeds your authority", role)
	}
	// 目标是 owner 而被降级时，降级后剩余的启用 owner 可能为零。
	// 守卫放在 UPDATE 的 WHERE 里：判断与写入必须原子，
	// 否则两个 owner 并发互相降级会双双通过检查。
	applied, err := s.users.UpdateRole(ctx, target.ID, role, target.Role == domain.RoleOwner)
	if err != nil {
		return err
	}
	if !applied {
		return domain.ErrConflict("cannot demote the last enabled owner")
	}
	s.writeAudit(ctx, actor, domain.AuditUserUpdate, target.Username,
		"角色 "+string(target.Role)+" → "+string(role), domain.AuditSuccess)
	return nil
}

// SetEnabled 启用或停用账号。
func (s *UserService) SetEnabled(ctx context.Context, targetID int64, enabled bool, actor *authn.Principal) error {
	target, err := s.users.GetByID(ctx, targetID)
	if err != nil {
		return err
	}
	if err := s.checkAccess(actor, target, "改启用状态"); err != nil {
		return err
	}
	if target.Enabled == enabled {
		return domain.ErrInvalid("account is already %s", map[bool]string{true: "enabled", false: "disabled"}[enabled])
	}
	// 停用是唯一能把「在用 owner」变成「零 owner」的操作，
	// 删除同理。守卫放在 UPDATE 的 WHERE 里，与写入原子执行。
	applied, err := s.users.UpdateEnabled(ctx, target.ID, enabled,
		!enabled && target.Role == domain.RoleOwner)
	if err != nil {
		return err
	}
	if !applied {
		return domain.ErrConflict("cannot disable the last enabled owner")
	}
	// 停用立即踢掉全部会话；启用没有需要吊销的东西，
	// token_version 提升只是让停用期间的残留令牌无法复活。
	if !enabled {
		if err := s.users.RevokeAllUserTokens(ctx, target.ID); err != nil {
			return err
		}
	}
	action := "停用账号"
	if enabled {
		action = "启用账号"
	}
	s.writeAudit(ctx, actor, domain.AuditUserUpdate, target.Username, action, domain.AuditSuccess)
	return nil
}

// ResetPassword 管理员重置目标账号的密码。
//
// 与自助改密一样吊销全部会话：密码被第三方重置后，
// 旧会话可能正是需要被切断的那条通路。
// 允许对自己操作——与 /account/password 是同义入口，不构成越权。
func (s *UserService) ResetPassword(ctx context.Context, targetID int64, newPassword string, actor *authn.Principal) error {
	target, err := s.users.GetByID(ctx, targetID)
	if err != nil {
		return err
	}
	if target.ID != actor.UserID && !canManage(actor.Role, target.Role) {
		return domain.ErrForbidden("insufficient authority over role %s", target.Role)
	}
	if err := ValidatePasswordStrength(newPassword, target.Username); err != nil {
		return err
	}
	hash, err := authn.HashPassword(newPassword)
	if err != nil {
		return err
	}
	target.PasswordHash = hash
	if err := s.users.UpdatePassword(ctx, target); err != nil {
		return err
	}
	if err := s.users.RevokeAllUserTokens(ctx, target.ID); err != nil {
		return err
	}
	s.writeAudit(ctx, actor, domain.AuditUserResetPw, target.Username, "密码已重置，全部会话已失效", domain.AuditSuccess)
	return nil
}

// Delete 删除账号。
func (s *UserService) Delete(ctx context.Context, targetID int64, actor *authn.Principal) error {
	target, err := s.users.GetByID(ctx, targetID)
	if err != nil {
		return err
	}
	if err := s.checkAccess(actor, target, "删除"); err != nil {
		return err
	}
	if target.Role == domain.RoleOwner {
		applied, err := s.users.Delete(ctx, targetID, true)
		if err != nil {
			return err
		}
		if !applied {
			return domain.ErrConflict("cannot delete the last enabled owner")
		}
	} else if _, err := s.users.Delete(ctx, targetID, false); err != nil {
		return err
	}
	s.writeAudit(ctx, actor, domain.AuditUserDelete, target.Username, "删除账号，角色 "+string(target.Role), domain.AuditSuccess)
	return nil
}

// checkAccess 是删/改/停共用的高压线：层级不够直接拒绝；
// 禁止对自身执行任何管理动作——自杀式操作（自删、自停、自降级）
// 没有「最后一个 owner」护栏兜底时的合法路径，干脆不留。
func (s *UserService) checkAccess(actor *authn.Principal, target *domain.User, action string) error {
	if !canManage(actor.Role, target.Role) {
		return domain.ErrForbidden("insufficient authority over role %s", target.Role)
	}
	if target.ID == actor.UserID {
		return domain.ErrForbidden("cannot %s your own account", action)
	}
	return nil
}

func (s *UserService) writeAudit(ctx context.Context, actor *authn.Principal, action domain.AuditAction,
	username, detail string, result domain.AuditResult) {
	if s.audit == nil {
		return
	}
	_ = s.audit.Write(ctx, &domain.AuditEvent{
		ActorID:    ptrID(actor),
		ActorName:  actorName(actor),
		Action:     action,
		Resource:   "user",
		ResourceID: username,
		Detail:     detail,
		Result:     result,
	})
}

// validateUsername 校验用户名格式：3-32 位，字母/数字/下划线/点/连字符。
// 收敛字符集是为了让用户名能安全地出现在日志、审计快照与订阅链接里。
func validateUsername(name string) error {
	name = strings.TrimSpace(name)
	if len(name) < 3 || len(name) > 32 {
		return domain.ErrInvalid("username must be 3-32 characters")
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '.', r == '-':
		default:
			return domain.ErrInvalid("username may only contain letters, digits, '_', '.', '-'")
		}
	}
	return nil
}
