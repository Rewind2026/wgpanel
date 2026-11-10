package service

import (
	"testing"
	"time"

	"github.com/Rewind2026/wgpanel/internal/authn"
	"github.com/Rewind2026/wgpanel/internal/domain"
	"github.com/Rewind2026/wgpanel/internal/store"
)

// 账号管理是唯一能改权限边界的入口，这里的规则全部围绕三个问题：
// 谁有资格管（层级）、能不能管自己（自我保护）、系统里能不能剩零个 owner（底线）。
// 每条规则都必须在这里活体验证，而不是只存在于注释里。

func newUserServiceForTest(t *testing.T) (*UserService, *store.Store) {
	t.Helper()
	st, err := store.OpenInMemory()
	if err != nil {
		t.Fatalf("open in-memory store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return NewUserService(st.UserRepo(), st.AuditRepo()), st
}

// mustSeedUser 直接走仓储造账号，绕开业务校验——测试需要构造
// 正常入口无法到达的状态（比如第二个 owner、禁用中的 owner）。
func mustSeedUser(t *testing.T, st *store.Store, username string, role domain.Role, enabled bool) *domain.User {
	t.Helper()
	hash, err := authn.HashPassword("Seed#Pass123")
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	u := &domain.User{
		Username:     username,
		PasswordHash: hash,
		Role:         role,
		Enabled:      enabled,
	}
	if _, err := st.UserRepo().Create(t.Context(), u); err != nil {
		t.Fatalf("seed user %s: %v", username, err)
	}
	return u
}

func principalOf(u *domain.User) *authn.Principal {
	return &authn.Principal{UserID: u.ID, Username: u.Username, Role: u.Role}
}

func TestCreateAccountRules(t *testing.T) {
	svc, st := newUserServiceForTest(t)
	owner := mustSeedUser(t, st, "root", domain.RoleOwner, true)
	admin := mustSeedUser(t, st, "boss", domain.RoleAdmin, true)

	t.Run("owner 可建 admin/operator/viewer", func(t *testing.T) {
		for _, role := range []domain.Role{domain.RoleAdmin, domain.RoleOperator, domain.RoleViewer} {
			u, err := svc.Create(t.Context(), CreateUserRequest{
				Username: "u_" + string(role), Password: "Strong#Pass123", Role: role,
			}, principalOf(owner))
			if err != nil {
				t.Fatalf("owner 创建 %s 应成功: %v", role, err)
			}
			if u.Role != role || !u.Enabled {
				t.Fatalf("新账号角色/状态不符: %+v", u)
			}
		}
	})

	t.Run("任何人都不能建 owner", func(t *testing.T) {
		_, err := svc.Create(t.Context(), CreateUserRequest{
			Username: "second_owner", Password: "Strong#Pass123", Role: domain.RoleOwner,
		}, principalOf(owner))
		if err == nil {
			t.Fatal("owner 角色不开放建号入口，必须拒绝")
		}
	})

	t.Run("admin 不能建同级 admin，但可以建下级", func(t *testing.T) {
		if _, err := svc.Create(t.Context(), CreateUserRequest{
			Username: "peer_admin", Password: "Strong#Pass123", Role: domain.RoleAdmin,
		}, principalOf(admin)); err == nil {
			t.Fatal("admin 建同级等于权限自我扩张，必须拒绝")
		}
		if _, err := svc.Create(t.Context(), CreateUserRequest{
			Username: "sub_op", Password: "Strong#Pass123", Role: domain.RoleOperator,
		}, principalOf(admin)); err != nil {
			t.Fatalf("admin 创建 operator 应成功: %v", err)
		}
	})

	t.Run("用户名重复与弱口令", func(t *testing.T) {
		_, err := svc.Create(t.Context(), CreateUserRequest{
			Username: "u_admin", Password: "Strong#Pass123", Role: domain.RoleOperator,
		}, principalOf(owner))
		if err == nil {
			t.Fatal("重复用户名必须拒绝")
		}
		_, err = svc.Create(t.Context(), CreateUserRequest{
			Username: "fresh_user", Password: "short", Role: domain.RoleViewer,
		}, principalOf(owner))
		if err == nil {
			t.Fatal("弱口令必须拒绝")
		}
	})

	t.Run("用户名两端空白被归一化", func(t *testing.T) {
		u, err := svc.Create(t.Context(), CreateUserRequest{
			Username: "  spaced  ", Password: "Strong#Pass123", Role: domain.RoleViewer,
		}, principalOf(owner))
		if err != nil {
			t.Fatalf("带空白的用户名应被 trim 后正常建号: %v", err)
		}
		if u.Username != "spaced" {
			t.Fatalf("用户名应落库为 trim 后的值，实际 %q", u.Username)
		}
		if _, err := st.UserRepo().GetByUsername(t.Context(), "  spaced  "); err == nil {
			t.Fatal("带空白的原样用户名不应能查到账号")
		}
	})

	t.Run("用户名大小写不敏感", func(t *testing.T) {
		u, err := svc.Create(t.Context(), CreateUserRequest{
			Username: "MiXeD", Password: "Strong#Pass123", Role: domain.RoleViewer,
		}, principalOf(owner))
		if err != nil {
			t.Fatalf("建号应成功: %v", err)
		}
		if u.Username != "mixed" {
			t.Fatalf("用户名应落库为小写规范形式，实际 %q", u.Username)
		}
		// 大小写变体是同一个账号，不能再建一个出来。
		for _, variant := range []string{"mixed", "MIXED", "Mixed"} {
			if _, err := svc.Create(t.Context(), CreateUserRequest{
				Username: variant, Password: "Strong#Pass123", Role: domain.RoleViewer,
			}, principalOf(owner)); err == nil {
				t.Fatalf("用户名变体 %q 应被判为重复", variant)
			}
		}
	})

	t.Run("operator 越权建号", func(t *testing.T) {
		op := mustSeedUser(t, st, "plain_op", domain.RoleOperator, true)
		_, err := svc.Create(t.Context(), CreateUserRequest{
			Username: "from_op", Password: "Strong#Pass123", Role: domain.RoleViewer,
		}, principalOf(op))
		if err == nil {
			t.Fatal("operator 没有 user:manage 权限，业务层同样必须拒绝")
		}
	})
}

func TestHierarchyRules(t *testing.T) {
	svc, st := newUserServiceForTest(t)
	owner := mustSeedUser(t, st, "root", domain.RoleOwner, true)
	adminA := mustSeedUser(t, st, "admin_a", domain.RoleAdmin, true)
	adminB := mustSeedUser(t, st, "admin_b", domain.RoleAdmin, true)
	op := mustSeedUser(t, st, "operator", domain.RoleOperator, true)
	viewer := mustSeedUser(t, st, "watcher", domain.RoleViewer, true)

	t.Run("admin 不可操作 admin 与 owner", func(t *testing.T) {
		for _, target := range []*domain.User{owner, adminB} {
			if err := svc.SetRole(t.Context(), target.ID, domain.RoleViewer, principalOf(adminA)); err == nil {
				t.Fatalf("admin 改 %s 的角色必须被拒", target.Username)
			}
			if err := svc.SetEnabled(t.Context(), target.ID, false, principalOf(adminA)); err == nil {
				t.Fatalf("admin 停用 %s 必须被拒", target.Username)
			}
			if err := svc.Delete(t.Context(), target.ID, principalOf(adminA)); err == nil {
				t.Fatalf("admin 删除 %s 必须被拒", target.Username)
			}
			if err := svc.ResetPassword(t.Context(), target.ID, "NewPass#4567", principalOf(adminA)); err == nil {
				t.Fatalf("admin 重置 %s 密码必须被拒", target.Username)
			}
		}
	})

	t.Run("owner 可管理下级全操作", func(t *testing.T) {
		if err := svc.SetRole(t.Context(), viewer.ID, domain.RoleOperator, principalOf(owner)); err != nil {
			t.Fatalf("owner 改角色应成功: %v", err)
		}
		if err := svc.SetEnabled(t.Context(), op.ID, false, principalOf(owner)); err != nil {
			t.Fatalf("owner 停用应成功: %v", err)
		}
		if err := svc.SetEnabled(t.Context(), op.ID, true, principalOf(owner)); err != nil {
			t.Fatalf("owner 启用应成功: %v", err)
		}
	})

	t.Run("对自身的一切破坏性操作必须被拒", func(t *testing.T) {
		if err := svc.Delete(t.Context(), owner.ID, principalOf(owner)); err == nil {
			t.Fatal("自删必须被拒")
		}
		if err := svc.SetEnabled(t.Context(), owner.ID, false, principalOf(owner)); err == nil {
			t.Fatal("自停用必须被拒")
		}
		if err := svc.SetRole(t.Context(), owner.ID, domain.RoleAdmin, principalOf(owner)); err == nil {
			t.Fatal("自降级必须被拒")
		}
		// 重置自己的密码不是破坏性操作，与 /account/password 同义，必须放行。
		if err := svc.ResetPassword(t.Context(), owner.ID, "SelfPass#123", principalOf(owner)); err != nil {
			t.Fatalf("自重置密码应放行: %v", err)
		}
	})

	t.Run("admin 不能把 operator 提拔成 admin", func(t *testing.T) {
		if err := svc.SetRole(t.Context(), op.ID, domain.RoleAdmin, principalOf(adminA)); err == nil {
			t.Fatal("提拔到与己同级必须被拒")
		}
	})

	// 单列更新是并发安全的前提：改角色不能顺带把「刚被停用」的账号重新启用，
	// 停用也不能顺带把刚改的角色覆盖回读到的旧值。
	t.Run("改角色不影响启用状态", func(t *testing.T) {
		sleeper := mustSeedUser(t, st, "sleeper", domain.RoleOperator, false)
		if err := svc.SetRole(t.Context(), sleeper.ID, domain.RoleViewer, principalOf(owner)); err != nil {
			t.Fatalf("对停用账号改角色应成功: %v", err)
		}
		after, err := st.UserRepo().GetByID(t.Context(), sleeper.ID)
		if err != nil {
			t.Fatalf("read back: %v", err)
		}
		if after.Role != domain.RoleViewer || after.Enabled {
			t.Fatalf("改角色不应改动启用状态，实际 role=%s enabled=%v", after.Role, after.Enabled)
		}
	})

	t.Run("启停不影响角色", func(t *testing.T) {
		if err := svc.SetEnabled(t.Context(), op.ID, false, principalOf(owner)); err != nil {
			t.Fatalf("停用应成功: %v", err)
		}
		after, err := st.UserRepo().GetByID(t.Context(), op.ID)
		if err != nil {
			t.Fatalf("read back: %v", err)
		}
		if after.Role != domain.RoleOperator {
			t.Fatalf("停用不应改动角色，实际 %s", after.Role)
		}
	})
}

// 最后 owner 保护的业务路径在正常鉴权下不可达（能调接口的 owner 必然启用中，
// 自身就是「剩下来的 owner」），它是一道纵深防御：鉴权态与库内状态之间
// 出现时间差、或未来放开自我操作时，这道护栏兜底。
// 因此这里的用例直接构造「调用方主体与库内状态脱节」的场景来触发它。
func TestLastOwnerProtection(t *testing.T) {
	svc, st := newUserServiceForTest(t)
	// root：库内已禁用的 owner，模拟「主体仍带 owner 身份但库里已无启用 owner」；
	// spare：唯一启用中的 owner。
	root := mustSeedUser(t, st, "root", domain.RoleOwner, false)
	spare := mustSeedUser(t, st, "spare", domain.RoleOwner, true)
	ghost := principalOf(root)

	t.Run("不能停用最后一个启用的 owner", func(t *testing.T) {
		if err := svc.SetEnabled(t.Context(), spare.ID, false, ghost); err == nil {
			t.Fatal("停用最后一个启用 owner 必须被拒")
		}
	})

	t.Run("不能删除最后一个启用的 owner", func(t *testing.T) {
		if err := svc.Delete(t.Context(), spare.ID, ghost); err == nil {
			t.Fatal("删除最后一个启用 owner 必须被拒")
		}
	})

	t.Run("不能降级最后一个启用的 owner", func(t *testing.T) {
		if err := svc.SetRole(t.Context(), spare.ID, domain.RoleAdmin, ghost); err == nil {
			t.Fatal("降级最后一个启用 owner 必须被拒")
		}
	})

	t.Run("存在另一个启用 owner 时操作放行", func(t *testing.T) {
		extra := mustSeedUser(t, st, "extra_owner", domain.RoleOwner, true)
		if err := svc.SetEnabled(t.Context(), spare.ID, false, ghost); err != nil {
			t.Fatalf("还有其他启用 owner 时停用应放行: %v", err)
		}
		if err := svc.Delete(t.Context(), spare.ID, principalOf(extra)); err != nil {
			t.Fatalf("删除已停用的 owner 应放行: %v", err)
		}
	})
}

func TestResetPasswordRevokesSessions(t *testing.T) {
	svc, st := newUserServiceForTest(t)
	issuer := authn.NewTokenIssuer([]byte("test-secret-for-user-service"), 15*time.Minute)
	auth := NewAuthService(st.UserRepo(), issuer, nil, 15*time.Minute, 24*time.Hour, 0, st.AuditRepo())

	owner := mustSeedUser(t, st, "root", domain.RoleOwner, true)
	target := mustSeedUser(t, st, "member", domain.RoleOperator, true)

	session, err := auth.Login(t.Context(), LoginRequest{Username: "member", Password: "Seed#Pass123"})
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if _, err := auth.AuthenticateAccessToken(t.Context(), session.AccessToken); err != nil {
		t.Fatalf("刚签发的 token 应可用: %v", err)
	}

	if err := svc.ResetPassword(t.Context(), target.ID, "Reset#Pass456", principalOf(owner)); err != nil {
		t.Fatalf("reset password: %v", err)
	}

	if _, err := auth.AuthenticateAccessToken(t.Context(), session.AccessToken); err == nil {
		t.Fatal("重置密码后，目标用户已签发的 access token 必须立即失效")
	}
	if _, err := auth.Login(t.Context(), LoginRequest{Username: "member", Password: "Seed#Pass123"}); err == nil {
		t.Fatal("重置后旧口令必须失效")
	}
	if _, err := auth.Login(t.Context(), LoginRequest{Username: "member", Password: "Reset#Pass456"}); err != nil {
		t.Fatalf("新口令应可登录: %v", err)
	}
}

func TestDisableBlocksLoginAndRevokesSessions(t *testing.T) {
	svc, st := newUserServiceForTest(t)
	issuer := authn.NewTokenIssuer([]byte("test-secret-for-disable"), 15*time.Minute)
	auth := NewAuthService(st.UserRepo(), issuer, nil, 15*time.Minute, 24*time.Hour, 0, st.AuditRepo())

	owner := mustSeedUser(t, st, "root", domain.RoleOwner, true)
	target := mustSeedUser(t, st, "member", domain.RoleAdmin, true)

	session, err := auth.Login(t.Context(), LoginRequest{Username: "member", Password: "Seed#Pass123"})
	if err != nil {
		t.Fatalf("login: %v", err)
	}

	if err := svc.SetEnabled(t.Context(), target.ID, false, principalOf(owner)); err != nil {
		t.Fatalf("disable: %v", err)
	}

	if _, err := auth.AuthenticateAccessToken(t.Context(), session.AccessToken); err == nil {
		t.Fatal("停用后已签发的 access token 必须立即失效")
	}
	if _, err := auth.Login(t.Context(), LoginRequest{Username: "member", Password: "Seed#Pass123"}); err == nil {
		t.Fatal("停用后登录必须被拒")
	}

	if err := svc.SetEnabled(t.Context(), target.ID, true, principalOf(owner)); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if _, err := auth.Login(t.Context(), LoginRequest{Username: "member", Password: "Seed#Pass123"}); err != nil {
		t.Fatalf("重新启用后应可登录: %v", err)
	}
}

func TestDeleteRemovesUserAndTokens(t *testing.T) {
	svc, st := newUserServiceForTest(t)
	owner := mustSeedUser(t, st, "root", domain.RoleOwner, true)
	target := mustSeedUser(t, st, "member", domain.RoleViewer, true)

	if _, err := authn.HashPassword("Seed#Pass123"); err != nil {
		t.Fatal(err)
	}
	if err := st.UserRepo().StoreRefreshToken(t.Context(), target.ID, "some-refresh-token", time.Now().Add(time.Hour).Unix()); err != nil {
		t.Fatalf("store refresh token: %v", err)
	}

	if err := svc.Delete(t.Context(), target.ID, principalOf(owner)); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := st.UserRepo().GetByID(t.Context(), target.ID); err == nil {
		t.Fatal("删除后用户必须不存在")
	}
	// refresh_token 对 user 是 CASCADE，删人后令牌一并清空；
	// 否则孤儿令牌仍有被 Consume 的理论通路。
	if _, err := st.UserRepo().ConsumeRefreshToken(t.Context(), "some-refresh-token", 0); err == nil {
		t.Fatal("被删用户的 refresh token 必须一并失效")
	}
}

// 用户名大小写不敏感必须在登录侧同样成立：
// 库里存的是小写，用户输入大写也必须登进同一个账号，
// 否则「换个大小写就登不进去」会被当成账号被删了。
func TestLoginUsernameIsCaseInsensitive(t *testing.T) {
	svc, st := newUserServiceForTest(t)
	issuer := authn.NewTokenIssuer([]byte("test-secret-for-case-login"), 15*time.Minute)
	limiter := authn.NewLoginLimiter(3, 15*time.Minute)
	auth := NewAuthService(st.UserRepo(), issuer, limiter, 15*time.Minute, 24*time.Hour, 0, st.AuditRepo())

	owner := mustSeedUser(t, st, "root", domain.RoleOwner, true)
	if _, err := svc.Create(t.Context(), CreateUserRequest{
		Username: "OpsTeam", Password: "Strong#Pass123", Role: domain.RoleOperator,
	}, principalOf(owner)); err != nil {
		t.Fatalf("create: %v", err)
	}

	for _, variant := range []string{"opsteam", "OpsTeam", "OPSTEAM", "  Opsteam  "} {
		session, err := auth.Login(t.Context(), LoginRequest{Username: variant, Password: "Strong#Pass123"})
		if err != nil {
			t.Fatalf("用 %q 登录应成功: %v", variant, err)
		}
		if session.User.Username != "opsteam" {
			t.Fatalf("登录返回的用户名应为规范形式 opsteam，实际 %q", session.User.Username)
		}
	}

	// 限流桶必须按规范形式计数：否则同一个账号有若干个桶，
	// 攻击者靠变换大小写就能无限次试密码。
	//
	// IP 维度用不同地址隔离，否则 IP 桶先耗尽，测到的就不是用户名维度了。
	attempts := []struct{ ip, username string }{
		{"10.0.0.1", "wrong"},
		{"10.0.0.2", "WRONG"},
		{"10.0.0.3", "Wrong"},
	}
	for _, a := range attempts {
		_, _ = auth.Login(t.Context(), LoginRequest{
			Username: a.username, Password: "Bad#Pass456", IP: a.ip,
		})
	}
	// 第四次用新 IP + 同一个用户名（小写）：只有用户名桶已经攒够 3 次失败才会拦住它。
	_, err := auth.Login(t.Context(), LoginRequest{
		Username: "wrong", Password: "Bad#Pass456", IP: "10.0.0.4",
	})
	if domain.AsError(err).Code != domain.CodeRateLimited {
		t.Fatalf("变换大小写的三次失败应落进同一个限流桶，实际错误 %v", err)
	}
}

func TestAuditTrailWritten(t *testing.T) {
	svc, st := newUserServiceForTest(t)
	owner := mustSeedUser(t, st, "root", domain.RoleOwner, true)

	created, err := svc.Create(t.Context(), CreateUserRequest{
		Username: "audited", Password: "Strong#Pass123", Role: domain.RoleViewer,
	}, principalOf(owner))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := svc.SetRole(t.Context(), created.ID, domain.RoleOperator, principalOf(owner)); err != nil {
		t.Fatalf("set role: %v", err)
	}
	if err := svc.Delete(t.Context(), created.ID, principalOf(owner)); err != nil {
		t.Fatalf("delete: %v", err)
	}

	events, _, err := st.AuditRepo().Query(t.Context(), domain.AuditQuery{Resource: "user", Page: 1, PageSize: 50})
	if err != nil {
		t.Fatalf("query audit: %v", err)
	}
	if len(events) < 3 {
		t.Fatalf("建号/改角色/删除都应留下审计，实际 %d 条: %+v", len(events), events)
	}
	wantActions := map[domain.AuditAction]bool{
		domain.AuditUserCreate: false,
		domain.AuditUserUpdate: false,
		domain.AuditUserDelete: false,
	}
	for _, ev := range events {
		if _, ok := wantActions[ev.Action]; ok {
			wantActions[ev.Action] = true
		}
	}
	for action, seen := range wantActions {
		if !seen {
			t.Fatalf("缺少审计动作 %s", action)
		}
	}
}
