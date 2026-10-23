package reconciler

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"

	"github.com/Rewind2026/wgpanel/internal/domain"
	"github.com/Rewind2026/wgpanel/internal/netdriver"
)

// applyRateLimits 幂等下发 Peer 级带宽限速。
//
// 限速和转发/路由的处理时机不同：转发与路由一经建立就不再变化，适合低频兜底重放；
// 而限速跟着 Peer 的增删改走，如果同样压到 netResyncInterval（分钟级），
// 用户改完带宽要等好几分钟才生效。因此这里每轮调谐都检查，
// 用「期望集合的指纹」去重，没变化就不 exec，有变化才完整下发一次。
func (r *Reconciler) applyRateLimits(ctx context.Context, cfg *domain.ServerConfig, peers []domain.Peer) {
	limits := rateLimitsOf(peers)
	fp := rateFingerprint(cfg.Interface, limits)

	r.mu.Lock()
	settled := fp == r.rateFP
	r.mu.Unlock()
	// 指纹只覆盖「期望集合」，覆盖不了内核侧的意外消失：
	// tc 状态只存在于内核内存，`tc qdisc del`、接口重建、容器迁移都会让它凭空没了，
	// 而数据库里的期望集合纹丝不动。此时必须重新下发，否则限速会静默失效，
	// 且只有等用户碰巧改了带宽才会恢复。
	//
	// 但这只在「期望集合非空」时成立。期望为空时正确的终态是内核里什么都没有，
	// 此时任何存在性检查都会返回 false，每一轮都被判成「需要重新下发」，
	// 于是每 2 秒执行一次 tc 清理，日志刷满，CPU 白白浪费。
	if settled && len(limits) > 0 && !r.net.RateLimitApplied(cfg.Interface) {
		settled = false
	}
	if settled {
		return
	}

	err := r.net.ApplyRateLimits(ctx, cfg.Interface, limits)

	r.mu.Lock()
	prevErr := r.rateErr
	recovered := prevErr != nil && err == nil
	// 失败时不记指纹：期望态没变，但内核状态没跟上，
	// 必须下一轮继续重试。若把指纹也记下来，一次失败就变成永久失败——
	// 这正是真机上"限速永远不生效"的原因。
	if err == nil || errors.Is(err, netdriver.ErrRateLimitUnsupported) {
		r.rateFP = fp
	}
	r.rateErr = err
	r.mu.Unlock()

	switch {
	case err == nil:
		if recovered {
			r.log.Info("rate limits recovered", "interface", cfg.Interface)
		} else {
			r.log.Info("rate limits applied", "interface", cfg.Interface, "count", len(limits))
		}
	case errors.Is(err, netdriver.ErrRateLimitUnsupported):
		// tc 缺失是容器镜像问题，不会自己恢复；
		// 仍然记下指纹，否则每 2 秒都会去执行一次注定失败的命令。
		r.log.Warn("rate limits unavailable", "error", err)
	default:
		// 失败必须是 WARN 而不是 DEBUG。限速不生效属于"用户设了但没生效"，
		// 只在 DEBUG 记录等于把故障藏起来：面板看起来一切正常，
		// 用户抱怨带宽限速没用，日志里却什么都没有。
		// 首次失败升到 WARN，后续轮次降到 DEBUG，避免每 2 秒刷一条。
		if prevErr == nil {
			r.log.Warn("rate limits failed", "interface", cfg.Interface, "error", err)
		} else {
			r.log.Debug("rate limits still failing", "interface", cfg.Interface, "error", err)
		}
	}
}

// rateLimitsOf 提取需要限速的 Peer。
//
// 只取启用且配额为正的：被停用的 Peer 不下发 class，
// 否则它会留在 tc 上白占一个桶位，等复用同一主键时才被意外继承。
func rateLimitsOf(peers []domain.Peer) []netdriver.RateLimit {
	out := make([]netdriver.RateLimit, 0, len(peers))
	for _, p := range peers {
		if !p.Enabled || p.RateLimitMbps <= 0 {
			continue
		}
		addr := hostAddress(p.Address)
		if addr == "" {
			continue
		}
		out = append(out, netdriver.RateLimit{ID: p.ID, Address: addr, Mbps: p.RateLimitMbps})
	}
	return out
}

// hostAddress 去掉 CIDR 掩码，只留主机地址（10.8.0.2/32 → 10.8.0.2）。
func hostAddress(addr string) string {
	if i := strings.IndexByte(addr, '/'); i >= 0 {
		addr = addr[:i]
	}
	return strings.TrimSpace(addr)
}

// rateFingerprint 生成期望限速集合的指纹。
//
// 指纹必须只由集合决定、与顺序无关：数据库查询顺序一旦变化（比如加了新的排序条件、
// 或换了个索引），如果直接按遍历顺序拼接就会误判成「有变更」，
// 于是白白重放一次 tc。因此先按键排序再拼接。
func rateFingerprint(dev string, limits []netdriver.RateLimit) string {
	entries := make([]string, 0, len(limits))
	for _, l := range limits {
		var b strings.Builder
		b.WriteString(strconv.FormatInt(l.ID, 10))
		b.WriteByte(':')
		b.WriteString(l.Address)
		b.WriteByte(':')
		b.WriteString(strconv.Itoa(l.Mbps))
		entries = append(entries, b.String())
	}
	sort.Strings(entries)

	var out strings.Builder
	out.WriteString(dev)
	for _, e := range entries {
		out.WriteByte('|')
		out.WriteString(e)
	}
	return out.String()
}

// RateLimitError 返回最近一次限速下发错误，用于就绪探针暴露降级状态。
func (r *Reconciler) RateLimitError() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.rateErr
}
