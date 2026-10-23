package netdriver

import (
	"slices"
	"strconv"
	"strings"
	"testing"
)

// tc 参数顺序错了不会编译报错，只会静默限错对象，必须钉死。
func TestBuildRateArgs(t *testing.T) {
	classArgs, filterArgs := buildRateArgs("wg0", 43, "10.8.0.2", 25, rateEgress)

	// classArgs 不含动作词：tcAddClass 会统一补上 "class add"，
	// 两处都写就会出现 "class add class add ..." 这种必然失败的命令。
	wantClass := []string{
		"dev", "wg0", "parent", "1:1", "classid", "1:43",
		"htb", "rate", "25mbit", "ceil", "25mbit",
	}
	if !slices.Equal(classArgs, wantClass) {
		t.Errorf("classArgs =\n%v\nwant\n%v", classArgs, wantClass)
	}
	if classArgs[0] == "class" || classArgs[0] == "add" {
		t.Error("classArgs 不应自带动作词，否则会与 tcAddClass 补的重复")
	}

	wantFilter := []string{
		"filter", "replace", "dev", "wg0", "protocol", "ip", "parent", "1:",
		"prio", "43", "u32", "match", "ip", "dst", "10.8.0.2/32", "flowid", "1:43",
	}
	if !slices.Equal(filterArgs, wantFilter) {
		t.Errorf("filterArgs =\n%v\nwant\n%v", filterArgs, wantFilter)
	}
}

// TestPeerClassesHangOffRootClass 锁定 Peer class 的父级是 root class 而非根 qdisc。
//
// 真机上暴露的缺陷：parent 写成根 qdisc 的 "1:"，tc 会把 Peer class 挂成
// 根 qdisc 的直接子级，与 root class 平级——`tc class show` 里多个 class
// 都标着 "root"，父子带宽链断开。命令全部返回 0，日志也报成功，
// 但 root class 预留的余量与桶间公平都不再生效。
func TestPeerClassesHangOffRootClass(t *testing.T) {
	classArgs, _ := buildRateArgs("wg0", 43, "10.8.0.2", 25, rateEgress)
	if got := classArgs[slices.Index(classArgs, "parent")+1]; got != rateRootClassID {
		t.Errorf("Peer class 的 parent = %q，期望 root class %q", got, rateRootClassID)
	}
	changed := changeRateArgs("wg0", 43, 25)
	if got := changed[slices.Index(changed, "parent")+1]; got != rateRootClassID {
		t.Errorf("class change 的 parent = %q，期望 root class %q", got, rateRootClassID)
	}
	// 反过来，root class 自己必须挂在根 qdisc 下。
	rootArgs := rootClassArgs("wg0", 30)
	if got := rootArgs[slices.Index(rootArgs, "parent")+1]; got != rateRootHandle {
		t.Errorf("root class 的 parent = %q，期望根 qdisc %q", got, rateRootHandle)
	}
}

// TestFilterMustAttachToRootQdisc 锁定 filter 挂在根 qdisc 而非 class 上。
//
// 真机上暴露的缺陷：filter 挂在 parent 1:1（root class）时，
// tc 正常创建规则、`tc filter show` 也能看到，但 rule hit 与 success 恒为 0，
// class 的 Sent 字节数也恒为 0，限速完全不生效且没有任何报错。
// 原因是 HTB 只在根 qdisc 层做分类：包先按根 qdisc 的规则定 flowid，
// 进入 class 之后只走该 class 直属的 filter。
func TestFilterMustAttachToRootQdisc(t *testing.T) {
	_, filterArgs := buildRateArgs("wg0", 43, "10.8.0.2", 25, rateEgress)
	pi := slices.Index(filterArgs, "parent")
	if pi < 0 {
		t.Fatalf("filterArgs 缺少 parent: %v", filterArgs)
	}
	if got := filterArgs[pi+1]; got != rateRootHandle {
		t.Errorf("filter 的 parent = %q，期望根 qdisc %q（挂 class 上永远不会命中）", got, rateRootHandle)
	}
}

