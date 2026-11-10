package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/Rewind2026/wgpanel/internal/authn"
	"github.com/Rewind2026/wgpanel/internal/domain"
)

// 账号管理接口的用户名大小写语义。
//
// 这组用例走完整 Router：用户名的归一化横跨登录、建号、限流与审计，
// 只测其中一层会漏掉「接口 200 但库里多了两个账号」这类静默错误。

// 建号时的大小写变体必须判为重复。
//
// 归一化若只做在某一层（只做登录、只做判重），
// 就会出现「Admin」和「admin」两个账号并存：
// 界面上看不出差别，审计日志里也分不出谁是谁。
func TestCreateUserRejectsCaseVariants(t *testing.T) {
	env := newTestEnv(t, nil)

	rec := env.do(t, http.MethodPost, "/api/v1/users/", domain.RoleOwner, map[string]any{
		"username": "OpsTeam",
		"password": "Strong#Pass123",
		"role":     "operator",
	})
	if rec.Code != http.StatusCreated && rec.Code != http.StatusOK {
		t.Fatalf("建号应成功，实际 %d：%s", rec.Code, rec.Body.String())
	}

	// 回传的用户名必须是归一后的形式，界面照它显示才不会和库里对不上。
	var out struct {
		Data struct {
			Username string `json:"username"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, rec.Body.String())
	}
	if out.Data.Username != "opsteam" {
		t.Errorf("建号回传的用户名应为小写规范形式，实际 %q", out.Data.Username)
	}

	for _, variant := range []string{"opsteam", "OPSTEAM", "Opsteam"} {
		rec := env.do(t, http.MethodPost, "/api/v1/users/", domain.RoleOwner, map[string]any{
			"username": variant,
			"password": "Strong#Pass123",
			"role":     "operator",
		})
		if rec.Code != http.StatusConflict {
			t.Errorf("用户名变体 %q 应判为重复（409），实际 %d：%s",
				variant, rec.Code, rec.Body.String())
		}
	}
}

// 登录时不区分大小写：库里存的是小写，用大写也要登得进去。
//
// 反过来更危险——若登录只认精确匹配，用户改了输入法大小写就登不进去，
// 而系统没有任何提示，看起来就像账号被删了。
func TestLoginAcceptsUsernameCaseVariants(t *testing.T) {
	env := newTestEnv(t, nil)
	ctx := t.Context()

	const password = "Strong#Pass123"
	hash, err := authn.HashPassword(password)
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	u, err := env.store.UserRepo().GetByID(ctx, env.userIDs[domain.RoleViewer])
	if err != nil {
		t.Fatalf("load viewer: %v", err)
	}
	if _, err := env.store.DB().ExecContext(ctx,
		`UPDATE user SET password_hash = ? WHERE id = ?`, hash, u.ID); err != nil {
		t.Fatalf("set password hash: %v", err)
	}

	for _, variant := range []string{u.Username, "TESTER-VIEWER", "Tester-Viewer"} {
		rec := env.login(t, variant, password)
		if rec.Code != http.StatusOK {
			t.Errorf("用 %q 登录应成功，实际 %d：%s", variant, rec.Code, rec.Body.String())
		}
	}
}
