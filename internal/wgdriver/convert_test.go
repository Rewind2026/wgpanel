package wgdriver

import (
	"net"
	"strings"
	"testing"

	"github.com/Rewind2026/wgpanel/internal/domain"
)

// keyA/keyB 是两个合法的 base64 x25519 公钥，用于构造互不相同的 peer。
const (
	keyA = "IKWEQunHXDSjyLwRHfh9QJx33FLgjUmQMFIIHxT5cQE="
	keyB = "qlKzvIzVOjcD9L0hez6aALdIYF4dcHr6D9nSBU5y7mY="
)

func allowedStrings(t *testing.T, ips []net.IPNet) []string {
	t.Helper()
	out := make([]string, 0, len(ips))
	for _, n := range ips {
		out = append(out, n.String())
	}
	return out
}

// 服务端 [Peer] 的 AllowedIPs 必须来自 Address。WireGuard 内核要求它在所有
// peer 之间唯一，若下发客户端侧的 0.0.0.0/0，内核只保留一个，其余变成 (none)。
func TestToPeerConfigUsesAddressNotClientAllowedIPs(t *testing.T) {
	p := domain.Peer{
		ID:                  1,
		Name:                "laptop",
		PublicKey:           keyA,
		Address:             "10.8.0.2/32",
		AllowedIPs:          "0.0.0.0/0,::/0",
		PersistentKeepalive: 25,
	}

	pc, err := toPeerConfig(p)
	if err != nil {
		t.Fatalf("toPeerConfig: %v", err)
	}

	got := allowedStrings(t, pc.AllowedIPs)
	if len(got) != 1 || got[0] != "10.8.0.2/32" {
		t.Fatalf("服务端 AllowedIPs 应为 peer 自身地址 10.8.0.2/32，实际 %v", got)
	}
}

// 多个 peer 必须得到互不重叠的服务端 AllowedIPs，否则内核无法同时满足。
func TestToPeerConfigsProduceDisjointAllowedIPs(t *testing.T) {
	peers := []domain.Peer{
		{ID: 1, Name: "a", PublicKey: keyA, Address: "10.8.0.2/32", AllowedIPs: "0.0.0.0/0,::/0", PersistentKeepalive: 25},
		{ID: 2, Name: "b", PublicKey: keyB, Address: "10.8.0.3/32", AllowedIPs: "0.0.0.0/0,::/0", PersistentKeepalive: 25},
	}

	cfgs, err := toPeerConfigs(peers)
	if err != nil {
		t.Fatalf("toPeerConfigs: %v", err)
	}

	seen := make(map[string]string, len(cfgs))
	for i, pc := range cfgs {
		for _, cidr := range allowedStrings(t, pc.AllowedIPs) {
			if other, dup := seen[cidr]; dup {
				t.Fatalf("peer %d 与 %s 的服务端 AllowedIPs 重复: %s", i, other, cidr)
			}
			seen[cidr] = peers[i].Name
		}
	}
}

// Address 是服务端 AllowedIPs 的唯一来源，因此必须是合法 CIDR。
func TestToPeerConfigRejectsInvalidAddress(t *testing.T) {
	p := domain.Peer{
		ID:                  7,
		Name:                "bad",
		PublicKey:           keyA,
		Address:             "not-an-ip",
		PersistentKeepalive: 25,
	}

	if _, err := toPeerConfig(p); err == nil {
		t.Fatal("非法 Address 应导致转换失败，而不是静默下发空 AllowedIPs")
	}
	if _, err := toPeerConfig(p); err != nil && !strings.Contains(err.Error(), "peer 7") {
		t.Errorf("错误信息应指明是哪个 peer，实际 %v", err)
	}
}

// RenderServerConfig 与 toPeerConfig 必须对同一字段达成一致，
// 否则导出的配置文件和实际运行的内核态会互相矛盾。
func TestRenderServerConfigMatchesKernelAllowedIPs(t *testing.T) {
	peers := []domain.Peer{
		{ID: 1, Name: "a", PublicKey: keyA, Address: "10.8.0.2/32", AllowedIPs: "0.0.0.0/0,::/0", PersistentKeepalive: 25},
	}
	cfg := &domain.ServerConfig{
		Interface:   "wg0",
		PrivateKey:  "wF3IEs79GkFV3FQ5RQzGJKcbOGhZB3MHtUO2O8OsyOA=",
		AddressCIDR: "10.8.0.1/24",
		ListenPort:  51820,
	}

	out := RenderServerConfig(cfg, peers)
	if !strings.Contains(out, "AllowedIPs = 10.8.0.2/32") {
		t.Errorf("导出的服务端配置应使用 peer 地址作为 AllowedIPs，实际输出:\n%s", out)
	}
	if strings.Contains(out, "AllowedIPs = 0.0.0.0/0,::/0") {
		t.Errorf("导出的服务端配置不应包含客户端侧的默认路由，实际输出:\n%s", out)
	}
}