// TestFilterMatchesDirection 锁定上下行的匹配方向相反。
//
// egress 路径上呈现的是解密后的内层包：服务端发给客户端的包，
// 目的地址才是 Peer 的隧道地址，写成 src 的话一个都匹配不到。
// IFB 侧承接的是客户端发来的包，源地址才是 Peer，必须反过来。
func TestFilterMatchesDirection(t *testing.T) {
	_, down := buildRateArgs("wg0", 43, "10.8.0.2", 25, rateEgress)
	if mi := slices.Index(down, "match"); down[mi+2] != "dst" {
		t.Errorf("下行 filter 应匹配 dst，实际 %q", down[mi+2])
	}

	_, up := buildRateArgs(rateIFBName, 43, "10.8.0.2", 25, rateIngress)
	mi := slices.Index(up, "match")
	if up[mi+2] != "src" {
		t.Errorf("上行 filter 应匹配 src，实际 %q", up[mi+2])
	}
	if up[mi+3] != "10.8.0.2/32" {
		t.Errorf("匹配地址 = %q，期望 10.8.0.2/32", up[mi+3])
	}

	if rateEgress.matchKey() == rateIngress.matchKey() {
		t.Error("上下行的 matchKey 必须相反")
	}
}

// TestFilterPrioIsUniquePerPeer 锁定 filter 的 prio 每个 Peer 唯一。
//
// 真机上暴露的缺陷：u32 filter 的 replace 需要 (parent, prio, handle) 才能
// 定位到唯一一条规则，不带显式 handle 时 replace 不是替换而是追加。
// 连发两次 prio 1 的 replace 之后，tc filter show 里同一个 pref 下
// 出现了 ::800 ::801 ::802 三条并存。固定 prio 会让每轮调谐都多挂一条重复规则，
// 规则越堆越多、匹配越来越慢，而且按 prio 删会把整个 pref 一起删掉。
func TestFilterPrioIsUniquePerPeer(t *testing.T) {
	seen := map[string]int64{}
	for id := int64(1); id <= 500; id++ {
		minor := rateClassMinor(id)
		_, filterArgs := buildRateArgs("wg0", minor, "10.8.0.2", 10, rateEgress)
		pi := slices.Index(filterArgs, "prio")
		if pi < 0 {
			t.Fatalf("filterArgs 缺少 prio: %v", filterArgs)
		}
		if prev, dup := seen[filterArgs[pi+1]]; dup {
			t.Errorf("peer %d 与 peer %d 的 filter prio 撞号 %q", prev, id, filterArgs[pi+1])
		}
		seen[filterArgs[pi+1]] = id
	}

	// 重定向规则同样必须 per-peer 唯一，否则清理时无法精确删除。
	seenPrio := map[string]struct{}{}
	for id := int64(1); id <= 500; id++ {
		minor := rateClassMinor(id)
		args := buildIngressRedirectArgs("wg0", minor, "10.8.0.2")
		pi := slices.Index(args, "prio")
		if _, dup := seenPrio[args[pi+1]]; dup {
			t.Errorf("peer %d 的重定向 prio 撞号 %q", id, args[pi+1])
		}
		seenPrio[args[pi+1]] = struct{}{}
	}
}

// TestIngressRedirectShape 锁定重定向规则的形态。
//
// 上行包从内核协议栈直送 wg0，永不经过 egress qdisc，只能在 ingress 上处理；
// ingress 上只有 police 可整形，而目标内核的 police 记账精度不够
// （20 Mbps 实测 6.5 Mbps），因此改用 mirred 重定向到 IFB 设备做 HTB。
// 少一个 action 词，上行限速就整个失效且不报错。
func TestIngressRedirectShape(t *testing.T) {
	got := buildIngressRedirectArgs("wg0", 43, "10.8.0.2")
	want := []string{
		"filter", "replace", "dev", "wg0", "protocol", "ip", "parent", "ffff:",
		"prio", "43", "u32",
		"match", "ip", "src", "10.8.0.2/32",
		"action", "mirred", "egress", "redirect", "dev", "wgifb0",
	}
	if !slices.Equal(got, want) {
		t.Errorf("buildIngressRedirectArgs =\n%v\nwant\n%v", got, want)
	}
	// parent 必须是 ingress qdisc 的 handle，挂在别处不会触发。
	if p := got[slices.Index(got, "parent")+1]; p != rateIngressHandle {
		t.Errorf("重定向规则的 parent = %q，期望 ingress qdisc %q", p, rateIngressHandle)
	}
	// ingress qdisc 与 egress 根 qdisc 是两个独立对象，handle 不能相同。
	if rateIngressHandle == rateRootHandle {
		t.Error("ingress 与 egress qdisc 的 handle 相同，第二条命令会报 File exists")
	}
}

