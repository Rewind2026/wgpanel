//go:build linux

package netdriver

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/vishvananda/netlink"
)

// forwardingPath 是容器 netns 内的 IPv4 转发开关。
//
// 它是 namespaced 的，但不一定可写：Docker 用 `--sysctl net.ipv4.ip_forward=1`
// 创建 netns 时，开关在容器启动前就已置位，随后 /proc/sys 以只读方式挂载。
// 所以"可写"和"已开启"必须分别判断，不能互相代替。
//
// 声明为 var 而非 const，只为让单元测试能指向临时文件。
var forwardingPath = "/proc/sys/net/ipv4/ip_forward"

// LinuxDriver 基于 netlink、iptables 与 tc 实现网络配置。
type LinuxDriver struct {
	iptablesBin string
	tcBin       string
}

// New 返回 Linux 平台的 netdriver。
func New() *LinuxDriver {
	d := &LinuxDriver{}
	if bin, err := exec.LookPath("iptables"); err == nil {
		d.iptablesBin = bin
	}
	if bin, err := exec.LookPath("tc"); err == nil {
		d.tcBin = bin
	}
	return d
}

// Probe 探测容器网络能力。
//
// 探测必须是只读的：/readyz 每次请求都会调用它，
// 如果在这里写 ip_forward，探针本身就变成了副作用源——
// 只想查状态却改了转发开关，出问题时无法区分是配置漂移还是探针干的。
// 真正要开启转发由 EnsureForwarding 负责。
func (d *LinuxDriver) Probe() Capabilities {
	caps := Capabilities{}
	if _, err := os.Stat("/dev/net/tun"); err == nil {
		caps.TUNDevice = true
	}
	if _, err := netlink.LinkList(); err == nil {
		caps.Netlink = true
	}
	caps.Iptables = d.iptablesBin != ""
	caps.Tc = d.tcBin != ""
	// 转发"可用"的判据是最终状态，而不是开关文件是否可写：
	// Docker 用 `--sysctl net.ipv4.ip_forward=1` 建 netns 时，
	// 容器内 /proc/sys 是只读的，但转发已经生效。
	caps.Forwarding = forwardingWritable() || forwardingEnabled()
	return caps
}

// forwardingWritable 判断 ip_forward 开关是否可写，不改变其取值。
// 只读打开成功即说明开关存在；以只写方式打开后立即关闭，
// 既验证了写权限，又不会把值改成什么。
func forwardingWritable() bool {
	f, err := os.OpenFile(forwardingPath, os.O_WRONLY, 0)
	if err != nil {
		return false
	}
	_ = f.Close()
	return true
}

// EnsureLink 确保 wireguard 链路存在且 MTU 与期望一致。
func (d *LinuxDriver) EnsureLink(ctx context.Context, name string, mtu int) error {
	link, err := netlink.LinkByName(name)
	if err != nil {
		if !isLinkNotFound(err) {
			return fmt.Errorf("netdriver: lookup link %s: %w", name, err)
		}
		attrs := netlink.NewLinkAttrs()
		attrs.Name = name
		if mtu > 0 {
			attrs.MTU = mtu
		}
		link = &netlink.GenericLink{LinkAttrs: attrs, LinkType: "wireguard"}
		if err := netlink.LinkAdd(link); err != nil {
			return fmt.Errorf("netdriver: create wireguard link %s（宿主需已加载 wireguard 内核模块）: %w", name, err)
		}
		link, err = netlink.LinkByName(name)
		if err != nil {
			return fmt.Errorf("netdriver: reload link %s: %w", name, err)
		}
	}
	if mtu > 0 && link.Attrs().MTU != mtu {
		if err := netlink.LinkSetMTU(link, mtu); err != nil {
			return fmt.Errorf("netdriver: set mtu %d on %s: %w", mtu, name, err)
		}
	}
	return nil
}

