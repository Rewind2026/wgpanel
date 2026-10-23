//go:build linux

package netdriver

import (
	"context"
	"fmt"
	"net"
	"os/exec"
	"strings"
	"time"

	"github.com/vishvananda/netlink"
)

// ApplyRateLimits 按给定集合下发双向限速。
//
// 两侧都是全量重建而不是增量修改：Peer 可能被删除、限速可能被取消，
// 而 tc 没有「按 Peer 找出并删掉它的 class」的查询接口。
// 重建的代价是几条 exec 调用，比留下永远占着带宽的僵尸规则划算。
//
//	下行：wg0 根 qdisc + 每 Peer 一个 HTB class，按 dst 匹配。
//	上行：wg0 ingress qdisc 把流量重定向到 IFB 设备，在那里做同构的 HTB，按 src 匹配。
func (d *LinuxDriver) ApplyRateLimits(ctx context.Context, dev string, limits []RateLimit) error {
	if d.tcBin == "" {
		return ErrRateLimitUnsupported
	}
	if _, err := netlink.LinkByName(dev); err != nil {
		return fmt.Errorf("netdriver: lookup link %s: %w", dev, err)
	}

	// 一个 Peer 都没限速时把整棵树摘掉，而不是建一棵只有 root class 的空树。
	//
	// 空树没有任何作用：它不分流、不整形，只是让接口上多挂一个 HTB 和一个 IFB 设备。
	// 更麻烦的是它让「内核里是否还有限速」这个判据失去意义——
	// 空树和真正的限速树长得一样，无法区分，于是调谐器每轮都会因为
	// 「期望集合为空但树还在」而重复下发，日志刷个不停。
	if !hasRateLimits(limits) {
		return d.CleanRateLimits(ctx, dev)
	}

	if err := d.applyEgressLimits(ctx, dev, limits); err != nil {
		return err
	}
	return d.applyIngressLimits(ctx, dev, limits)
}

// hasRateLimits 判断集合里是否存在真正生效的限速项。
//
// 不能只看 len(limits)>0：调用方可能传入 Mbps<=0 或没有地址的条目，
// 它们不会被下发成任何 class，把它们算进去会让空树被误认为有效限速。
func hasRateLimits(limits []RateLimit) bool {
	for _, l := range limits {
		if l.Mbps > 0 && l.Address != "" {
			return true
		}
	}
	return false
}

// applyEgressLimits 下行限速：HTB 根 qdisc + 每个 Peer 一个 class。
//
// 根 qdisc 必须先删后建，不能用 replace。
//
// WireGuard 是虚拟设备，netlink 不支持对已存在的 qdisc 执行 change 操作，
// `tc qdisc replace` 会返回 "Change operation not supported by specified qdisc"。
// 首次创建时 replace 恰好等价于 add，因此问题只在第二次下发之后暴露：
// 面板启动时那一次成功，之后用户改任何带宽都静默失败，且日志只在 DEBUG 级，
// 看起来就像"限速功能偶尔不生效"。
//
// 删除失败通常意味着本来就没有根 qdisc，忽略即可，后面的 add 会补上。
func (d *LinuxDriver) applyEgressLimits(ctx context.Context, dev string, limits []RateLimit) error {
	_ = d.tcRun(ctx, "qdisc", "del", "dev", dev, "root")
	if err := d.tcRun(ctx, "qdisc", "add", "dev", dev, "root", "handle", rateRootHandle,
		"htb", "default", "1"); err != nil {
		return err
	}
	rootRate := rootRateMbps(limits)
	if err := d.tcAddClass(ctx, rootClassArgs(dev, rootRate)); err != nil {
		return err
	}

	for _, l := range limits {
		minor, ok := ratePeerHandle(l)
		if !ok {
			continue
		}
		classArgs, filterArgs := buildRateArgs(dev, minor, l.Address, l.Mbps, rateEgress)
		// add 在 class 已存在时报 File exists，属正常；
		// change 负责把带宽刷成最新值（限速被调小后必须立刻生效）。
		if err := d.tcAddClass(ctx, classArgs); err != nil {
			return err
		}
		if err := d.tcRun(ctx, changeRateArgs(dev, minor, l.Mbps)...); err != nil {
			return err
		}
		if err := d.tcRun(ctx, filterArgs...); err != nil {
			return err
		}
	}
	return nil
}

