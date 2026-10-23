package ipam

import (
	"fmt"
	"net"
	"net/netip"
	"sort"
	"strings"
	"sync"
)

// Allocator 在一个 IPv4 网段内分配主机地址，并发安全。
type Allocator struct {
	mu     sync.Mutex
	prefix netip.Prefix
	first  netip.Addr // 首个候选主机地址（跳过网络地址）
	last   netip.Addr // 末个候选主机地址（跳过广播地址）
	used   map[string]struct{}
}

// NewAllocator 创建分配器。prefix 应为 /24 ~ /29 这类小网段。
// used 为已被占用的地址集合（可为 nil），键为不含掩码的 IP。
func NewAllocator(prefix netip.Prefix, used map[string]struct{}) (*Allocator, error) {
	if !prefix.IsValid() || !prefix.Addr().Is4() {
		return nil, fmt.Errorf("ipam: prefix must be a valid IPv4 CIDR, got %q", prefix)
	}
	bits := prefix.Bits()
	if bits < 24 || bits > 30 {
		return nil, fmt.Errorf("ipam: prefix length /%d out of supported range [/24, /30]", bits)
	}

	masked := netip.PrefixFrom(prefix.Addr(), bits).Masked()
	hostBits := 32 - bits
	// 候选区间为 [network+1, broadcast-1]；hostBits<=1 时无可分配地址。
	if hostBits < 2 {
		return nil, fmt.Errorf("ipam: subnet %s is too small to host any client", masked)
	}
	first := masked.Addr().Next()
	last := lastAddr(masked.Addr(), hostBits)

	if used == nil {
		used = map[string]struct{}{}
	}
	return &Allocator{
		prefix: masked,
		first:  first,
		last:   last,
		used:   used,
	}, nil
}

// lastAddr 计算掩码内的最大地址（广播地址），分配循环以它为开区间上界。
func lastAddr(network netip.Addr, hostBits int) netip.Addr {
	v4 := network.As4()
	raw := uint32(v4[0])<<24 | uint32(v4[1])<<16 | uint32(v4[2])<<8 | uint32(v4[3])
	raw |= (1 << uint(hostBits)) - 1
	return netip.AddrFrom4([4]byte{
		byte(raw >> 24), byte(raw >> 16), byte(raw >> 8), byte(raw),
	})
}

// Next 分配下一个可用地址，返回 host/32 形式的 CIDR。
func (a *Allocator) Next() (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	for addr := a.first; addr.IsValid() && addr.Compare(a.last) < 0; addr = addr.Next() {
		candidate := addr.String()
		if _, taken := a.used[candidate]; !taken {
			a.used[candidate] = struct{}{}
			return candidate + "/32", nil
		}
	}
	return "", fmt.Errorf("ipam: address pool exhausted in %s", a.prefix)
}

// Reserve 预占指定地址。已被占用时返回 false。
func (a *Allocator) Reserve(addr string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	normalized := HostAddress(addr)
	if _, taken := a.used[normalized]; taken {
		return false
	}
	a.used[normalized] = struct{}{}
	return true
}

// Release 释放地址。
func (a *Allocator) Release(addr string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.used, HostAddress(addr))
}

// Prefix 返回当前网段。
func (a *Allocator) Prefix() netip.Prefix { return a.prefix }

// Capacity 返回可分配地址总数。
func (a *Allocator) Capacity() int {
	n := 0
	for addr := a.first; addr.IsValid() && addr.Compare(a.last) < 0; addr = addr.Next() {
		n++
	}
	return n
}

// HostAddress 从 CIDR 或裸 IP 中提取主机地址，"10.8.0.2/32" 与 "10.8.0.2" 都返回 "10.8.0.2"。
func HostAddress(addr string) string {
	entry := strings.TrimSpace(addr)
	if p, err := netip.ParsePrefix(entry); err == nil {
		return p.Addr().String()
	}
	if a, err := netip.ParseAddr(entry); err == nil {
		return a.String()
	}
	if ip := net.ParseIP(entry); ip != nil {
		return ip.String()
	}
	return entry
}

// NormalizeAddresses 把 CIDR 列表归一化为不含掩码的地址集合，去重后按序返回。
func NormalizeAddresses(list []string) []string {
	out := make([]string, 0, len(list))
	seen := make(map[string]struct{}, len(list))
	for _, item := range list {
		addr := HostAddress(item)
		if addr == "" {
			continue
		}
		if _, dup := seen[addr]; dup {
			continue
		}
		seen[addr] = struct{}{}
		out = append(out, addr)
	}
	sort.Strings(out)
	return out
}

// InPrefix 判断地址是否落在给定网段内。
func InPrefix(addr, cidr string) bool {
	a, err := netip.ParseAddr(HostAddress(addr))
	if err != nil {
		return false
	}
	network, err := netip.ParsePrefix(strings.TrimSpace(cidr))
	if err != nil {
		return false
	}
	return network.Contains(a)
}