// DeleteLink 删除链路，链路不存在视为成功。
func (d *LinuxDriver) DeleteLink(ctx context.Context, name string) error {
	link, err := netlink.LinkByName(name)
	if err != nil {
		if isLinkNotFound(err) {
			return nil
		}
		return fmt.Errorf("netdriver: lookup link %s: %w", name, err)
	}
	if err := netlink.LinkDel(link); err != nil && !isLinkNotFound(err) {
		return fmt.Errorf("netdriver: delete link %s: %w", name, err)
	}
	return nil
}

// SetLinkUp 拉起链路。
func (d *LinuxDriver) SetLinkUp(ctx context.Context, name string) error {
	link, err := netlink.LinkByName(name)
	if err != nil {
		return fmt.Errorf("netdriver: lookup link %s: %w", name, err)
	}
	if link.Attrs().Flags&net.FlagUp != 0 {
		return nil
	}
	if err := netlink.LinkSetUp(link); err != nil {
		return fmt.Errorf("netdriver: bring link %s up: %w", name, err)
	}
	return nil
}

// SetMTU 调整链路 MTU。
func (d *LinuxDriver) SetMTU(ctx context.Context, name string, mtu int) error {
	if mtu <= 0 {
		return nil
	}
	link, err := netlink.LinkByName(name)
	if err != nil {
		return fmt.Errorf("netdriver: lookup link %s: %w", name, err)
	}
	if link.Attrs().MTU == mtu {
		return nil
	}
	if err := netlink.LinkSetMTU(link, mtu); err != nil {
		return fmt.Errorf("netdriver: set mtu %d on %s: %w", mtu, name, err)
	}
	return nil
}

// EnsureAddress 幂等绑定地址，已存在时忽略。
func (d *LinuxDriver) EnsureAddress(ctx context.Context, name, cidrList string) error {
	link, err := netlink.LinkByName(name)
	if err != nil {
		return fmt.Errorf("netdriver: lookup link %s: %w", name, err)
	}
	for _, cidr := range splitList(cidrList) {
		ipnet, err := toIPNet(cidr)
		if err != nil {
			return err
		}
		if err := netlink.AddrAdd(link, &netlink.Addr{IPNet: ipnet}); err != nil {
			if errors.Is(err, syscall.EEXIST) || strings.Contains(err.Error(), "file exists") {
				continue
			}
			return fmt.Errorf("netdriver: add address %s to %s: %w", cidr, name, err)
		}
	}
	return nil
}

// LinkInfo 读取链路实际状态。
func (d *LinuxDriver) LinkInfo(name string) (LinkInfo, error) {
	link, err := netlink.LinkByName(name)
	if err != nil {
		if isLinkNotFound(err) {
			return LinkInfo{Name: name}, nil
		}
		return LinkInfo{}, fmt.Errorf("netdriver: lookup link %s: %w", name, err)
	}
	info := LinkInfo{
		Name:   name,
		Exists: true,
		Up:     link.Attrs().Flags&net.FlagUp != 0,
		MTU:    link.Attrs().MTU,
	}
	addrs, err := netlink.AddrList(link, 0)
	if err != nil {
		return info, fmt.Errorf("netdriver: list addresses of %s: %w", name, err)
	}
	for _, a := range addrs {
		info.Addresses = append(info.Addresses, a.IPNet.String())
	}
	return info, nil
}

// EnsureForwarding 开启容器 netns 内的 IPv4 转发。
//
// 开关可能已经处于目标状态且不可写，典型场景是 Docker 通过
// `--sysctl net.ipv4.ip_forward=1` 在创建 netns 时就把它设好了：
// 此时 /proc/sys 在容器里是只读挂载，写必然失败，但转发实际已经生效。
// 因此先读后写——已开启就直接返回，避免把"已经是正确的"误报成故障。
func (d *LinuxDriver) EnsureForwarding() error {
	if forwardingEnabled() {
		return nil
	}
	if err := forwardingWrite("1"); err != nil {
		return fmt.Errorf("netdriver: enable ip forwarding: %w", err)
	}
	return nil
}

