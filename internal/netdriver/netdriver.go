// Package netdriver 负责容器网络命名空间内的链路、地址、路由与 NAT 配置。
//
// 与 wgdriver 的分工：
//   - wgdriver 只负责 WireGuard 控制面（密钥、监听端口、Peer、握手与计数器）。
//   - netdriver 负责「让这个接口真的能收发包」：创建 wireguard 类型链路、
//     绑定地址、设置 MTU、拉起链路、开启转发、下发路由与 NAT。
//
// 所有方法必须是幂等的：调谐器会反复调用，重复执行不产生副作用。
package netdriver

import (
	"context"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
)

// mainTable 是内核主路由表号（RT_TABLE_MAIN）。
const mainTable = 254

// ErrNATUnavailable 表示容器内缺少 iptables 工具，NAT 无法下发。
var ErrNATUnavailable = fmt.Errorf("netdriver: iptables not available in container")

// Mode 描述进程所处的网络形态，决定哪些网络层操作属于「本进程该管的」。
type Mode string

const (
	// ModeManaged 是「面板自管网络」形态：进程拥有独立 netns（K8s Pod、
	// systemd 裸机进程），转发开关、路由与 NAT 都在自己的命名空间里，
	// 必须由面板下发。
	//
	// 它不是一种 Docker 网络驱动：原 bridge 部署模式已移除——docker bridge
	// 是独立链路，上游路由器的 RA（链路本地组播）到不了容器，SLAAC 拿不到
	// 全局地址；发布宿主 v6 端口只能经 DNAT 解决入站握手，隧道内客户端
	// 访问 IPv6 互联网（出站数据面）无解。
	ModeManaged Mode = "managed"
	// ModeHost 是 --network host：进程与宿主共享 netns，
	// ip_forward 开关与宿主防火墙是同一个对象，
	// 面板擅自改写等同于修改宿主策略，因此一律跳过。
	ModeHost Mode = "host"
	// ModeIpvlan / ModeMacvlan 是 L2 直挂网络：容器经虚拟链路直接接入物理二层，
	// eth0 上持有局域网真实地址（IPv4 由 IPAM 分配，IPv6 由上游路由器 RA/SLAAC 下发）。
	//
	// 对面板而言这两种模式与自管（managed）形态的网络层职责完全一致——
	// 转发、路由、NAT 都在容器自己的 netns 里，必须照常下发；
	// 差别只在对外可达性：eth0 的地址就是客户端可直连的 Endpoint，
	// 不再是 NAT 内网地址。因此它们走自管的同一条网络调谐路径，
	// 只在 Endpoint 探测与 RA 诊断上做模式感知。
	ModeIpvlan Mode = "ipvlan"
	// ModeMacvlan 见 ModeIpvlan。
	ModeMacvlan Mode = "macvlan"
)

// IsL2 报告模式是否为 L2 直挂（ipvlan/macvlan）。
func (m Mode) IsL2() bool { return m == ModeIpvlan || m == ModeMacvlan }

// ParseMode 解析网络模式字符串，未知值返回错误而不是静默降级。
// bridge 已不再合法：部署模式只剩 host / ipvlan / macvlan（managed 由
// systemd/K8s 清单显式声明，bridge 值交给 config 层给出迁移提示）。
func ParseMode(s string) (Mode, error) {
	switch Mode(strings.ToLower(strings.TrimSpace(s))) {
	case ModeManaged:
		return ModeManaged, nil
	case ModeHost:
		return ModeHost, nil
	case ModeIpvlan:
		return ModeIpvlan, nil
	case ModeMacvlan:
		return ModeMacvlan, nil
	}
	return "", fmt.Errorf("netdriver: native mode must be managed, host, ipvlan or macvlan, got %q", s)
}

// Capabilities 描述容器内实际可用的网络能力。
type Capabilities struct {
	// TUNDevice 表示 /dev/net/tun 存在。
	TUNDevice bool
	// Netlink 表示具备创建/配置链路所需的 CAP_NET_ADMIN。
	Netlink bool
	// Iptables 表示容器内存在 iptables 命令。
	Iptables bool
	// Tc 表示容器内存在 tc 命令，Peer 级带宽限速依赖它。
	Tc bool
	// Forwarding 表示 IPv4 转发链路可用：开关已开启，或开关可写（能自行开启）。
	//
	// 两者满足其一即可，不能只看可写性：Docker 用 --sysctl 在建 netns 时就置位了
	// 开关，随后 /proc/sys 只读挂载，此时"写不了"但转发已生效。
	// 也不能只看取值，否则开关未开启且不可写时会被误报为可用。
	Forwarding bool
}

