package scheduler

import (
	"net"
	"testing"
)

// parseIPForTest 是测试里的 IP 解析辅助函数。
func parseIPForTest(s string) net.IP { return net.ParseIP(s) }

// normalizeEndpoint 必须把「客户端路由不到」的地址全部拒掉。
//
// 真机上暴露的缺陷：隧道地址 10.8.0.1 与容器 bridge 地址 172.17.0.3
// 都被当成公网 Endpoint 写进客户端配置，客户端因此永远建不起连接，
// 而面板没有任何提示——用户只会看到握手失败。
func TestNormalizeEndpointRejectsUnreachableAddresses(t *testing.T) {
	rejected := []string{
		"",
		"   ",
		"10.8.0.1",      // wg 隧道地址
		"10.8.0.1/24",   // 带掩码
		"172.17.0.3",    // Docker bridge
		"192.168.1.10",  // RFC1918
		"127.0.0.1",     // loopback
		"0.0.0.0",       // unspecified
		"fe80::1",       // link-local
		"fe80::1%wg0",   // 带 zone
		"fd00::1",       // ULA
		"not-an-ip",     // 非 IP
		"1.2.3.4:51820", // 带端口，不是地址
	}
	for _, in := range rejected {
		if got := normalizeEndpoint(in); got != "" {
			t.Errorf("normalizeEndpoint(%q) = %q，期望空串", in, got)
		}
	}
}

func TestNormalizeEndpointAcceptsPublicAddresses(t *testing.T) {
	accepted := map[string]string{
		"203.0.113.10": "203.0.113.10",
		"  8.8.8.8   ": "8.8.8.8",
		"2001:db8::1":  "2001:db8::1",
	}
	for in, want := range accepted {
		if got := normalizeEndpoint(in); got != want {
			t.Errorf("normalizeEndpoint(%q) = %q，期望 %q", in, got, want)
		}
	}
}

// isPrivateIP 只认 RFC1918/4193，未指定地址与私网都应判为不可用。
func TestIsPrivateIP(t *testing.T) {
	private := []string{"10.0.0.1", "172.16.0.1", "192.168.0.1", "fd00::1", "0.0.0.0"}
	for _, in := range private {
		ip := parseIPForTest(in)
		if ip == nil {
			t.Fatalf("测试用例 %q 不是合法 IP", in)
		}
		if !isPrivateIP(ip) {
			t.Errorf("%q 应判定为私网/不可用", in)
		}
	}
	public := []string{"8.8.8.8", "203.0.113.1", "2001:db8::1"}
	for _, in := range public {
		ip := parseIPForTest(in)
		if isPrivateIP(ip) {
			t.Errorf("%q 是公网地址，不应被判为私网", in)
		}
	}
}

// normalizeLANEndpoint 是 L2/host 模式上行接口地址的专用规整：
// 放行私网 v4（局域网客户端可达），其余不可达形态依旧拒绝。
func TestNormalizeLANEndpoint(t *testing.T) {
	accepted := map[string]string{
		"192.168.50.2":  "192.168.50.2",
		"10.0.0.5":      "10.0.0.5",
		" 203.0.113.7 ": "203.0.113.7",
	}
	for in, want := range accepted {
		if got := normalizeLANEndpoint(in); got != want {
			t.Errorf("normalizeLANEndpoint(%q) = %q，期望 %q", in, got, want)
		}
	}
	rejected := []string{
		"",
		"192.168.50.2/24", // 带掩码
		"127.0.0.1",       // 回环
		"169.254.1.1",     // 链路本地
		"0.0.0.0",         // 未指定
		"fe80::1",         // IPv6 不走这一级（由 normalizeEndpoint 处理）
		"not-an-ip",
	}
	for _, in := range rejected {
		if got := normalizeLANEndpoint(in); got != "" {
			t.Errorf("normalizeLANEndpoint(%q) = %q，期望空串", in, got)
		}
	}
}
