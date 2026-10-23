package netdriver

import (
	"fmt"
	"strconv"
	"strings"
)

// 带宽限速在 WireGuard 接口上只能分成两个方向做，这是设备本身的性质决定的：
//
//   - 下行（服务端 -> 客户端）走 wg0 的 egress qdisc。WireGuard 在 egress 路径上
//     呈现的是解密后的内层包，目的地址就是 Peer 的隧道地址，
//     因此按 `match ip dst <peer>` 分类到 HTB class 是精确的。
//
//   - 上行（客户端 -> 服务端）永远不经过 egress qdisc——那些包是从内核协议栈
//     直接送进 wg0 的。所以上行必须走 ingress 钩子。
//
// 上行在 ingress 上怎么做，这里踩过一个坑：先用 tc police 动作，
// 在目标机器上 20 Mbps 只跑出 6.5 Mbps，100 Mbps 只跑出 21.75 Mbps，
// 且补上官方文档里说的 mtu 参数也无效——该内核的 police 记账精度不可用。
// 现在改用 IFB（Intermediate Functional Block）把 ingress 流量重定向到一个
// 虚拟设备，在那里用与下行完全相同的 HTB class 整形。
// 实测 5/20/100 Mbps 分别收敛到 4.80/19.23/95.49 Mbps，与下行精度一致。
const (
	rateRootHandle  = "1:"
	rateRootClassID = "1:1"
	// rateRootClassMinor 是 root class 占用的 minor 号，Peer 的 minor 从它之后开始。
	rateRootClassMinor = 1
	// rateFallbackMbps 是没有任何限速配置时根 class 的带宽上限。
	// 取 10 Gbit/s：足够跑满万兆口，又不至于让 HTB 的整数换算溢出。
	rateFallbackMbps = 10000
	// maxClassID 是 class minor 号上限。超过则无法用 peerID 直接当 handle。
	maxClassID = 65534
	// rateIngressHandle 是 wg0 上 ingress qdisc 的 handle。
	// ingress qdisc 必须挂在 ingress 钩子上，与 egress 根 qdisc 是两个独立对象，
	// 用同一个 handle 会在第二条命令时报 File exists。
	rateIngressHandle = "ffff:"
	// rateIFBName 是承接上行流量的 IFB 设备名。
	// 一个 wg 接口只需要一个：所有 Peer 的上行流量都汇到同一个设备上，
	// 再按源地址分到各自的 class。
	rateIFBName = "wgifb0"
)

// rateClassMinor 把 Peer 主键映射为 HTB class 的 minor 号。
//
// 整体 +1 是为了让 Peer 的 class 与 root class（minor 1）不冲突。
// 主键为 0 或映射后越界都返回 0，表示"无法映射"，调用方应拒绝下发而不是
// 悄悄用一个小概率错误的号去覆盖别人的 class。
func rateClassMinor(peerID int64) int64 {
	if peerID <= 0 {
		return 0
	}
	minor := peerID + rateRootClassMinor
	if minor > maxClassID {
		return 0
	}
	return minor
}

// RateLimit 是一个客户端的带宽上限。
type RateLimit struct {
	// ID 是 Peer 主键，内部会经 rateClassMinor 映射为 HTB class 的 minor 号。
	ID int64
	// Address 是 Peer 的隧道内地址（不含掩码）。
	// 下行按它做目的地址匹配，上行按它做源地址匹配。
	Address string
	// Mbps 是单个方向的带宽上限，0 表示不限速（不下发 class 与 filter）。
	// 两个方向各自独立限到 Mbps，实际峰值带宽约为 2×Mbps。
	Mbps int
}

// ErrRateLimitUnsupported 表示容器内没有 tc 或 ifb 模块，限速不可用。
var ErrRateLimitUnsupported = fmt.Errorf("netdriver: tc not available in container")