// applyIngressLimits 上行限速：ingress qdisc + 每 Peer 一条 mirred 重定向 +
// IFB 设备上的 HTB 树。
//
// 两棵 qdisc 都是先删后建而不是逐条改：
//
//  1. ingress qdisc 必须整体重建。tc 的 `qdisc del dev X handle ffff:` 删不掉它
//     （ingress qdisc 的删除语法是 `qdisc del dev X ingress`），残留下来时，
//     已经指向被删掉的 IFB 设备的重定向规则会继续生效，把上行包全部丢弃，
//     表现为「取消限速之后隧道彻底不通」。
//
//  2. u32 filter 的 replace 需要 (parent, prio, handle) 才能定位到唯一一条规则，
//     不带显式 handle 时 replace 不是替换而是追加：真机上反复下发同一条规则后，
//     同一个 prio 下出现了 ::800 ::801 ::802 三条并存。
//     重建 ingress qdisc 让每次下发都从空表开始，累积问题自然消失。
func (d *LinuxDriver) applyIngressLimits(ctx context.Context, dev string, limits []RateLimit) error {
	if err := d.ensureIFB(dev); err != nil {
		return err
	}
	// IFB 侧与下行完全同构，只是匹配方向反过来按 src。
	_ = d.tcRun(ctx, "qdisc", "del", "dev", rateIFBName, "root")
	if err := d.tcRun(ctx, "qdisc", "add", "dev", rateIFBName, "root", "handle", rateRootHandle,
		"htb", "default", "1"); err != nil {
		return err
	}
	if err := d.tcAddClass(ctx, rootClassArgs(rateIFBName, rootRateMbps(limits))); err != nil {
		return err
	}
	for _, l := range limits {
		minor, ok := ratePeerHandle(l)
		if !ok {
			continue
		}
		classArgs, filterArgs := buildRateArgs(rateIFBName, minor, l.Address, l.Mbps, rateIngress)
		if err := d.tcAddClass(ctx, classArgs); err != nil {
			return err
		}
		if err := d.tcRun(ctx, changeRateArgs(rateIFBName, minor, l.Mbps)...); err != nil {
			return err
		}
		if err := d.tcRun(ctx, filterArgs...); err != nil {
			return err
		}
	}

	_ = d.tcRun(ctx, "qdisc", "del", "dev", dev, "ingress")
	if err := d.tcRun(ctx, "qdisc", "add", "dev", dev, "handle", rateIngressHandle, "ingress"); err != nil {
		return err
	}
	for _, l := range limits {
		minor, ok := ratePeerHandle(l)
		if !ok {
			continue
		}
		if err := d.tcRun(ctx, buildIngressRedirectArgs(dev, minor, l.Address)...); err != nil {
			return err
		}
	}
	return nil
}

// ensureIFB 确保 IFB 设备存在、已拉起，且 MTU 与 WireGuard 接口一致。
//
// MTU 必须对齐：IFB 默认 1500，比隧道内层 MTU 大。重定向时按 IFB 的 MTU
// 判断能否直接转发，尺寸不一致会让包在整形设备上被重新分片或丢弃，
// 表现为"开了限速之后连接变得很慢甚至超时"。
//
// 容器里 modprobe ifb 会失败（镜像内没有 /lib/modules），但内核已内建 ifb，
// `ip link add ... type ifb` 直接可用。
func (d *LinuxDriver) ensureIFB(dev string) error {
	mtu := linkMTU(dev)
	link, err := netlink.LinkByName(rateIFBName)
	if err != nil {
		if !isLinkNotFound(err) {
			return fmt.Errorf("netdriver: lookup link %s: %w", rateIFBName, err)
		}
		attrs := netlink.NewLinkAttrs()
		attrs.Name = rateIFBName
		if mtu > 0 {
			attrs.MTU = mtu
		}
		if err := netlink.LinkAdd(&netlink.Ifb{LinkAttrs: attrs}); err != nil {
			return fmt.Errorf("netdriver: create ifb device %s（宿主需支持 ifb）: %w", rateIFBName, err)
		}
		if link, err = netlink.LinkByName(rateIFBName); err != nil {
			return fmt.Errorf("netdriver: reload link %s: %w", rateIFBName, err)
		}
	}
	// 已存在的设备可能被外部改过 MTU 或 down 掉，这里一并纠正。
	if mtu > 0 && link.Attrs().MTU != mtu {
		if err := netlink.LinkSetMTU(link, mtu); err != nil {
			return fmt.Errorf("netdriver: set mtu %d on %s: %w", mtu, rateIFBName, err)
		}
	}
	if link.Attrs().Flags&net.FlagUp == 0 {
		if err := netlink.LinkSetUp(link); err != nil {
			return fmt.Errorf("netdriver: bring link %s up: %w", rateIFBName, err)
		}
	}
	return nil
}

