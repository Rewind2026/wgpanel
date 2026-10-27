// netinfo 提供面向诊断的接口地址枚举与 IPv6 RA（路由器通告）状态检查。
//
// 背景：ipvlan/macvlan 模式下容器的 IPv6 不经 Docker IPAM，而是依赖
// 上游路由器以 RA/SLAAC 下发。这条链路有三颗内核开关必须同时满足：
//
//	disable_ipv6 = 0   接口已启用 IPv6（Docker 对无 v6 子网的网络默认置 1）
//	accept_ra    = 2   forwarding 开启时内核会忽略默认值 1 的 RA，必须显式放行
//	forwarding   = 1   面板做 VPN 转发的前提，但它反过来压制 accept_ra
//
// 任何一颗不对，eth0 就拿不到全局 IPv6 地址，外部设备无法直连 WireGuard。
// 这里把三颗开关的实际取值、可写性与地址结果一次性读出来，
// 供 /api/system/network 诊断展示；EnsureRA 额外尝试自愈前两颗。

package netdriver

import (
	"encoding/binary"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
)

// /proc/sys/net/ipv6/conf/<iface>/ 下的三颗开关。
const (
	raDisableIPv6 = "disable_ipv6"
	raAcceptRA    = "accept_ra"
	raForwarding  = "forwarding"
)

// IPv6 地址 flag 位（/proc/net/if_inet6 第五列，IF_INET6 内核文档定义）。
const (
	flagTemporary      = 0x01
	flagTentative      = 0x02
	flagDeprecated     = 0x04
	flagTentativeOpt   = 0x08
	flagDADFailed      = 0x10
	flagHomeAddress    = 0x20
	flagOptimistic     = 0x40
	flagPermanent      = 0x80
	// v6GlobalExclude 汇总「不能当对外 Endpoint」的 flag 组合：
	// DAD 尚未通过、已弃用或乐观重复地址检测中的地址都不可靠。
	// PERMANENT/TEMPORARY/HOMEADDRESS 是正常可用状态，予以保留。
	v6GlobalExclude = flagTentative | flagDeprecated | flagTentativeOpt | flagDADFailed | flagOptimistic
)

// InterfaceInfo 是单个接口的地址快照，用于诊断展示。
type InterfaceInfo struct {
	Name     string   `json:"name"`
	Addrs    []string `json:"addrs"`
	V4       []string `json:"v4"`
	V6Global []string `json:"v6Global"`
}

// RAStatus 是一个接口的 IPv6 RA/SLAAC 链路状态。
// 数值取不到时为 -1（接口不存在或 /proc 不可读）。
type RAStatus struct {
	Interface   string   `json:"interface"`
	DisableIPv6 int      `json:"disableIPv6"`
	AcceptRA    int      `json:"acceptRA"`
	Forwarding  int      `json:"forwarding"`
	// Writable 表示 sysctl 是否可写。容器内 /proc/sys 常被只读挂载，
	// 此时取值正确但无法自愈，必须把两种状态区分开。
	Writable bool     `json:"writable"`
	GlobalV6 []string `json:"globalV6"`
	// Problems 列出阻断 SLAAC 的配置问题（人类可读）。
	Problems []string `json:"problems,omitempty"`
	// Applied 列出本次自愈成功写入的项（EnsureRA 专用）。
	Applied []string `json:"applied,omitempty"`
}

// EnumerateInterfaces 枚举非回环接口的地址，按名称排序保证输出稳定。
func EnumerateInterfaces() []InterfaceInfo {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	exclude := map[string]bool{"lo": true}
	var out []InterfaceInfo
	for _, ifc := range ifaces {
		if exclude[ifc.Name] {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		info := InterfaceInfo{Name: ifc.Name}
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			cidr := ipn.String()
			info.Addrs = append(info.Addrs, cidr)
			if v4 := ipn.IP.To4(); v4 != nil {
				info.V4 = append(info.V4, v4.String())
				continue
			}
			if ipn.IP.IsGlobalUnicast() && isGlobalV6(ipn.IP) {
				info.V6Global = append(info.V6Global, ipn.IP.String())
			}
		}
		out = append(out, info)
	}
	return out
}

// DefaultUplink 返回默认路由所在的接口名。
// 没有默认路由时回退 eth0，再回退第一个非回环 UP 接口；都找不到返回空串。
func DefaultUplink() string {
	if name := uplinkFromRoute(); name != "" {
		return name
	}
	if _, err := net.InterfaceByName("eth0"); err == nil {
		return "eth0"
	}
	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	for _, ifc := range ifaces {
		if ifc.Name != "lo" && ifc.Flags&net.FlagUp != 0 {
			return ifc.Name
		}
	}
	return ""
}