// wg-quick 的 strip() 只剥离 Address/DNS/MTU/Table/Pre*/SaveConfig 等自有字段，
// 其余键会原样透传给 wg setconf。而 wg(8) 在 [Interface] 段只接受
// PrivateKey、ListenPort、FwMark，出现别的键会报 `Line unrecognized`
// 并让客户端/服务端根本无法启动。这里锁定这条约束。
var interfaceSectionKeysAllowedByWg = map[string]bool{
	"PrivateKey":   true,
	"ListenPort":   true,
	"FwMark":       true,
	"ReplacePeers": true,
	// 以下由 wg-quick 处理，会被 strip() 剥离
	"Address": true, "DNS": true, "MTU": true, "Table": true,
	"PreUp": true, "PostUp": true, "PreDown": true, "PostDown": true,
	"SaveConfig": true,
}

var peerSectionKeysAllowedByWg = map[string]bool{
	"PublicKey":           true,
	"PresharedKey":        true,
	"AllowedIPs":          true,
	"Endpoint":            true,
	"PersistentKeepalive": true,
}

// assertSectionKeys 校验渲染结果中每个键都落在 wg(8) 认可的段位里。
func assertSectionKeys(t *testing.T, out string) {
	t.Helper()
	section := ""
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			section = line
			continue
		}
		key := line
		if i := strings.Index(line, "="); i >= 0 {
			key = strings.TrimSpace(line[:i])
		}
		switch section {
		case "[Interface]":
			if !interfaceSectionKeysAllowedByWg[key] {
				t.Errorf("[Interface] 段出现 wg setconf 不接受的键 %q:\n%s", key, out)
			}
		case "[Peer]":
			if !peerSectionKeysAllowedByWg[key] {
				t.Errorf("[Peer] 段出现 wg setconf 不接受的键 %q:\n%s", key, out)
			}
		}
	}
}

// 客户端配置里 AllowedIPs/Endpoint/PersistentKeepalive/PresharedKey 描述的是
// 与服务端的关系，必须在 [Peer]；写进 [Interface] 会让 wg-quick 启动失败。
func TestRenderPeerConfigPlacesKeysInCorrectSection(t *testing.T) {
	server := &domain.ServerConfig{
		PublicKey:    keyA,
		PresharedKey: "GFJiA0pR9xkYcMvHtL5sQwBn1uJdGfXaEoZrTyUiOpAs=",
		DNS:          "1.1.1.1,8.8.8.8",
		MTU:          1420,
		ListenPort:   51820,
	}
	peer := &domain.Peer{
		ID:                  1,
		Name:                "laptop",
		PrivateKey:          "wF3IEs79GkFV3FQ5RQzGJKcbOGhZB3MHtUO2O8OsyOA=",
		PresharedKey:        "GFJiA0pR9xkYcMvHtL5sQwBn1uJdGfXaEoZrTyUiOpAs=",
		PublicKey:           keyB,
		Address:             "10.8.0.2/32",
		AllowedIPs:          "0.0.0.0/0,::/0",
		PersistentKeepalive: 25,
	}

	out := RenderPeerConfig(server, peer, "192.168.50.63")
	assertSectionKeys(t, out)

	// 客户端必须真的拿到全隧道路由和 endpoint，否则隧道静默不通。
	// [Peer] 描述的是「客户端 ↔ 服务端」这条关系，因此公钥是服务端的。
	if !strings.Contains(out, "[Peer]\nPublicKey = "+keyA) {
		t.Errorf("[Peer] 段应以服务端公钥开头，实际输出:\n%s", out)
	}
	if !strings.Contains(out, "AllowedIPs = 0.0.0.0/0,::/0") {
		t.Errorf("客户端配置缺少全隧道路由，实际输出:\n%s", out)
	}
	if !strings.Contains(out, "Endpoint = 192.168.50.63:51820") {
		t.Errorf("客户端配置缺少 endpoint，实际输出:\n%s", out)
	}
	if !strings.Contains(out, "PersistentKeepalive = 25") {
		t.Errorf("客户端配置缺少 keepalive，实际输出:\n%s", out)
	}
	// PSK 只能出现一次，且必须与内核下发的 peer.PresharedKey 一致。
	if n := strings.Count(out, "PresharedKey = "); n != 1 {
		t.Errorf("客户端配置的 PSK 应恰好出现 1 次，实际 %d 次:\n%s", n, out)
	}
	if !strings.Contains(out, "PresharedKey = GFJiA0pR9xkYcMvHtL5sQwBn1uJdGfXaEoZrTyUiOpAs=") {
		t.Errorf("客户端配置的 PSK 必须与服务端内核下发的一致，实际输出:\n%s", out)
	}
}

