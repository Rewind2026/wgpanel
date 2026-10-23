package reconciler

import (
	"context"
	"strings"

	"github.com/Rewind2026/wgpanel/internal/domain"
)

// applyLink 修复链路口的漂移。
//
// 为什么必须单独修：wgctrl 只能看到 WireGuard 控制面（端口、公钥、Peer、计数器），
// 看不到 link 本身。接口被 down、MTU 被改、地址被摘掉这些操作执行得很成功，
// 控制面查询一切正常，但流量已经进不来也出不去——只能由 netdriver 读回真实状态再修。
//
// 这里每轮调谐都做：读一次 LinkInfo 是纯 netlink 读取，成本远低于 fork 外部进程，
// 换来的是「外部误操作后最多一个调谐周期自愈」，而不是等五分钟的兜底重放。
func (r *Reconciler) applyLink(ctx context.Context, cfg *domain.ServerConfig) {
	repairs, err := r.repairLink(ctx, cfg)

	r.mu.Lock()
	changed := (err == nil) != (r.linkErr == nil)
	r.linkErr = err
	r.mu.Unlock()

	switch {
	case err != nil && changed:
		r.log.Warn("link repair failed", "interface", cfg.Interface, "error", err)
	case err != nil:
		r.log.Debug("link repair still failing", "error", err)
	default:
		for _, rep := range repairs {
			r.log.Warn("link drift corrected", "interface", cfg.Interface, "repair", rep)
		}
	}
}

// repairLink 计算并执行链路修复，返回实际做出的修复动作描述。
func (r *Reconciler) repairLink(ctx context.Context, cfg *domain.ServerConfig) ([]string, error) {
	info, err := r.net.LinkInfo(cfg.Interface)
	if err != nil {
		return nil, err
	}

	var repairs []string
	if !info.Exists {
		if err := r.net.EnsureLink(ctx, cfg.Interface, cfg.MTU); err != nil {
			return nil, err
		}
		// EnsureLink 只保证存在与 MTU，地址和 up 还要单独补。
		if err := r.net.EnsureAddress(ctx, cfg.Interface, cfg.AddressCIDR); err != nil {
			return nil, err
		}
		if err := r.net.SetLinkUp(ctx, cfg.Interface); err != nil {
			return nil, err
		}
		return []string{"recreated"}, nil
	}

	if !info.Up {
		if err := r.net.SetLinkUp(ctx, cfg.Interface); err != nil {
			return nil, err
		}
		repairs = append(repairs, "link-up")
	}
	if cfg.MTU > 0 && info.MTU != cfg.MTU {
		if err := r.net.SetMTU(ctx, cfg.Interface, cfg.MTU); err != nil {
			return repairs, err
		}
		repairs = append(repairs, "mtu")
	}
	if missing := missingAddresses(cfg.AddressCIDR, info.Addresses); len(missing) > 0 {
		if err := r.net.EnsureAddress(ctx, cfg.Interface, cfg.AddressCIDR); err != nil {
			return repairs, err
		}
		repairs = append(repairs, "address")
	}
	return repairs, nil
}

// missingAddresses 返回期望地址中实际缺失的部分。
//
// 允许多配：用户额外挂了第二个地址不是漂移，不能把多出来的地址当成不一致，
// 否则每轮调谐都会无意义地下发一次 EnsureAddress。
func missingAddresses(cidrList string, actual []string) []string {
	want := splitList(cidrList)
	if len(want) == 0 {
		return nil
	}
	have := make(map[string]struct{}, len(actual))
	for _, a := range actual {
		have[a] = struct{}{}
	}
	var missing []string
	for _, w := range want {
		if _, ok := have[w]; !ok {
			missing = append(missing, w)
		}
	}
	return missing
}

// splitList 拆分逗号分隔的地址列表并去除空白。
func splitList(v string) []string {
	var out []string
	for _, s := range strings.Split(v, ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}