// linkMTU 读取接口 MTU，读不到时返回 0 表示「不关心」。
func linkMTU(name string) int {
	link, err := netlink.LinkByName(name)
	if err != nil {
		return 0
	}
	return link.Attrs().MTU
}

// RateLimitApplied 报告接口上是否仍有本模块创建的限速规则。
//
// 判据是「存在 Peer class」而不是「存在根 qdisc」：
// 只有 root class 的空树不分流也不整形，把它当成已应用会让调谐器
// 在「没有任何 Peer 限速」时永远认为无需处理，而内核里其实还挂着
// 一棵无用的 HTB；反过来「期望集合非空但树被清空」时又能正确触发重放。
//
// 只看下行侧即可：上行依赖的 IFB 设备与 ingress qdisc 都随下发一起重建，
// 任何一侧缺失都会导致上下行行为不一致，而这种情况只可能源于
// 外部手工删除整套 tc 状态——那种情况下下行侧也已经不存在了。
func (d *LinuxDriver) RateLimitApplied(dev string) bool {
	if d.tcBin == "" {
		return false
	}
	cctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, d.tcBin, "class", "show", "dev", dev).Output()
	if err != nil {
		return false
	}
	return len(parsePeerClassIDs(string(out))) > 0
}

// ratePeerHandle 校验一个 Peer 能否映射为 class handle，并返回映射结果。
func ratePeerHandle(l RateLimit) (int64, bool) {
	minor := rateClassMinor(l.ID)
	if minor == 0 {
		return 0, false
	}
	if l.Address == "" {
		return 0, false
	}
	return minor, true
}

// CleanRateLimits 删除接口上的限速规则：wg0 的 egress 与 ingress 根 qdisc，
// 以及承载上行整形的 IFB 设备。
//
// ingress qdisc 必须删。残留它比残留 egress 更危险：里面 mirred 规则会把上行包
// 重定向到已被删掉的 IFB 设备，内核找不到转发目标就把包丢掉，
// 表现为「取消限速之后隧道彻底不通」而不是「限速没生效」。
//
// 删除顺序也有讲究：先拆 qdisc 再拆 IFB。反过来时会有一个瞬间，
// ingress 规则还在而目标设备已经消失。
//
// IFB 设备也必须一并删掉：它是一个真实存在的虚拟网卡，不删就会一直留在
// 容器网络里，`ip link` 列表越用越脏，也让「有没有人在用限速」变得无法判断。
func (d *LinuxDriver) CleanRateLimits(ctx context.Context, dev string) error {
	if d.tcBin == "" {
		return ErrRateLimitUnsupported
	}
	// 接口可能已经不存在，此时视为清理完成。
	if _, err := netlink.LinkByName(dev); err != nil {
		return nil
	}
	_ = d.tcRun(ctx, "qdisc", "del", "dev", dev, "root")
	_ = d.tcRun(ctx, "qdisc", "del", "dev", dev, "ingress")
	d.deleteIFB()
	return nil
}

// deleteIFB 删除 IFB 设备，不存在视为成功。
func (d *LinuxDriver) deleteIFB() {
	link, err := netlink.LinkByName(rateIFBName)
	if err != nil {
		return
	}
	if err := netlink.LinkDel(link); err != nil && !isLinkNotFound(err) {
		return
	}
}

// tcAddClass 执行 class add，「已存在」视为成功。
func (d *LinuxDriver) tcAddClass(ctx context.Context, args []string) error {
	full := append([]string{"class", "add"}, args...)
	if err := d.tcRun(ctx, full...); err != nil && !isTcExists(err) {
		return err
	}
	return nil
}

// tcRun 以固定参数数组执行 tc，不经过 shell。
func (d *LinuxDriver) tcRun(ctx context.Context, args ...string) error {
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, d.tcBin, args...).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("netdriver: tc %s: %s", strings.Join(args, " "), msg)
	}
	return nil
}

func isTcExists(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "File exists") || strings.Contains(msg, "already exists")
}
