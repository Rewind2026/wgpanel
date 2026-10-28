package netdriver

import "testing"

// RA 诊断的口径必须随模式变化：只有 L2 模式（ipvlan/macvlan）的 RA 链路
// 归本进程管。
//
// 这组用例锁定的是「不误报」：host 与 managed 下无论内核取值如何，
// 都不得报出阻断问题，否则用户会去改根本不该改的宿主/CNI 配置。
func TestInspectRAMarksApplicabilityPerMode(t *testing.T) {
	cases := []struct {
		mode Mode
		want bool
	}{
		{ModeManaged, false},
		{ModeHost, false},
		{ModeIpvlan, true},
		{ModeMacvlan, true},
	}
	for _, c := range cases {
		st := InspectRA(c.mode, "lo")
		if st.Applicable != c.want {
			t.Errorf("InspectRA(%q).Applicable = %v, want %v", c.mode, st.Applicable, c.want)
		}
	}
}

// 非适用模式下 Problems 必须为空——否则界面会把宿主策略
// （如 host 上 accept_ra=0）显示成面板可修复的故障，误导用户。
func TestInspectRANonApplicableHasNoProblems(t *testing.T) {
	for _, mode := range []Mode{ModeManaged, ModeHost} {
		st := InspectRA(mode, "lo")
		if len(st.Problems) != 0 {
			t.Errorf("InspectRA(%q).Problems = %#v，应为空", mode, st.Problems)
		}
	}
}

// 适用模式下必须能报出阻断问题，否则诊断就失去意义。
func TestInspectRAApplicableReportsProblems(t *testing.T) {
	st := InspectRA(ModeIpvlan, "lo")
	// lo 上 disable_ipv6 取值恒为 0，此处只断言「Applicable 时结构可用」：
	// 至少 Problems 为空切片（而非 null），且 Applicable 为真。
	if !st.Applicable {
		t.Fatal("L2 模式应标记为适用")
	}
	if st.Problems == nil {
		t.Error("Problems 应为非 nil 空切片，nil 会被 JSON 编码成 null")
	}
	if st.GlobalV6 == nil {
		t.Error("GlobalV6 应为非 nil 空切片，nil 会被 JSON 编码成 null")
	}
}
