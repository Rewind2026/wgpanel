// Package scheduler 承载周期性后台任务：流量降采样、过期数据清理、Endpoint 推断。
//
// 与 cmd 分离的理由：这些任务属于业务编排，测试与复用不应依赖 main 包。
package scheduler

import (
	"context"
	"log/slog"
	"net"
	"os"
	"strings"
	"time"

	"github.com/Rewind2026/wgpanel/internal/config"
	"github.com/Rewind2026/wgpanel/internal/netdriver"
	"github.com/Rewind2026/wgpanel/internal/service"
	"github.com/Rewind2026/wgpanel/internal/store"
)

// Scheduler 按固定节奏运行维护任务。
type Scheduler struct {
	cfg   config.Config
	store *store.Store
	log   *slog.Logger
}

// New 构造调度器。
func New(cfg config.Config, st *store.Store, log *slog.Logger) *Scheduler {
	return &Scheduler{cfg: cfg, store: st, log: log}
}

// Start 运行调度循环，直到 ctx 取消。
func (s *Scheduler) Start(ctx context.Context) {
	hourly := time.NewTicker(time.Hour)
	defer hourly.Stop()

	// 启动 1 分钟后执行一次初始降采样，保证 10m 桶不空。
	initial := time.NewTimer(time.Minute)
	defer initial.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-initial.C:
			s.runRollup(ctx)
			s.runDailyRollup(ctx)
		case <-hourly.C:
			s.runRollup(ctx)
			s.runDailyRollup(ctx)
			s.runCleanup(ctx)
		}
	}
}

// runRollup 把原始采样聚合到 10m 与 hour 桶。
func (s *Scheduler) runRollup(ctx context.Context) {
	repo := s.store.TrafficRepo()
	now := time.Now()
	if err := repo.Rollup(ctx, "10m", now.Add(-7*24*time.Hour).Unix(), 600); err != nil {
		s.log.Error("rollup 10m failed", "error", err)
	}
	if err := repo.Rollup(ctx, "hour", now.Add(-90*24*time.Hour).Unix(), 3600); err != nil {
		s.log.Error("rollup hour failed", "error", err)
	}
}

// runDailyRollup 聚合到 day 桶。
//
// 与 10m/hour 桶不同，day 桶原本挂在 24 小时的 ticker 上，
// 意味着新部署后的头 24 小时内 day 桶必然为空——
// 界面上「最近 30 天」会返回空数组，而不是一条短曲线。
//
// 降采样本身是幂等的（ON CONFLICT DO UPDATE 取更大的累计值），
// 重复执行不会污染数据，因此这里跟着 10m/hour 一起跑，
// 让 day 桶在启动一分钟后就有内容。
func (s *Scheduler) runDailyRollup(ctx context.Context) {
	if err := s.store.TrafficRepo().Rollup(ctx, "day", time.Now().Add(-730*24*time.Hour).Unix(), 86400); err != nil {
		s.log.Error("rollup day failed", "error", err)
	}
}

// runCleanup 按保留策略删除过期数据。
func (s *Scheduler) runCleanup(ctx context.Context) {
	now := time.Now()
	traffic := s.store.TrafficRepo()

	steps := []struct {
		bucket string
		keep   time.Duration
	}{
		{"raw", s.cfg.RetentionRaw},
		{"10m", s.cfg.RetentionMin},
		{"hour", s.cfg.RetentionHour},
		{"day", s.cfg.RetentionDay},
	}
	for _, step := range steps {
		n, err := traffic.DeleteOlderThan(ctx, step.bucket, now.Add(-step.keep).Unix())
		if err != nil {
			s.log.Error("cleanup traffic failed", "bucket", step.bucket, "error", err)
			continue
		}
		if n > 0 {
			s.log.Info("traffic data cleaned", "bucket", step.bucket, "rows", n)
		}
	}

	if n, err := traffic.DeleteSystemMetricsBefore(ctx, now.Add(-7*24*time.Hour).Unix()); err != nil {
		s.log.Error("cleanup system metrics failed", "error", err)
	} else if n > 0 {
		s.log.Info("system metrics cleaned", "rows", n)
	}
	if n, err := s.store.AuditRepo().DeleteOlderThan(ctx, now.Add(-s.cfg.AuditRetention).Unix()); err != nil {
		s.log.Error("cleanup audit failed", "error", err)
	} else if n > 0 {
		s.log.Info("audit logs cleaned", "rows", n)
	}
	if n, err := s.store.UserRepo().CleanupExpiredTokens(ctx); err != nil {
		s.log.Error("cleanup tokens failed", "error", err)
	} else if n > 0 {
		s.log.Info("expired tokens cleaned", "rows", n)
	}
}

