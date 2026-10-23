package config

import (
	"fmt"
	"net/netip"
)

// parseCIDR 解析并校验 CIDR 字符串。
func parseCIDR(s string) (netip.Prefix, netip.Addr, error) {
	p, err := netip.ParsePrefix(s)
	if err != nil {
		return netip.Prefix{}, netip.Addr{}, err
	}
	if !p.IsValid() {
		return netip.Prefix{}, netip.Addr{}, fmt.Errorf("invalid prefix")
	}
	return p, p.Addr(), nil
}