// TestRateClassMinorNeverCollidesWithRoot 锁定 minor 号必须避开 root class。
//
// 真机上暴露的缺陷：minor 直接用 Peer 主键，而新建库的第一个 Peer 主键就是 1，
// 与 root class 的 1:1 撞号。结果是这个 Peer 的带宽被写进 root class，
// 表现为 tc 里只剩 "class htb 1:1 root rate 1Mbit"，其他 Peer 一起被限。
func TestRateClassMinorNeverCollidesWithRoot(t *testing.T) {
	for id := int64(1); id <= 100; id++ {
		minor := rateClassMinor(id)
		if minor == rateRootClassMinor {
			t.Fatalf("peer %d 的 minor 与 root class 撞号", id)
		}
		if minor != id+rateRootClassMinor {
			t.Errorf("peer %d 的 minor = %d，期望 %d", id, minor, id+rateRootClassMinor)
		}
	}
	// 多个 Peer 之间也不能撞号。
	seen := map[int64]int64{}
	for id := int64(1); id <= 1000; id++ {
		m := rateClassMinor(id)
		if prev, dup := seen[m]; dup {
			t.Errorf("peer %d 与 peer %d 映射到同一 minor %d", prev, id, m)
		}
		seen[m] = id
	}
}

// TestRateClassMinorRejectsUnusable 确认无法映射时返回 0，
// 由调用方拒绝下发，而不是悄悄用一个小概率错误的号覆盖别人的 class。
func TestRateClassMinorRejectsUnusable(t *testing.T) {
	for _, id := range []int64{0, -1, maxClassID} {
		if got := rateClassMinor(id); got != 0 {
			t.Errorf("rateClassMinor(%d) = %d，期望 0（拒绝下发）", id, got)
		}
	}
	if got := rateClassMinor(maxClassID - 1); got != maxClassID {
		t.Errorf("边界内应可映射，得到 %d", got)
	}
}

func TestRootClassArgs(t *testing.T) {
	got := rootClassArgs("wg0", 30)
	want := []string{
		"dev", "wg0", "parent", "1:", "classid", "1:1",
		"htb", "rate", "30mbit", "ceil", "30mbit",
	}
	if !slices.Equal(got, want) {
		t.Errorf("rootClassArgs =\n%v\nwant\n%v", got, want)
	}
}

func TestBuildRateArgsSetsRateEqualsCeil(t *testing.T) {
	// ceil > rate 会产生令牌桶突发；rate == ceil 才是真正的硬上限。
	classArgs, _ := buildRateArgs("wg0", 8, "10.8.0.7", 3, rateEgress)
	ri := slices.Index(classArgs, "rate")
	ci := slices.Index(classArgs, "ceil")
	if ri < 0 || ci < 0 {
		t.Fatalf("classArgs 缺少 rate/ceil: %v", classArgs)
	}
	if classArgs[ri+1] != classArgs[ci+1] {
		t.Errorf("rate=%q 与 ceil=%q 应相等，否则不是硬限速", classArgs[ri+1], classArgs[ci+1])
	}
}

func TestChangeRateArgsTargetsSameClass(t *testing.T) {
	got := changeRateArgs("wg1", 10, 50)
	want := []string{
		"class", "change", "dev", "wg1", "parent", "1:1", "classid", "1:10",
		"htb", "rate", "50mbit", "ceil", "50mbit",
	}
	if !slices.Equal(got, want) {
		t.Errorf("changeRateArgs =\n%v\nwant\n%v", got, want)
	}
}

