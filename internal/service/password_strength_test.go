package service

import (
	"strings"
	"testing"
)

// 密码强度规则的前后端口径必须一致：
// 前端镜像（web/src/lib/password.ts）在提交前就告诉用户哪里不达标，
// 两边口径一旦分叉，就会出现「界面全绿、提交被拒」——
// 用户既看不到哪里不对，也无从修正。
//
// 这些用例锁住三个容易分叉的点：
//   - 长度按字符计，不是字节；
//   - 「字母」按 Unicode 字母类别判定，emoji 属于「其他」；
//   - 与用户名的比较只折叠 ASCII 大小写。

func TestValidatePasswordStrength(t *testing.T) {
	cases := []struct {
		name     string
		password string
		username string
		wantErr  string
	}{
		{name: "合规口令", password: "Strong#Pass123", username: "admin"},
		{name: "纯字母", password: "abcdefghij", username: "admin", wantErr: "must mix letters"},
		{name: "纯数字", password: "12345678901", username: "admin", wantErr: "must mix letters"},
		{name: "太短", password: "Ab#12", username: "admin", wantErr: "at least 10 characters"},
		{name: "超长", password: strings.Repeat("a1", 65), username: "admin", wantErr: "must not exceed 128 characters"},
		{name: "与用户名相同", password: "longadmin01", username: "longadmin01", wantErr: "must not equal username"},
		{name: "与用户名相同（忽略大小写）", password: "LONGADMIN01", username: "longadmin01", wantErr: "must not equal username"},
		{name: "只是包含用户名", password: "longadmin01x", username: "longadmin01"},

		// 长度按字符计：10 个汉字是 10 位（30 字节），应当通过长度下限。
		{name: "10 位汉字", password: "密码密码密码密码密码", username: "admin", wantErr: "must mix letters"},
		{name: "10 位汉字加数字", password: "密码密码密码密码密码1", username: "admin"},
		// 同样 10 个字符，字节数是 30，超过按字节算的下限会误判为「太短」——
		// 中文用户按「10 位」理解时长度是达标的。
		{name: "10 位汉字含数字通过长度下限", password: "密码密码密码密码密码2", username: "admin"},

		// emoji 是符号，不是字母：混不进「字母」这一侧。
		{name: "emoji 加数字无字母", password: "🔒🔒🔒🔒🔒12345", username: "admin", wantErr: "must mix letters"},
		{name: "emoji 加字母", password: "🔒🔒🔒🔒🔒abcde", username: "admin"},
		// 假名属于字母：应与后端的 unicode.IsLetter 保持一致。
		{name: "日文假名加数字", password: "あいうえおかきくけこ1", username: "admin"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidatePasswordStrength(tc.password, tc.username)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("期望通过，实际被拒：%v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("期望以 %q 拒绝，实际通过", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("期望错误含 %q，实际：%v", tc.wantErr, err)
			}
		})
	}
}

// 用户名只允许 ASCII，因此「与用户名相同」只需折叠 ASCII 大小写。
// 用完整 Unicode 折叠会在 İ 之类的字符上与前端镜像得出相反结论。
func TestValidatePasswordStrengthUsernameFold(t *testing.T) {
	// İstanbul1 共 9 个字符，先补一位再验证。
	if err := ValidatePasswordStrength("İstanbul12", "admin"); err != nil {
		t.Fatalf("非 ASCII 字符不应被判为与用户名相同：%v", err)
	}
}