// UplinkAddresses 返回默认路由接口上的候选 Endpoint 地址：
// v4 为该接口全部 IPv4（去掩码），v6 为全部全局 IPv6（去掩码、剔除 DAD 未通过等不可靠地址）。
func UplinkAddresses() (iface string, v4 []string, v6 []string) {
	iface = DefaultUplink()
	if iface == "" {
		return "", nil, nil
	}
	ifc, err := net.InterfaceByName(iface)
	if err != nil {
		return iface, nil, nil
	}
	addrs, err := ifc.Addrs()
	if err != nil {
		return iface, nil, nil
	}
	// /proc/net/if_inet6 的 flag 信息按 (接口, 十六进制地址) 索引，
	// net.Interface.Addrs 拿不到这些 flag，必须合表比对。
	bad := v6FlagsByInterface()[iface]
	for _, a := range addrs {
		ipn, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		if ipv4 := ipn.IP.To4(); ipv4 != nil {
			v4 = append(v4, ipv4.String())
			continue
		}
		if !ipn.IP.IsGlobalUnicast() || !isGlobalV6(ipn.IP) {
			continue
		}
		if bad[hexAddr(ipn.IP)] {
			continue
		}
		v6 = append(v6, ipn.IP.String())
	}
	return iface, v4, v6
}

// InspectRA 只读检查接口的 RA 链路状态，不产生任何副作用。
func InspectRA(iface string) RAStatus {
	st := RAStatus{
		Interface:   iface,
		DisableIPv6: readIPv6Sysctl(iface, raDisableIPv6),
		AcceptRA:    readIPv6Sysctl(iface, raAcceptRA),
		Forwarding:  readIPv6Sysctl(iface, raForwarding),
		Writable:    raWritable(iface),
		GlobalV6:    interfaceGlobalV6(iface),
	}
	st.Problems = raProblems(st)
	return st
}

// EnsureRA 在 InspectRA 的基础上尝试自愈：
// 把 disable_ipv6 归零、accept_ra 提到 2。/proc/sys 只读时写入失败，
// 问题原样记入 Problems——此时修复手段是 compose 的 sysctls/endpoint sysctls，
// 容器内已无能为力。
func EnsureRA(iface string) RAStatus {
	st := InspectRA(iface)
	if !st.Writable {
		return st
	}
	if st.DisableIPv6 > 0 {
		if err := writeIPv6Sysctl(iface, raDisableIPv6, "0"); err != nil {
			st.Problems = append(st.Problems, fmt.Sprintf("写入 %s=0 失败：%v", raDisableIPv6, err))
		} else {
			st.Applied = append(st.Applied, raDisableIPv6+"=0")
			st.DisableIPv6 = 0
		}
	}
	if st.AcceptRA >= 0 && st.AcceptRA != 2 {
		if err := writeIPv6Sysctl(iface, raAcceptRA, "2"); err != nil {
			st.Problems = append(st.Problems, fmt.Sprintf("写入 %s=2 失败：%v", raAcceptRA, err))
		} else {
			st.Applied = append(st.Applied, raAcceptRA+"=2")
			st.AcceptRA = 2
		}
	}
	st.GlobalV6 = interfaceGlobalV6(iface)
	st.Problems = raProblems(st)
	return st
}

// raProblems 把取值翻译成可读的故障描述。
func raProblems(st RAStatus) []string {
	var out []string
	if st.DisableIPv6 < 0 {
		out = append(out, "无法读取 IPv6 sysctl（接口不存在或 /proc 不可读）")
		return out
	}
	if st.DisableIPv6 > 0 {
		out = append(out, "disable_ipv6=1：接口已禁用 IPv6，RA 被忽略，需以 endpoint sysctls 打开")
	}
	if st.AcceptRA == 0 {
		out = append(out, "accept_ra=0：RA 已被显式关闭")
	}
	if st.AcceptRA == 1 && st.Forwarding > 0 {
		out = append(out, "accept_ra=1 且 forwarding=1：内核会丢弃 RA，需提升为 accept_ra=2")
	}
	if len(st.Problems) == 0 && len(out) == 0 && len(st.GlobalV6) == 0 {
		out = append(out, "sysctl 正常但尚未拿到全局 IPv6 地址：等下一轮 RA，或上游路由器未下发")
	}
	return out
}