// buildRateArgs 构造一个 Peer 在指定设备上的 class 与 filter 参数，
// 供下行（wg0 egress）与上行（IFB 设备）共用，供测试与实现共用。
//
// 单独抽出来是因为 tc 的参数顺序错了不会编译报错、只会静默限错对象，
// 需要能用单元测试钉住。
//
// classID 已经是 rateClassMinor 映射后的 minor 号，不再是 Peer 主键。
//
// class 的 parent 必须是 root class（1:1）而不是根 qdisc（1:）。写成 1: 时
// tc 会把这个 class 挂成根 qdisc 的直接子级，与 root class 平级，
// 于是 HTB 里出现多个互不隶属的 root class：命令全部成功，
// 但整条父子带宽链断掉，root class 的余量分配、桶间公平都不再生效，
// 排查时只能看到"限速时快时慢"这种无法定位的现象。
//
// filter 的 parent 必须是根 qdisc（1:）而不是 root class（1:1）。
// 挂在 class 上时 tc 会正常创建规则、tc filter show 也能看到，
// 但 HTB 的分类只发生在根 qdisc 层：包先按根 qdisc 的规则定 flowid，
// 进入 class 之后再走该 class 直属的 filter。
// 实测挂在 parent 1:1 上时 rule hit 与 success 恒为 0，
// class 的 Sent 字节数也恒为 0，限速完全不生效且无任何报错。
//
// dir 决定匹配方向：下行按 dst，上行按 src。
//
// filter 的 prio 直接用 classID，即每个 Peer 一条独立规则。
// 这不是随意选的：u32 filter 的 replace 语义要求 (parent, prio, handle) 三者
// 才能定位到唯一一条规则，不带显式 handle 时 replace 不会顶掉同 prio 的旧规则，
// 而是再挂一条。真机上连发两次 prio 1 的 replace 后，tc filter show 里
// 同一个 pref 下出现了 ::800 ::801 ::802 三条并存。
// 于是固定 prio 会让每轮调谐都多挂一条重复规则：规则越堆越多、匹配越来越慢，
// 而且删不干净——按 prio 删会把整个 pref 下的规则一起删掉。
// prio 唯一之后，删除才能精确到单个 Peer。
func buildRateArgs(dev string, classID int64, address string, mbps int, dir rateDirection) (classArgs, filterArgs []string) {
	full := rateRootHandle + strconv.FormatInt(classID, 10)
	// rate 与 ceil 取同一个值即硬限速；只设 rate 会让未整形的突发流量钻出去。
	rate := strconv.Itoa(mbps) + "mbit"

	classArgs = []string{
		"dev", dev, "parent", rateRootClassID, "classid", full,
		"htb", "rate", rate, "ceil", rate,
	}
	filterArgs = []string{
		"filter", "replace", "dev", dev, "protocol", "ip", "parent", rateRootHandle,
		"prio", strconv.FormatInt(classID, 10), "u32",
		"match", "ip", dir.matchKey(), address + "/32",
		"flowid", full,
	}
	return classArgs, filterArgs
}

// rateDirection 区分上下行在 filter 上的匹配方向。
type rateDirection int

const (
	// rateEgress 是下行：服务端发给客户端，目的地址是 Peer。
	rateEgress rateDirection = iota
	// rateIngress 是上行：客户端发给服务端，源地址是 Peer。
	rateIngress
)

func (d rateDirection) matchKey() string {
	if d == rateIngress {
		return "src"
	}
	return "dst"
}

// changeRateArgs 构造修改既有 class 带宽的参数。
//
// parent 与 add 时保持一致（root class），否则 tc 会把它当成平级 class 重新挂载。
func changeRateArgs(dev string, classID int64, mbps int) []string {
	full := rateRootHandle + strconv.FormatInt(classID, 10)
	rate := strconv.Itoa(mbps) + "mbit"
	return []string{
		"class", "change", "dev", dev, "parent", rateRootClassID, "classid", full,
		"htb", "rate", rate, "ceil", rate,
	}
}