func TestRootRateMbps(t *testing.T) {
	cases := []struct {
		name   string
		limits []RateLimit
		want   int
	}{
		{"none", nil, rateFallbackMbps},
		{"allZero", []RateLimit{{ID: 1, Mbps: 0}}, rateFallbackMbps},
		{"single", []RateLimit{{ID: 1, Mbps: 10}}, 15},
		{"sum", []RateLimit{{ID: 1, Mbps: 10}, {ID: 2, Mbps: 10}}, 30},
		{"negativeIgnored", []RateLimit{{ID: 1, Mbps: -5}, {ID: 2, Mbps: 10}}, 15},
	}
	for _, c := range cases {
		if got := rootRateMbps(c.limits); got != c.want {
			t.Errorf("%s: rootRateMbps = %d, want %d", c.name, got, c.want)
		}
	}
}

// 根 class 必须严格大于所有子 class 之和，否则未设限速的流量会被卡死在默认 class。
func TestRootRateExceedsSumOfChildren(t *testing.T) {
	limits := []RateLimit{
		{ID: 1, Address: "10.8.0.2", Mbps: 20},
		{ID: 2, Address: "10.8.0.3", Mbps: 20},
	}
	sum := 0
	for _, l := range limits {
		sum += l.Mbps
	}
	if rootRateMbps(limits) <= sum {
		t.Errorf("根带宽 %d 必须大于子 class 之和 %d", rootRateMbps(limits), sum)
	}
}

func TestParseClassIDs(t *testing.T) {
	out := `class htb 1:1 root prio 0 rate 10Gbit ceil 10Gbit burst 0b cburst 0b
class htb 1:12 parent 1:1 leaf 8012: prio 0 rate 20Mbit ceil 20Mbit burst 0b cburst 0b
class htb 1:34 parent 1:1 leaf 8034: prio 0 rate 5Mbit ceil 5Mbit burst 0b cburst 0b
qdisc htb 1: root refcnt 2 r2q 10 default 0x1 direct_packets_stat 0
class netem 30:1 parent 30:
`
	got := parseClassIDs(out)
	want := map[int64]struct{}{1: {}, 12: {}, 34: {}}
	if len(got) != len(want) {
		t.Fatalf("parseClassIDs = %v, want %v", got, want)
	}
	for id := range want {
		if _, ok := got[id]; !ok {
			t.Errorf("缺少 class %d，实际 %v", id, got)
		}
	}
}

func TestParseClassIDsIgnoresForeignAndInvalid(t *testing.T) {
	out := `class htb 2:5 parent 2:1
class htb 1:0 root
class htb 1:70000 parent 1:1
class htb 1:abc parent 1:1
class fq_codel 0: parent 1:
`
	if got := parseClassIDs(out); len(got) != 0 {
		t.Errorf("非本 qdisc、minor 为 0、越界与非数字都应忽略，实际 %v", got)
	}
}

func TestParseClassIDsEmptyOutput(t *testing.T) {
	if got := parseClassIDs(""); len(got) != 0 {
		t.Errorf("空输出应返回空集合，实际 %v", got)
	}
}

// filter 用 replace：每轮全量下发都走同一条 u32 规则，
// 用 add 会累积出多条匹配同一地址的规则，命中的是先挂上的旧 class。
func TestFilterArgsAreReplaceNotAdd(t *testing.T) {
	_, filterArgs := buildRateArgs("wg0", 6, "10.8.0.5", 10, rateEgress)
	if filterArgs[0] != "filter" || filterArgs[1] != "replace" {
		t.Errorf("filter 动作为 %q，期望 replace", strings.Join(filterArgs[:2], " "))
	}
	args := buildIngressRedirectArgs("wg0", 6, "10.8.0.5")
	if args[0] != "filter" || args[1] != "replace" {
		t.Errorf("重定向规则动作为 %q，期望 replace", strings.Join(args[:2], " "))
	}
}