// forwardingEnabled 读取 ip_forward 当前取值。
// 读不到时返回 false，让调用方继续走写入路径。
func forwardingEnabled() bool {
	b, err := os.ReadFile(forwardingPath)
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(b)) == "1"
}

// forwardingWrite 写入转发开关。
//
// 抽成包级变量是为了让单元测试能强制写失败。真机上"值已是 1 但 /proc/sys
// 只读"这一场景没法用 0444 文件复现：测试常以 root 运行，而 root 忽略权限位，
// 写照样成功，于是"是否尝试过写"这条关键分支永远测不到。
var forwardingWrite = func(v string) error {
	return os.WriteFile(forwardingPath, []byte(v), 0o644)
}

// EnsureRouting 按 Table 语义下发接口网段路由，并在设置 fwmark 时附加策略规则。
func (d *LinuxDriver) EnsureRouting(ctx context.Context, name, subnet, table string, fwmark int) error {
	tableID, enabled, err := resolveTable(table)
	if err != nil {
		return err
	}
	if !enabled {
		return nil
	}
	if fwmark != 0 && tableID == mainTable {
		return fmt.Errorf("netdriver: fwmark 需配合显式路由表使用（Table 不能是 auto）")
	}
	ipnet, err := toIPNet(subnet)
	if err != nil {
		return err
	}
	link, err := netlink.LinkByName(name)
	if err != nil {
		return fmt.Errorf("netdriver: lookup link %s: %w", name, err)
	}

	route := &netlink.Route{
		LinkIndex: link.Attrs().Index,
		Dst:       ipnet,
		Table:     tableID,
		Scope:     netlink.SCOPE_LINK,
		Protocol:  syscall.RTPROT_STATIC,
		Type:      syscall.RTN_UNICAST,
	}
	if err := netlink.RouteReplace(route); err != nil {
		return fmt.Errorf("netdriver: add route %s dev %s: %w", subnet, name, err)
	}

	if fwmark == 0 {
		return nil
	}
	// 与 wg-quick 一致：用 fwmark 作为规则优先级，避免与其它规则冲突。
	mark := uint32(fwmark)
	mask := uint32(0xFFFFFFFF)
	rule := netlink.Rule{Priority: fwmark, Table: tableID, Mark: mark, Mask: &mask}
	if err := netlink.RuleAdd(&rule); err != nil && !isRuleExists(err) {
		return fmt.Errorf("netdriver: add fwmark rule (%d -> table %d): %w", fwmark, tableID, err)
	}
	return nil
}

// natRules 返回 NAT 与转发规则集（幂等下发用）。
//
// 提取成函数而非在调用点内联，是为了让单元测试能直接断言真实规则集：
// 规则写错时只有真实调用路径会暴露问题，测试里抄一份副本则永远测不到。
// 参数规则不含表选项时的顺序由 withFlag 保证。
func natRules(name, subnet string) [][]string {
	return [][]string{
		{"-t", "nat", "POSTROUTING", "-s", subnet, "!", "-o", name, "-j", "MASQUERADE"},
		{"FORWARD", "-i", name, "-j", "ACCEPT"},
		{"FORWARD", "-o", name, "-m", "conntrack", "--ctstate", "RELATED,ESTABLISHED", "-j", "ACCEPT"},
		{"-t", "mangle", "FORWARD", "-p", "tcp", "--tcp-flags", "SYN,RST", "SYN", "-j", "TCPMSS", "--clamp-mss-to-pmtu"},
	}
}