// buildIngressRedirectArgs 构造把某个 Peer 的上行流量重定向到 IFB 设备的 ingress 规则。
//
// mirred egress redirect 会「偷走」这个包（stolen）：包被送到 IFB 设备上排队，
// 整形完再从那里发回原接口。这是 IFB 的标准用法，也是唯一能在不改内核代码的
// 前提下对 ingress 做整形的办法——ingress 上只有 police 可用，而该内核的
// police 记账精度不足以做带宽整形（20 Mbps 只跑出 6.5 Mbps）。
//
// prio 与 buildRateArgs 同理取 classID，保证每个 Peer 一条独立规则。
// 上行必须按 src 匹配：这是客户端发往服务端的包，源地址才是 Peer 的隧道地址。
//
// 用 replace 而不是 add：每次下发前 ingress qdisc 都被整体重建，
// 规则表是空的，replace 与 add 等价；用 replace 是为了在重建失败时
// 也不至于在半张表上叠加规则。
func buildIngressRedirectArgs(dev string, classID int64, address string) []string {
	return []string{
		"filter", "replace", "dev", dev, "protocol", "ip", "parent", rateIngressHandle,
		"prio", strconv.FormatInt(classID, 10), "u32",
		"match", "ip", "src", address + "/32",
		"action", "mirred", "egress", "redirect", "dev", rateIFBName,
	}
}

// rootClassArgs 构造根 class 的参数。
//
// 根 class 挂在根 qdisc（1:）下，是所有 Peer class 的父级，必须先于它们存在。
func rootClassArgs(dev string, mbps int) []string {
	rate := strconv.Itoa(mbps) + "mbit"
	return []string{
		"dev", dev, "parent", rateRootHandle, "classid", rateRootClassID,
		"htb", "rate", rate, "ceil", rate,
	}
}

// rootRateMbps 计算根 class 带宽：所有限速之和加上余量。
func rootRateMbps(limits []RateLimit) int {
	sum := 0
	for _, l := range limits {
		if l.Mbps > 0 {
			sum += l.Mbps
		}
	}
	if sum == 0 {
		return rateFallbackMbps
	}
	// 50% 余量：限速总和之外的突发流量仍应能通过，否则限速会把整条隧道拖慢。
	return sum * 3 / 2
}

// parseClassIDs 从 `tc class show` 的输出里抽出 class 的 minor 号。
//
// 只认 root handle 下的 class：htb 的输出形如
//
//	class htb 1:1 root prio 0 rate 10Gbit ceil 10Gbit burst 0b cburst 0b
//	class htb 1:12 parent 1:1 leaf 8012: prio 0 rate 20Mbit ceil 20Mbit
func parseClassIDs(out string) map[int64]struct{} {
	ids := map[int64]struct{}{}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 || fields[0] != "class" || fields[1] != "htb" {
			continue
		}
		majorMinor, ok := strings.CutPrefix(fields[2], rateRootHandle)
		if !ok {
			continue
		}
		n, err := strconv.ParseInt(majorMinor, 10, 64)
		if err != nil || n <= 0 || n > maxClassID {
			continue
		}
		ids[n] = struct{}{}
	}
	return ids
}

// parsePeerClassIDs 从 `tc class show` 的输出里抽出除根 class 以外的 class minor 号。
//
// 只认 root handle 下的 class：htb 的输出形如
//
//	class htb 1:1 root prio 0 rate 10Gbit ceil 10Gbit burst 0b cburst 0b
//	class htb 1:12 parent 1:1 leaf 8012: prio 0 rate 20Mbit ceil 20Mbit
//
// 根 class（1:1）由本模块创建，但不代表任何 Peer 正在被限速，必须排除。
func parsePeerClassIDs(out string) map[int64]struct{} {
	ids := parseClassIDs(out)
	delete(ids, rateRootClassMinor)
	return ids
}
