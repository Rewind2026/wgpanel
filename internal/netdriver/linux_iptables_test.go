//go:build linux

package netdriver

import (
	"strings"
	"testing"
)

// TestWithFlagPlacesTableBeforeCommand 锁定 iptables 参数顺序。
//
// 真机上暴露的缺陷：动作标志被放在最前面，带表规则变成
// "-A -t nat POSTROUTING ..."，iptables 把 `nat` 当链名，报
// "Bad argument `nat'"，NAT 与 MSS 规则一条都下发不了。
// 而 panel 自身毫无察觉——wg 接口正常、登录正常，只有出网流量静默失败。
func TestWithFlagPlacesTableBeforeCommand(t *testing.T) {
	cases := []struct {
		name string
		flag string
		rule []string
		want string
	}{
		{
			name: "nat 表",
			flag: "-A",
			rule: []string{"-t", "nat", "POSTROUTING", "-s", "10.8.0.0/24", "!", "-o", "wg0", "-j", "MASQUERADE"},
			want: "-t nat -A POSTROUTING -s 10.8.0.0/24 ! -o wg0 -j MASQUERADE",
		},
		{
			name: "mangle 表",
			flag: "-D",
			rule: []string{"-t", "mangle", "FORWARD", "-p", "tcp", "-j", "TCPMSS", "--clamp-mss-to-pmtu"},
			want: "-t mangle -D FORWARD -p tcp -j TCPMSS --clamp-mss-to-pmtu",
		},
		{
			name: "无表前缀",
			flag: "-C",
			rule: []string{"FORWARD", "-i", "wg0", "-j", "ACCEPT"},
			want: "-C FORWARD -i wg0 -j ACCEPT",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := strings.Join(withFlag(c.flag, c.rule), " ")
			if got != c.want {
				t.Errorf("withFlag(%q, ...) =\n  %s\nwant\n  %s", c.flag, got, c.want)
			}
		})
	}
}

// TestWithFlagDoesNotMutateInput 确认规则切片不被改写。
// 规则定义是包级共享的表驱动数据，就地改写会让第二次调用拼出错误参数。
func TestWithFlagDoesNotMutateInput(t *testing.T) {
	rule := []string{"-t", "nat", "POSTROUTING", "-j", "MASQUERADE"}
	orig := strings.Join(rule, " ")
	_ = withFlag("-A", rule)
	if got := strings.Join(rule, " "); got != orig {
		t.Errorf("输入被改写：%s，期望 %s", got, orig)
	}
}

// TestRealRuleSetsUseValidFlagOrder 直接断言生产代码的规则集，
// 而不是抄一份副本：抄副本的话，规则写错时测试依然全绿。
func TestRealRuleSetsUseValidFlagOrder(t *testing.T) {
	for _, set := range [][][]string{natRules("wg0", "10.8.0.0/24"), natTeardownRules("wg0", "10.8.0.0/24")} {
		for _, rule := range set {
			for _, flag := range []string{"-C", "-A", "-D"} {
				args := withFlag(flag, rule)
				cmdIdx := -1
				for i, a := range args {
					if a == flag {
						cmdIdx = i
						break
					}
				}
				if cmdIdx < 0 {
					t.Fatalf("标志 %s 未出现在参数中：%v", flag, args)
				}
				// -t 是全局选项，必须排在命令之前。
				for i, a := range args {
					if a == "-t" && i > cmdIdx {
						t.Errorf("规则 %v 加标志 %s 后 -t 出现在命令之后：%v", rule, flag, args)
					}
				}
			}
		}
	}
}

// TestTeardownRulesAreAppliedByEnsureNAT 确认回滚规则确实是下发过的规则。
// 漏删比删不掉更糟：接口消失后残留的 FORWARD 规则会一直留在 filter 表里。
func TestTeardownRulesAreAppliedByEnsureNAT(t *testing.T) {
	applied := make(map[string]bool)
	for _, r := range natRules("wg0", "10.8.0.0/24") {
		applied[strings.Join(withFlag("-A", r), " ")] = true
	}
	for _, r := range natTeardownRules("wg0", "10.8.0.0/24") {
		key := strings.Join(withFlag("-A", r), " ")
		if !applied[key] {
			t.Errorf("回滚规则不在下发集合中：%s", key)
		}
	}
}

// TestNATRulesCarrySubnetAndInterface 确认下发规则用的是实参而非硬编码，
// 避免多接口场景下 MASQUERADE 打到错误的网段。
func TestNATRulesCarrySubnetAndInterface(t *testing.T) {
	masq := strings.Join(natRules("wg1", "10.9.0.0/24")[0], " ")
	for _, want := range []string{"10.9.0.0/24", "wg1"} {
		if !strings.Contains(masq, want) {
			t.Errorf("MASQUERADE 规则缺少 %s：%s", want, masq)
		}
	}
	if strings.Contains(masq, "wg0") || strings.Contains(masq, "10.8.") {
		t.Errorf("MASQUERADE 规则混入了其他接口参数：%s", masq)
	}
}
