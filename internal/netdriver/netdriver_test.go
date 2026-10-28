package netdriver

import "testing"

func TestSubnetOfMasksHostBits(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"10.8.0.1/24", "10.8.0.0/24"},
		{"10.8.0.37/24", "10.8.0.0/24"},
		{"192.168.1.5/30", "192.168.1.4/30"},
		{"10.0.0.1/32", "10.0.0.1/32"},
		{"fd00::1/64", "fd00::/64"},
	}
	for _, c := range cases {
		got, err := SubnetOf(c.in)
		if err != nil {
			t.Fatalf("SubnetOf(%q): %v", c.in, err)
		}
		if got != c.want {
			t.Errorf("SubnetOf(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSubnetOfRejectsGarbage(t *testing.T) {
	for _, in := range []string{"", "10.8.0.1", "not-an-ip/24", "10.8.0.1/99"} {
		if _, err := SubnetOf(in); err == nil {
			t.Errorf("SubnetOf(%q) 应返回错误", in)
		}
	}
}

func TestResolveTable(t *testing.T) {
	cases := []struct {
		in      string
		wantID  int
		wantOn  bool
		wantErr bool
	}{
		{"", mainTable, true, false},
		{"auto", mainTable, true, false},
		{"AUTO", mainTable, true, false},
		{"off", 0, false, false},
		{"OFF", 0, false, false},
		{"51820", 51820, true, false},
		{"0", 0, false, true},
		{"wgpanel", 0, false, true},
	}
	for _, c := range cases {
		id, on, err := resolveTable(c.in)
		if (err != nil) != c.wantErr {
			t.Errorf("resolveTable(%q) err = %v, wantErr %v", c.in, err, c.wantErr)
			continue
		}
		if err != nil {
			continue
		}
		if id != c.wantID || on != c.wantOn {
			t.Errorf("resolveTable(%q) = (%d, %v), want (%d, %v)", c.in, id, on, c.wantID, c.wantOn)
		}
	}
}

func TestSplitListTrimsAndSkipsEmpty(t *testing.T) {
	got := splitList(" 10.0.0.0/24 , ,10.8.0.0/24 ")
	if len(got) != 2 || got[0] != "10.0.0.0/24" || got[1] != "10.8.0.0/24" {
		t.Errorf("splitList = %#v", got)
	}
}

func TestCapabilitiesUsableOnlyNeedsNetlink(t *testing.T) {
	// 内核 WireGuard 接口由 netlink 创建，不需要 /dev/net/tun。
	c := Capabilities{Netlink: true}
	if !c.Usable() {
		t.Error("Netlink 可用时应判定为 Usable")
	}
	if (Capabilities{}).Usable() {
		t.Error("缺少 Netlink 时不应判定为 Usable")
	}
}

func TestCapabilitiesProblems(t *testing.T) {
	full := Capabilities{Netlink: true, TUNDevice: true, Iptables: true, Tc: true, Forwarding: true}
	if got := (Capabilities{}).Problems(ModeManaged); len(got) == 0 {
		t.Error("无任何能力时应报告问题")
	}
	if got := full.Problems(ModeManaged); len(got) != 0 {
		t.Errorf("能力齐备时不应报告问题，得到 %#v", got)
	}
	// 缺 tc 只影响限速，不应与缺 netlink 同等严重。
	if got := (Capabilities{Netlink: true, TUNDevice: true, Iptables: true, Forwarding: true}).Problems(ModeManaged); len(got) != 1 {
		t.Errorf("仅缺 tc 时应报告且只报告一个问题，得到 %#v", got)
	}
	// host 模式下转发与 NAT 归属宿主，缺这两项不算面板的能力缺口。
	if got := (Capabilities{Netlink: true, TUNDevice: true, Tc: true}).Problems(ModeHost); len(got) != 0 {
		t.Errorf("host 模式不应因缺 iptables/forwarding 报问题，得到 %#v", got)
	}
	// host 模式下缺 netlink 依然致命。
	if got := (Capabilities{TUNDevice: true, Tc: true}).Problems(ModeHost); len(got) != 1 {
		t.Errorf("host 模式缺 netlink 仍应报问题，得到 %#v", got)
	}
	// host 模式下缺 tc 仍要报，因为它与宿主无关。
	if got := (Capabilities{Netlink: true, TUNDevice: true}).Problems(ModeHost); len(got) != 1 {
		t.Errorf("host 模式缺 tc 仍应报问题，得到 %#v", got)
	}
}

func TestParseMode(t *testing.T) {
	// managed（自管 netns）：K8s Pod 与 systemd 裸机形态。
	for _, in := range []string{"managed", "Managed", " managed "} {
		m, err := ParseMode(in)
		if err != nil || m != ModeManaged {
			t.Errorf("ParseMode(%q) = (%v, %v)", in, m, err)
		}
	}
	for _, in := range []string{"host", "Host"} {
		m, err := ParseMode(in)
		if err != nil || m != ModeHost {
			t.Errorf("ParseMode(%q) = (%v, %v)", in, m, err)
		}
	}
	// L2 直挂模式：解析成功且 IsL2 为真。
	for _, in := range []string{"ipvlan", "IPVLAN", "macvlan", " MacVlan "} {
		m, err := ParseMode(in)
		if err != nil || !m.IsL2() {
			t.Errorf("ParseMode(%q) = (%v, %v)，期望 L2 模式", in, m, err)
		}
	}
	// bridge 部署模式已移除：必须报错而不是静默降级成别的模式。
	for _, in := range []string{"bridge", "BRIDGE", " bridge "} {
		if _, err := ParseMode(in); err == nil {
			t.Errorf("ParseMode(%q) 应返回错误（bridge 模式已移除）", in)
		}
	}
	for _, in := range []string{"", "none", "gre"} {
		if _, err := ParseMode(in); err == nil {
			t.Errorf("ParseMode(%q) 应返回错误", in)
		}
	}
}