// uplinkFromRoute 从 /proc/net/route 找默认路由（目标 0.0.0.0）的出接口。
func uplinkFromRoute() string {
	data, err := os.ReadFile("/proc/net/route")
	if err != nil {
		return ""
	}
	for i, line := range strings.Split(string(data), "\n") {
		if i == 0 || line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		if fields[1] == "00000000" {
			return fields[0]
		}
	}
	return ""
}

// v6FlagsByInterface 解析 /proc/net/if_inet6，返回 接口 → (十六进制地址 → 是否带排除 flag)。
// 该文件仅 Linux 提供；读取失败时返回空表，等价于不做 flag 过滤。
func v6FlagsByInterface() map[string]map[string]bool {
	data, err := os.ReadFile("/proc/net/if_inet6")
	if err != nil {
		return nil
	}
	out := map[string]map[string]bool{}
	for i, line := range strings.Split(string(data), "\n") {
		if i == 0 || line == "" {
			continue
		}
		fields := strings.Fields(line)
		// 格式：地址 序号 前缀长度 scope flags 接口名
		if len(fields) < 6 {
			continue
		}
		flags, err := strconv.ParseUint(fields[4], 16, 32)
		if err != nil {
			continue
		}
		if flags&v6GlobalExclude == 0 {
			continue
		}
		if out[fields[5]] == nil {
			out[fields[5]] = map[string]bool{}
		}
		out[fields[5]][fields[0]] = true
	}
	return out
}

// interfaceGlobalV6 返回接口上全部可用的全局 IPv6 地址（十进制点分形式）。
func interfaceGlobalV6(iface string) []string {
	ifc, err := net.InterfaceByName(iface)
	if err != nil {
		return nil
	}
	addrs, err := ifc.Addrs()
	if err != nil {
		return nil
	}
	bad := v6FlagsByInterface()[iface]
	var out []string
	for _, a := range addrs {
		ipn, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		if ipn.IP.To4() != nil || !ipn.IP.IsGlobalUnicast() || !isGlobalV6(ipn.IP) {
			continue
		}
		if bad[hexAddr(ipn.IP)] {
			continue
		}
		out = append(out, ipn.IP.String())
	}
	return out
}

// readIPv6Sysctl 读取单个接口级 IPv6 sysctl，取不到返回 -1。
func readIPv6Sysctl(iface, name string) int {
	v, err := os.ReadFile(fmt.Sprintf("/proc/sys/net/ipv6/conf/%s/%s", iface, name))
	if err != nil {
		return -1
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(v)))
	if err != nil {
		return -1
	}
	return n
}

// raWritable 探测接口级 sysctl 是否可写：只写方式打开成功即认为可写，
// 立即关闭不改变取值。
func raWritable(iface string) bool {
	f, err := os.OpenFile(fmt.Sprintf("/proc/sys/net/ipv6/conf/%s/%s", iface, raAcceptRA), os.O_WRONLY, 0)
	if err != nil {
		return false
	}
	_ = f.Close()
	return true
}

// writeIPv6Sysctl 写入单个接口级 IPv6 sysctl。
func writeIPv6Sysctl(iface, name, value string) error {
	return os.WriteFile(fmt.Sprintf("/proc/sys/net/ipv6/conf/%s/%s", iface, name), []byte(value), 0)
}

// hexAddr 把 IPv4/IPv6 地址转成 /proc/net/if_inet6 风格的 32 位十六进制小写串。
func hexAddr(ip net.IP) string {
	if v4 := ip.To4(); v4 != nil {
		// /proc/net/if_inet6 只有 v6 条目；v4 不会进来，防御性返回空串。
		return ""
	}
	b := ip.To16()
	if b == nil {
		return ""
	}
	// 内核按大端输出每组 16 位，与 net.IP 的内存布局一致，直接逐组编码。
	var sb strings.Builder
	for i := 0; i < 16; i += 2 {
		var group [2]byte
		group[0], group[1] = b[i], b[i+1]
		sb.WriteString(fmt.Sprintf("%04x", binary.BigEndian.Uint16(group[:])))
	}
	return sb.String()
}

// isGlobalV6 报告是否为 2000::/3 全局单播（GUA）。
func isGlobalV6(ip net.IP) bool {
	return len(ip) == net.IPv6len && ip[0]&0xE0 == 0x20
}