// DetectPublicEndpoint 推断服务端公网地址。
//
// 优先级分两档：
//
//	managed：环境变量 > 面板设置 > 服务端监听地址 > 容器出口 IP
//	host / ipvlan / macvlan：
//	     环境变量 > 面板设置 > 上行接口 IPv4 > 上行接口全局 IPv6 > 监听地址 > 出口 IP
//
// host/L2 模式多出的两档是模式可达性的直接结果：这些部署里上行接口
// （macvlan/ipvlan 容器的 eth0、host 模式的物理网卡）持有局域网真实地址，
// 其中 IPv6 由上游路由器 RA/SLAAC 下发的全局地址（GUA）可被外部设备直连。
// managed 模式没有这一档——eth0 是 Docker 默认网络/K8s Pod 的 NAT 内网地址，
// 客户端根本路由不到，把它们写进配置只会制造「配置正常但连不上」的假象。
//
// 服务端监听地址这一级有两个坑：
//   - ListenIPv4 是 wg 接口的隧道地址（如 10.8.0.1），客户端根本路由不到它，
//     把它当 Endpoint 写进客户端配置会让连接必然失败；
//   - ListenIPv6 同理，且还要处理带掩码与 zone 的形式。
//
// 因此这里只接受明确看起来像公网地址的值：不含 CIDR 掩码、不以常见私网段开头。
// 都不满足时返回空串，让导出配置里不出现 Endpoint 行——
// 一个没有 Endpoint 的配置至少还能让用户手工填，
// 一个填错的 Endpoint 则会让用户以为是服务端故障。
//
// 最后一级（容器出口 IP）在 NAT/多网卡/端口映射下未必是客户端可达地址，
// 因此调用方必须把它当提示展示，不允许静默写回配置。
func DetectPublicEndpoint(ctx context.Context, mode netdriver.Mode, svc *service.ServerService, settings *store.SettingRepo) string {
	if v := strings.TrimSpace(os.Getenv("WGP_PUBLIC_ENDPOINT")); v != "" {
		return v
	}
	// 面板设置里的显式覆盖优先级最高：容器内的自动探测在 NAT 后面几乎必然猜错，
	// 管理员在界面上填的地址才是真正想让客户端用的那个。
	if settings != nil {
		if st, err := settings.LoadSettings(ctx); err == nil {
			if v := strings.TrimSpace(st.PublicEndpoint); v != "" {
				return v
			}
		}
	}
	// host/L2 模式：上行接口的地址就是本机在链路层的真实地址，
	// 局域网客户端走 IPv4、外部设备走 RA 下发的 GUA，都比隧道地址更可达。
	if mode != netdriver.ModeManaged {
		iface, v4, v6 := netdriver.UplinkAddresses()
		if iface != "" {
			for _, a := range v4 {
				if v := normalizeLANEndpoint(a); v != "" {
					return v
				}
			}
			for _, a := range v6 {
				if v := normalizeEndpoint(a); v != "" {
					return v
				}
			}
		}
	}
	if cfg, err := svc.Get(ctx); err == nil {
		if v := normalizeEndpoint(cfg.ListenIPv4); v != "" {
			return v
		}
		if v := normalizeEndpoint(cfg.ListenIPv6); v != "" {
			return v
		}
	}
	return outboundIP(ctx)
}

// normalizeLANEndpoint 规整上行接口的 IPv4 候选地址。
// 与 normalizeEndpoint 的差别在于放行 RFC1918 私网：L2/host 部署里
// 上行地址就是客户端可达的真实地址（局域网场景），不能用公网性一票否决；
// 但回环、链路本地、带掩码、带 zone 的值依然不可用。
func normalizeLANEndpoint(addr string) string {
	v := strings.TrimSpace(addr)
	if v == "" {
		return ""
	}
	if strings.Contains(v, "/") {
		return ""
	}
	if i := strings.IndexByte(v, '%'); i >= 0 {
		return ""
	}
	ip := net.ParseIP(v)
	if ip == nil || ip.To4() == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
		return ""
	}
	return v
}

// normalizeEndpoint 把监听地址规整成可写入 Endpoint 的形式，
// 私有网段与带掩码的值一律视为不可用。
func normalizeEndpoint(addr string) string {
	v := strings.TrimSpace(addr)
	if v == "" {
		return ""
	}
	// 带掩码说明这是接口地址而不是对外可达的 endpoint。
	if strings.Contains(v, "/") {
		return ""
	}
	// IPv6 的 zone 标识（fe80::1%eth0）只在本地有意义。
	if i := strings.IndexByte(v, '%'); i >= 0 {
		return ""
	}
	ip := net.ParseIP(v)
	if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() || isPrivateIP(ip) {
		return ""
	}
	return v
}

// isPrivateIP 判断是否属于 RFC1918 / RFC4193 私有网段。
func isPrivateIP(ip net.IP) bool {
	return ip.IsPrivate() || ip.IsUnspecified()
}

// outboundIP 通过一次 UDP connect 推断出口 IP（不会真正发包）。
func outboundIP(ctx context.Context) string {
	dialer := net.Dialer{Timeout: time.Second}
	conn, err := dialer.DialContext(ctx, "udp", "1.1.1.1:80")
	if err != nil {
		return ""
	}
	defer conn.Close()
	host, _, err := net.SplitHostPort(conn.LocalAddr().String())
	if err != nil || host == "" {
		return ""
	}
	// 容器里看到的是 NAT 内网地址（172.17.0.x / 10.x.x.x），
	// 客户端永远路由不到它。返回它比返回空串更糟：
	// 会被当作可信地址写进 Endpoint，让所有客户端连不上。
	return normalizeEndpoint(host)
}