// Problems 返回人类可读的能力缺口，用于启动日志与就绪探针。
//
// host 模式下转发与 NAT 归属宿主，不算能力缺口；否则每个 host 部署都会刷一屏
// 无意义的告警，把真正的问题淹没掉。自管（managed/ipvlan/macvlan）形态
// 的转发与 NAT 由面板下发，缺 iptables 或转发开关就是真缺口。
func (c Capabilities) Problems(mode Mode) []string {
	var out []string
	if !c.Netlink {
		out = append(out, "netlink 操作被拒绝：请以 --cap-add NET_ADMIN 启动")
	}
	if !c.TUNDevice {
		out = append(out, "缺少 /dev/net/tun：仅 userspace（wireguard-go）实现需要，内核模块模式可忽略")
	}
	if mode != ModeHost {
		if !c.Iptables {
			out = append(out, "容器内无 iptables：NAT 将不可用（宿主网络模式下可忽略）")
		}
		if !c.Forwarding {
			out = append(out, "net.ipv4.ip_forward 未开启且不可写：转发链路将不通")
		}
	}
	if !c.Tc {
		out = append(out, "容器内无 tc：Peer 带宽限速不可用（其余功能正常）")
	}
	return out
}

// Usable 报告是否具备建立隧道的基本条件。
// 内核模块模式下由 netlink 创建接口，不需要 /dev/net/tun，故只校验 netlink 权限。
func (c Capabilities) Usable() bool { return c.Netlink }

// LinkInfo 是链路的实际状态，用于调谐比对。
type LinkInfo struct {
	Name      string
	Exists    bool
	Up        bool
	MTU       int
	Addresses []string
}

// NetDriver 抽象网络配置面，便于在无特权环境打桩。
type NetDriver interface {
	// Probe 探测容器网络能力。
	Probe() Capabilities

	// EnsureLink 确保 wireguard 链路存在且 MTU 正确。
	EnsureLink(ctx context.Context, name string, mtu int) error
	// DeleteLink 删除链路，不存在时视为成功。
	DeleteLink(ctx context.Context, name string) error
	// SetLinkUp 拉起链路。
	SetLinkUp(ctx context.Context, name string) error
	// EnsureAddress 幂等绑定地址（支持逗号分隔多地址）。
	EnsureAddress(ctx context.Context, name, cidrList string) error
	// SetMTU 调整链路 MTU。
	SetMTU(ctx context.Context, name string, mtu int) error
	// LinkInfo 读取链路实际状态。
	LinkInfo(name string) (LinkInfo, error)

	// EnsureForwarding 开启容器 netns 内 IPv4 转发。
	EnsureForwarding() error

	// EnsureRouting 按 Table 语义下发接口网段路由与 fwmark 策略规则。
	EnsureRouting(ctx context.Context, name, subnet, table string, fwmark int) error

	// EnsureNAT 下发 MASQUERADE、转发放行与 MSS 钳制规则。
	EnsureNAT(ctx context.Context, name, subnet string) error
	// RemoveNAT 回收 NAT 规则。
	RemoveNAT(ctx context.Context, name, subnet string) error

	// ApplyRateLimits 全量下发按 Peer 的带宽限速，并回收已失效的 class。
	ApplyRateLimits(ctx context.Context, dev string, limits []RateLimit) error
	// RateLimitApplied 报告接口上是否已存在本模块管理的限速 qdisc。
	//
	// 调谐器用它绕过「期望集合指纹未变就跳过下发」的优化：tc 状态只存在于内核内存中，
	// 一次 `tc qdisc del`、接口重建或容器迁移都会让它凭空消失，而期望集合毫无变化。
	// 不做这项核对的话，限速会一直处于「数据库里有、内核里没有」的状态，
	// 直到用户碰巧改了带宽才会被重新下发。
	RateLimitApplied(dev string) bool
	// CleanRateLimits 删除接口上的限速 qdisc。
	CleanRateLimits(ctx context.Context, dev string) error
}

// SubnetOf 返回地址所属网络的规范化 CIDR（如 10.8.0.1/24 → 10.8.0.0/24）。
// 路由与 NAT 必须使用网络地址而非主机地址，否则会生成不可达或错误的规则。
func SubnetOf(cidr string) (string, error) {
	p, err := netip.ParsePrefix(strings.TrimSpace(cidr))
	if err != nil {
		return "", fmt.Errorf("netdriver: invalid cidr %q: %w", cidr, err)
	}
	return p.Masked().String(), nil
}

// splitList 拆分逗号分隔的配置项并去除空白。
func splitList(v string) []string {
	var out []string
	for _, s := range strings.Split(v, ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// parseCIDR 解析单个 CIDR 并归一化为地址与掩码长度。
func parseCIDR(s string) (netip.Addr, int, error) {
	p, err := netip.ParsePrefix(strings.TrimSpace(s))
	if err != nil {
		return netip.Addr{}, 0, fmt.Errorf("netdriver: invalid cidr %q: %w", s, err)
	}
	return p.Addr(), p.Bits(), nil
}

// resolveTable 把 Table 配置解析为内核路由表号。
// "off" 表示不下发路由；"auto" 使用主表（254）；其余为显式表号或表名对应的数字。
func resolveTable(table string) (int, bool, error) {
	switch strings.ToLower(strings.TrimSpace(table)) {
	case "", "auto":
		return mainTable, true, nil
	case "off":
		return 0, false, nil
	}
	if n, err := strconv.Atoi(table); err == nil {
		if n <= 0 {
			return 0, false, fmt.Errorf("netdriver: invalid table %q", table)
		}
		return n, true, nil
	}
	return 0, false, fmt.Errorf("netdriver: routing table must be a number, auto or off, got %q", table)
}