// natTeardownRules 返回需要回收的规则集。
//
// 不含 MSS 规则：TCPMSS 是幂等的按需规则，回滚时删除会与并发下发打架，
// 且它随接口存在而存在，接口删除后自然消失。
func natTeardownRules(name, subnet string) [][]string {
	return [][]string{
		{"-t", "nat", "POSTROUTING", "-s", subnet, "!", "-o", name, "-j", "MASQUERADE"},
		{"FORWARD", "-i", name, "-j", "ACCEPT"},
		{"FORWARD", "-o", name, "-m", "conntrack", "--ctstate", "RELATED,ESTABLISHED", "-j", "ACCEPT"},
	}
}

// EnsureNAT 下发 NAT 与转发规则（幂等）。
func (d *LinuxDriver) EnsureNAT(ctx context.Context, name, subnet string) error {
	for _, rule := range natRules(name, subnet) {
		if err := d.ensureRule(ctx, rule); err != nil {
			return err
		}
	}
	return nil
}

// RemoveNAT 回收 NAT 与转发规则。
func (d *LinuxDriver) RemoveNAT(ctx context.Context, name, subnet string) error {
	if d.iptablesBin == "" {
		return nil
	}
	for _, rule := range natTeardownRules(name, subnet) {
		if err := d.iptablesRun(ctx, withFlag("-D", rule)...); err != nil && !isRuleMissing(err) {
			return err
		}
	}
	return nil
}

// ensureRule 先 -C 探测再 -A 追加，保证幂等。
func (d *LinuxDriver) ensureRule(ctx context.Context, rule []string) error {
	if err := d.iptablesRun(ctx, withFlag("-C", rule)...); err == nil {
		return nil
	}
	return d.iptablesRun(ctx, withFlag("-A", rule)...)
}

// withFlag 组装 iptables 参数，把 -C/-A/-D 动作标志插到链名之前。
//
// 标志必须排在 "-t <table>" 之后：iptables 的语法是
//
//	iptables [-t table] <命令> [链] [规则...]
//
// -t 是全局选项，必须先于命令出现。若简单地把标志塞到参数最前面，
// 带表的规则会变成 "-A -t nat POSTROUTING ..."，iptables 会把 `nat`
// 当成链名，报 "Bad argument `nat'"——NAT 与 MSS 规则因此全部下发失败。
func withFlag(flag string, rule []string) []string {
	// 前缀只可能是 "-t <table>" 这一种全局选项，占两个参数。
	prefixLen := 0
	if len(rule) >= 2 && rule[0] == "-t" {
		prefixLen = 2
	}
	args := make([]string, 0, len(rule)+1)
	args = append(args, rule[:prefixLen]...)
	args = append(args, flag)
	return append(args, rule[prefixLen:]...)
}

// iptablesRun 以固定参数数组执行 iptables，不经过 shell。
func (d *LinuxDriver) iptablesRun(ctx context.Context, args ...string) error {
	if d.iptablesBin == "" {
		return ErrNATUnavailable
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, d.iptablesBin, args...).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("netdriver: iptables %s: %s", strings.Join(args, " "), msg)
	}
	return nil
}

// toIPNet 把 CIDR 字符串转换为 net.IPNet。
func toIPNet(cidr string) (*net.IPNet, error) {
	addr, bits, err := parseCIDR(cidr)
	if err != nil {
		return nil, err
	}
	return &net.IPNet{
		IP:   net.IP(addr.AsSlice()),
		Mask: net.CIDRMask(bits, addr.BitLen()),
	}, nil
}

func isLinkNotFound(err error) bool {
	if err == nil {
		return false
	}
	var nf netlink.LinkNotFoundError
	if errors.As(err, &nf) {
		return true
	}
	return errors.Is(err, syscall.ENODEV) || errors.Is(err, os.ErrNotExist)
}

func isRuleExists(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, syscall.EEXIST) || strings.Contains(err.Error(), "file exists")
}

func isRuleMissing(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "No chain/target/match by that name") ||
		strings.Contains(msg, "does a matching rule exist")
}

// compile-time 断言：确保 Linux 实现满足接口。
var _ NetDriver = (*LinuxDriver)(nil)