// TestFilterAndClassTargetSameHandle 确认 filter 的 flowid 与 classid 完全一致。
// 不一致时 tc 不报错，但流量被分类到一个不存在的桶上，表现为限速毫无效果。
func TestFilterAndClassTargetSameHandle(t *testing.T) {
	for _, classID := range []int64{2, 43, 65534} {
		for _, dir := range []rateDirection{rateEgress, rateIngress} {
			classArgs, filterArgs := buildRateArgs("wg0", classID, "10.8.0.9", 10, dir)
			want := "1:" + strconv.FormatInt(classID, 10)
			ci := slices.Index(classArgs, "classid")
			if ci < 0 || classArgs[ci+1] != want {
				t.Errorf("classid = %v，期望 %s", classArgs, want)
			}
			fi := slices.Index(filterArgs, "flowid")
			if fi < 0 || filterArgs[fi+1] != want {
				t.Errorf("flowid = %v，期望 %s", filterArgs, want)
			}
		}
	}
}

// 没有任何 Peer 限速时必须走清理而不是建一棵空树。
//
// 真机上暴露的缺陷：集合为空时仍然 del 再 add 根 qdisc，建出一棵
// 只含 root class 的 HTB。它不分流也不整形，却让「内核里是否还有限速」
// 这个判据永远为真（或永远为假，视实现而定），结果是调谐器每 2 秒
// 重复下发一次，日志被刷满，真出问题时反而找不到有效信息。
func TestHasRateLimits(t *testing.T) {
	cases := []struct {
		name   string
		limits []RateLimit
		want   bool
	}{
		{"nil", nil, false},
		{"empty", []RateLimit{}, false},
		{"zeroMbps", []RateLimit{{ID: 1, Address: "10.8.0.2", Mbps: 0}}, false},
		{"negativeMbps", []RateLimit{{ID: 1, Address: "10.8.0.2", Mbps: -1}}, false},
		{"noAddress", []RateLimit{{ID: 1, Address: "", Mbps: 10}}, false},
		{"oneValid", []RateLimit{{ID: 1, Address: "10.8.0.2", Mbps: 10}}, true},
		{"validAmongInvalid", []RateLimit{
			{ID: 1, Address: "", Mbps: 10},
			{ID: 2, Address: "10.8.0.3", Mbps: 5},
		}, true},
	}
	for _, c := range cases {
		if got := hasRateLimits(c.limits); got != c.want {
			t.Errorf("%s: hasRateLimits = %v, want %v", c.name, got, c.want)
		}
	}
}

// RateLimitApplied 的判据必须是「存在 Peer class」，空树不算已应用。
func TestRateLimitAppliedIgnoresEmptyTree(t *testing.T) {
	emptyTree := `class htb 1:1 root prio 0 rate 10Gbit ceil 10Gbit burst 0b cburst 0b
`
	if got := parsePeerClassIDs(emptyTree); len(got) != 0 {
		t.Errorf("只有 root class 时不应判定为已应用，实际 %v", got)
	}

	realTree := `class htb 1:1 root prio 0 rate 30Mbit ceil 30Mbit burst 0b cburst 0b
class htb 1:2 parent 1:1 prio 0 rate 20Mbit ceil 20Mbit burst 0b cburst 0b
`
	if got := parsePeerClassIDs(realTree); len(got) != 1 {
		t.Errorf("存在 Peer class 时应判定为已应用，实际 %v", got)
	}
}

// ratePeerHandle 必须拒绝无法映射的条目，否则会用错误的桶覆盖别人的限速。
func TestRatePeerHandle(t *testing.T) {
	cases := []struct {
		name string
		in   RateLimit
		ok   bool
	}{
		{"valid", RateLimit{ID: 1, Address: "10.8.0.2", Mbps: 10}, true},
		{"noAddress", RateLimit{ID: 1, Address: "", Mbps: 10}, false},
		{"zeroID", RateLimit{ID: 0, Address: "10.8.0.2", Mbps: 10}, false},
		{"overflow", RateLimit{ID: maxClassID, Address: "10.8.0.2", Mbps: 10}, false},
	}
	for _, c := range cases {
		_, ok := ratePeerHandle(c.in)
		if ok != c.ok {
			t.Errorf("%s: ratePeerHandle ok = %v, want %v", c.name, ok, c.ok)
		}
	}
}
