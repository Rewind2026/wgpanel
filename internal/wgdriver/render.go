package wgdriver

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/Rewind2026/wgpanel/internal/domain"
)

// RenderServerConfig 生成服务端 wg-quick 配置，用于导出与外部工具兼容。
//
// 与 RenderPeerConfig 同理，[Interface] 段只写 wg 与 wg-quick 都认识的键：
// PrivateKey/ListenPort/FwMark 以及 wg-quick 自有的 Address/DNS/MTU/Table/
// SaveConfig。PublicKey 可由 PrivateKey 导出，接口级 PresharedKey、
// AllowedIPs、PersistentKeepalive 在 WireGuard 中没有对应语义
// （PSK 与 AllowedIPs 都是 per-peer 的），写在这里会让 wg setconf 直接拒绝。
func RenderServerConfig(cfg *domain.ServerConfig, peers []domain.Peer) string {
	var b strings.Builder

	b.WriteString("[Interface]\n")
	fmt.Fprintf(&b, "PrivateKey = %s\n", cfg.PrivateKey)
	fmt.Fprintf(&b, "Address = %s\n", cfg.AddressCIDR)
	if cfg.ListenPort > 0 {
		fmt.Fprintf(&b, "ListenPort = %d\n", cfg.ListenPort)
	}
	if cfg.MTU > 0 {
		fmt.Fprintf(&b, "MTU = %d\n", cfg.MTU)
	}
	if cfg.FwMark > 0 {
		fmt.Fprintf(&b, "FwMark = 0x%x\n", cfg.FwMark)
	}
	if cfg.Table != "" && cfg.Table != "off" {
		fmt.Fprintf(&b, "Table = %s\n", cfg.Table)
	}
	if cfg.DNS != "" {
		fmt.Fprintf(&b, "DNS = %s\n", cfg.DNS)
	}
	fmt.Fprintf(&b, "SaveConfig = %t\n", cfg.SaveConfig)

	for _, p := range peers {
		fmt.Fprintf(&b, "\n# %s\n", p.Name)
		b.WriteString("[Peer]\n")
		fmt.Fprintf(&b, "PublicKey = %s\n", p.PublicKey)
		if p.PresharedKey != "" {
			fmt.Fprintf(&b, "PresharedKey = %s\n", p.PresharedKey)
		}
		fmt.Fprintf(&b, "AllowedIPs = %s\n", p.Address)
	}

	return b.String()
}

// RenderPeerConfig 生成单个客户端可导入的 wg-quick 配置。
// endpoint 为服务端公网地址，形如 1.2.3.4；peer.Endpoint 优先。
//
// 段位必须严格对齐 wg(8) 的解析器：[Interface] 只接受 PrivateKey、ListenPort、
// FwMark；其余描述对端关系的键（AllowedIPs、Endpoint、PersistentKeepalive、
// PresharedKey）必须写在 [Peer]。wg-quick 的 strip 只剥离 Address/DNS/MTU/
// Table/Pre* 等自有字段，这些键会被原样透传给 wg setconf，写错段位会直接报
// `Line unrecognized`，导致客户端根本无法建立接口。
func RenderPeerConfig(server *domain.ServerConfig, peer *domain.Peer, endpoint string) string {
	var b strings.Builder

	b.WriteString("[Interface]\n")
	fmt.Fprintf(&b, "PrivateKey = %s\n", peer.PrivateKey)
	fmt.Fprintf(&b, "Address = %s\n", peer.Address)
	// 全隧道路由下客户端所有 DNS 查询都走隧道，必须显式下发，否则解析仍走本地 DNS。
	if server.DNS != "" {
		fmt.Fprintf(&b, "DNS = %s\n", server.DNS)
	}
	if server.MTU > 0 {
		fmt.Fprintf(&b, "MTU = %d\n", server.MTU)
	}

	b.WriteString("\n[Peer]\n")
	fmt.Fprintf(&b, "PublicKey = %s\n", server.PublicKey)
	// PSK 必须与 toPeerConfig 下发到内核的那一份一致，即 peer.PresharedKey。
	// server.PresharedKey 是接口级字段，从未通过 ConfigureDevice 下发，
	// 写进客户端配置不仅重复，还会让两端 PSK 不匹配导致握手永远失败。
	if peer.PresharedKey != "" {
		fmt.Fprintf(&b, "PresharedKey = %s\n", peer.PresharedKey)
	}
	// 客户端侧 AllowedIPs：哪些目的地的流量走隧道。它必须在这里——[Interface]
	// 段的同名键是非法的，写在这里客户端会启动失败。
	if peer.AllowedIPs != "" {
		fmt.Fprintf(&b, "AllowedIPs = %s\n", peer.AllowedIPs)
	}
	if peer.Endpoint == "" && endpoint != "" {
		fmt.Fprintf(&b, "Endpoint = %s\n", joinEndpointPort(endpoint, server.ListenPort))
	}
	if peer.PersistentKeepalive > 0 {
		fmt.Fprintf(&b, "PersistentKeepalive = %d\n", peer.PersistentKeepalive)
	}

	return b.String()
}

// joinEndpointPort 把管理员填写的 endpoint 与监听端口拼成 wg(8) 能解析的
// host:port 形态。
//
// endpoint 是用户输入，四种形态都合法且都会真的出现在生产里：
//   - 裸地址 "192.168.50.63" 补端口；
//   - 已带端口 "172.17.0.2:51820" 原样保留，不能再追加；
//   - 裸 IPv6 "2001:db8::1" 必须加方括号，否则 wg 无法分辨哪个冒号是端口分隔符；
//   - 带方括号 IPv6 "[2001:db8::1]:51820" 原样保留。
//
// 之前统一按 "%s:%d" 拼接，产出 172.17.0.2:51820:51820 与 2001:db8::1:51820
// 这两种畸形值；wg setconf 直接拒绝，而面板侧不报错，
// 用户看到的现象是「配置导出成功但客户端连不上」，极难定位。
func joinEndpointPort(endpoint string, port int) string {
	ep := strings.TrimSpace(endpoint)
	if strings.HasPrefix(ep, "[") {
		// [2001:db8::1] 或 [2001:db8::1]:51820：方括号已把 host 界定清楚，
		// 只要看右括号后面还有没有端口即可。
		if end := strings.LastIndex(ep, "]"); end >= 0 {
			if strings.Contains(ep[end+1:], ":") {
				return ep
			}
			return ep + ":" + strconv.Itoa(port)
		}
		return ep
	}
	// 裸 IPv6 的判定依据是「出现两个以上冒号」；IPv4 与域名最多一个。
	if strings.Count(ep, ":") > 1 {
		return "[" + ep + "]:" + strconv.Itoa(port)
	}
	if strings.Contains(ep, ":") {
		return ep
	}
	return ep + ":" + strconv.Itoa(port)
}