// 服务端配置同样不能让非法键落进 [Interface]。
func TestRenderServerConfigPlacesKeysInCorrectSection(t *testing.T) {
	cfg := &domain.ServerConfig{
		Interface:           "wg0",
		PrivateKey:          "wF3IEs79GkFV3FQ5RQzGJKcbOGhZB3MHtUO2O8OsyOA=",
		PublicKey:           keyA,
		PresharedKey:        "GFJiA0pR9xkYcMvHtL5sQwBn1uJdGfXaEoZrTyUiOpAs=",
		AddressCIDR:         "10.8.0.1/24",
		ListenPort:          51820,
		MTU:                 1420,
		FwMark:              0x100,
		Table:               "auto",
		AllowedIPs:          "0.0.0.0/0",
		PersistentKeepalive: 25,
		DNS:                 "1.1.1.1",
		SaveConfig:          true,
	}
	peers := []domain.Peer{
		{ID: 1, Name: "a", PublicKey: keyA, Address: "10.8.0.2/32"},
		{ID: 2, Name: "b", PublicKey: keyB, Address: "10.8.0.3/32"},
	}

	out := RenderServerConfig(cfg, peers)
	assertSectionKeys(t, out)

	// 接口级 AllowedIPs 不能出现在 [Interface]，服务端路由由各 peer 的地址决定。
	if idx := strings.Index(out, "[Peer]"); idx > 0 {
		if strings.Contains(out[:idx], "AllowedIPs") {
			t.Errorf("[Interface] 段不应出现 AllowedIPs，实际输出:\n%s", out)
		}
	}
}

// endpoint 参数由管理员填写，它可能自带端口，也可能是不带端口的裸地址，
// IPv6 还得加方括号。这三种形态拼接规则完全不同，
// 统一按 "endpoint:ListenPort" 拼会产出 172.17.0.2:51820:51820 这种
// wg setconf 直接拒绝的配置——客户端表现为「配置导入成功但连不上」，
// 且面板侧没有任何报错可查。
func TestRenderPeerConfigEndpointJoining(t *testing.T) {
	server := &domain.ServerConfig{
		PublicKey:  keyA,
		ListenPort: 51820,
	}
	peer := &domain.Peer{
		ID:         1,
		Name:       "laptop",
		PrivateKey: "wF3IEs79GkFV3FQ5RQzGJKcbOGhZB3MHtUO2O8OsyOA=",
		PublicKey:  keyB,
		Address:    "10.8.0.2/32",
		AllowedIPs: "10.8.0.0/24",
	}

	cases := []struct {
		name     string
		endpoint string
		want     string
	}{
		{"裸 IPv4 补端口", "192.168.50.63", "Endpoint = 192.168.50.63:51820"},
		{"已带端口不再追加", "172.17.0.2:51820", "Endpoint = 172.17.0.2:51820"},
		{"自定义端口保留", "203.0.113.7:9999", "Endpoint = 203.0.113.7:9999"},
		{"裸 IPv6 加方括号补端口", "2001:db8::1", "Endpoint = [2001:db8::1]:51820"},
		{"已带端口的 IPv6 不重复加括号", "[2001:db8::1]:51820", "Endpoint = [2001:db8::1]:51820"},
		{"域名补端口", "vpn.example.com", "Endpoint = vpn.example.com:51820"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := RenderPeerConfig(server, peer, tc.endpoint)
			if !strings.Contains(out, tc.want) {
				t.Errorf("期望包含 %q，实际输出:\n%s", tc.want, out)
			}
			// 不能出现被重复追加端口或裸 IPv6 的畸形行。
			if strings.Contains(out, ":51820:") {
				t.Errorf("Endpoint 端口被重复追加，实际输出:\n%s", out)
			}
			for _, line := range strings.Split(out, "\n") {
				if strings.HasPrefix(line, "Endpoint = ") && strings.Count(line, ":") > 2 &&
					!strings.HasPrefix(line, "Endpoint = [") {
					t.Errorf("裸 IPv6 地址缺少方括号，wg 无法解析: %q", line)
				}
			}
		})
	}
}
