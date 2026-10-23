package wgdriver

import (
	"fmt"
	"net"
	"strings"
	"time"

	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"

	"github.com/Rewind2026/wgpanel/internal/domain"
)

// parseCIDRs 把逗号或空白分隔的 CIDR 列表解析为 net.IPNet。
func parseCIDRs(s string) ([]net.IPNet, error) {
	fields := strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\n' || r == '\t'
	})
	out := make([]net.IPNet, 0, len(fields))
	for _, f := range fields {
		if f == "" {
			continue
		}
		_, cidr, err := net.ParseCIDR(f)
		if err != nil {
			return nil, fmt.Errorf("invalid cidr %q: %w", f, err)
		}
		out = append(out, *cidr)
	}
	return out, nil
}

// renderCIDRs 将 IPNet 列表序列化回逗号分隔字符串。
func renderCIDRs(cidrs []net.IPNet) string {
	parts := make([]string, 0, len(cidrs))
	for _, c := range cidrs {
		parts = append(parts, c.String())
	}
	return strings.Join(parts, ", ")
}

// parseKey 解析 base64 编码的密钥。
func parseKey(s string) (wgtypes.Key, error) {
	return wgtypes.ParseKey(strings.TrimSpace(s))
}

// parseEndpoint 解析 host:port 形式的端点。
func parseEndpoint(s string) (*net.UDPAddr, error) {
	addr, err := net.ResolveUDPAddr("udp", strings.TrimSpace(s))
	if err != nil {
		return nil, fmt.Errorf("invalid endpoint %q: %w", s, err)
	}
	return addr, nil
}

// keepalivePtr 返回非 nil 的 keepalive 间隔指针；0 表示清除。
func keepalivePtr(seconds int) *time.Duration {
	if seconds < 0 {
		seconds = 0
	}
	d := time.Duration(seconds) * time.Second
	return &d
}

// toPeerConfig 把领域模型转换为 wgctrl 的 Peer 配置。
//
// 注意 AllowedIPs 用的是 p.Address 而不是 p.AllowedIPs。服务端 [Peer] 的
// AllowedIPs 是「哪些源地址的回包要交给这个 peer」，WireGuard 内核要求它在
// 所有 peer 之间唯一：把 0.0.0.0/0 写进多个 peer 时，内核只保留一个，其余
// 会被清成 (none)，表现为握手能成功但所有流量静默丢弃。
// p.AllowedIPs 描述的是客户端侧「哪些流量走隧道」，每个客户端各有一份接口，
// 允许重复，与服务端语义无关。参见 RenderServerConfig 对同一字段的处理。
func toPeerConfig(p domain.Peer) (wgtypes.PeerConfig, error) {
	pub, err := parseKey(p.PublicKey)
	if err != nil {
		return wgtypes.PeerConfig{}, fmt.Errorf("peer %d public key invalid: %w", p.ID, err)
	}
	allowed, err := parseCIDRs(p.Address)
	if err != nil {
		return wgtypes.PeerConfig{}, fmt.Errorf("peer %d: %w", p.ID, err)
	}
	pc := wgtypes.PeerConfig{
		PublicKey:                   pub,
		ReplaceAllowedIPs:           true,
		AllowedIPs:                  allowed,
		PersistentKeepaliveInterval: keepalivePtr(p.PersistentKeepalive),
	}
	if p.PresharedKey != "" {
		psk, err := parseKey(p.PresharedKey)
		if err != nil {
			return wgtypes.PeerConfig{}, fmt.Errorf("peer %d preshared key invalid: %w", p.ID, err)
		}
		pc.PresharedKey = &psk
	}
	if p.Endpoint != "" {
		ep, err := parseEndpoint(p.Endpoint)
		if err != nil {
			return wgtypes.PeerConfig{}, fmt.Errorf("peer %d: %w", p.ID, err)
		}
		pc.Endpoint = ep
	}
	return pc, nil
}

// toPeerConfigs 批量转换，任一 Peer 非法则整体失败。
func toPeerConfigs(peers []domain.Peer) ([]wgtypes.PeerConfig, error) {
	out := make([]wgtypes.PeerConfig, 0, len(peers))
	for _, p := range peers {
		pc, err := toPeerConfig(p)
		if err != nil {
			return nil, err
		}
		out = append(out, pc)
	}
	return out, nil
}
