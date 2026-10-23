package httpapi

import (
	"fmt"
	"net/http"
	"strings"
	"time"
)

// handlePrometheus 输出 Prometheus 文本格式指标。
//
// 不引入 prometheus/client_golang：面板只需要十来个固定指标，
// 完整客户端库会带进注册表、标签推导与一堆用不上的类型，
// 而「一条命令部署 + 静态单文件」是这个项目的硬约束。
//
// 代价是指标集合是写死的，新增指标必须改这里；
// 换来的是零依赖、零反射、行为完全可预测。
func (s *Server) handlePrometheus(w http.ResponseWriter, r *http.Request) {
	snap := s.collector.SnapshotNow(r.Context())
	// 配额比例需要配额上限，采集快照里没有，因此单独读一次 Peer 配置。
	peers, err := s.peers.List(r.Context())
	if err != nil {
		s.log.Warn("prometheus export degraded: peer list failed", "error", err)
		peers = nil
	}
	peerByID := make(map[int64]peerMeta, len(peers))
	for i := range peers {
		peerByID[peers[i].ID] = peerMeta{
			name:          peers[i].Name,
			quotaBytes:    peers[i].TrafficQuotaBytes,
			quotaUsed:     peers[i].QuotaUsedBytes,
			rateLimitMbit: peers[i].RateLimitMbps,
		}
	}

	var b strings.Builder
	gauge := func(name, help string, value any) {
		fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s gauge\n%s %v\n", name, help, name, name, value)
	}

	gauge("wgpanel_up", "面板进程与采集链路是否可用", 1)
	gauge("wgpanel_uptime_seconds", "进程运行时长", time.Since(s.startedAt).Seconds())
	gauge("wgpanel_build_info", "构建版本，标签恒为 1", s.version)
	gauge("wgpanel_reconcile_last_success_timestamp_seconds", "最近一次成功调谐时间", s.lastReconcileSeconds())

	// 系统
	sys := snap.System
	gauge("wgpanel_system_cpu_percent", "CPU 占用百分比", sys.CPUPercent)
	gauge("wgpanel_system_memory_used_bytes", "已用内存", sys.MemUsedBytes)
	gauge("wgpanel_system_memory_percent", "内存占用百分比", sys.MemPercent)
	gauge("wgpanel_system_disk_used_bytes", "已用磁盘", sys.DiskUsedBytes)
	gauge("wgpanel_system_disk_percent", "磁盘占用百分比", sys.DiskPercent)
	gauge("wgpanel_system_goroutines", "Go 协程数", sys.Goroutines)

	// 隧道
	tun := snap.Tunnel
	interfaceUp := 0
	if tun.InterfaceUp {
		interfaceUp = 1
	}
	fmt.Fprintf(&b, "# HELP wgpanel_tunnel_up 接口是否在内核中存在\n# TYPE wgpanel_tunnel_up gauge\nwgpanel_tunnel_up %d\n", interfaceUp)
	gauge("wgpanel_tunnel_rx_bytes_total", "隧道累计接收字节", tun.RxBytes)
	gauge("wgpanel_tunnel_tx_bytes_total", "隧道累计发送字节", tun.TxBytes)
	gauge("wgpanel_tunnel_rx_bps", "隧道当前接收速率", tun.RxRateBps)
	gauge("wgpanel_tunnel_tx_bps", "隧道当前发送速率", tun.TxRateBps)
	gauge("wgpanel_tunnel_peers_total", "已配置 Peer 数", tun.TotalPeers)
	gauge("wgpanel_tunnel_peers_online", "当前在线 Peer 数", tun.OnlinePeers)

	// 每个 Peer 一条序列。标签只放标识与低基数属性，
	// 不放 note / tags / endpoint —— 那些是用户自由输入，放进标签会让时序爆炸。
	fmt.Fprint(&b, "# HELP wgpanel_peer_online Peer 是否在线\n# TYPE wgpanel_peer_online gauge\n")
	fmt.Fprint(&b, "# HELP wgpanel_peer_rx_bytes_total Peer 累计接收字节\n# TYPE wgpanel_peer_rx_bytes_total counter\n")
	fmt.Fprint(&b, "# HELP wgpanel_peer_tx_bytes_total Peer 累计发送字节\n# TYPE wgpanel_peer_tx_bytes_total counter\n")
	fmt.Fprint(&b, "# HELP wgpanel_peer_quota_used_bytes Peer 当前配额周期已用流量\n# TYPE wgpanel_peer_quota_used_bytes gauge\n")
	fmt.Fprint(&b, "# HELP wgpanel_peer_quota_ratio Peer 配额使用比例，1 表示已用满\n# TYPE wgpanel_peer_quota_ratio gauge\n")
	fmt.Fprint(&b, "# HELP wgpanel_peer_rate_limit_mbps Peer 限速上限，0 表示不限\n# TYPE wgpanel_peer_rate_limit_mbps gauge\n")

	for i := range snap.Peers {
		p := snap.Peers[i]
		meta, known := peerByID[p.PeerID]
		name := meta.name
		if !known {
			name = p.PublicKey[:min(8, len(p.PublicKey))]
		}
		labels := fmt.Sprintf(`{peer_id="%d",peer_name="%s"}`, p.PeerID, escapeLabel(name))
		online := 0
		if p.Online {
			online = 1
		}
		fmt.Fprintf(&b, "wgpanel_peer_online%s %d\n", labels, online)
		fmt.Fprintf(&b, "wgpanel_peer_rx_bytes_total%s %d\n", labels, p.RxBytes)
		fmt.Fprintf(&b, "wgpanel_peer_tx_bytes_total%s %d\n", labels, p.TxBytes)
		if !known {
			continue
		}
		fmt.Fprintf(&b, "wgpanel_peer_quota_used_bytes%s %d\n", labels, meta.quotaUsed)
		fmt.Fprintf(&b, "wgpanel_peer_rate_limit_mbps%s %d\n", labels, meta.rateLimitMbit)
		if meta.quotaBytes > 0 {
			ratio := float64(meta.quotaUsed) / float64(meta.quotaBytes)
			if ratio < 0 {
				ratio = 0
			}
			fmt.Fprintf(&b, "wgpanel_peer_quota_ratio%s %.4f\n", labels, ratio)
		}
	}

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(b.String()))
}

// peerMeta 是 Prometheus 导出需要的 Peer 附加信息。
type peerMeta struct {
	name          string
	quotaBytes    int64
	quotaUsed     int64
	rateLimitMbit int
}

// escapeLabel 转义 Prometheus 标签值里的特殊字符。
func escapeLabel(v string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)
	return r.Replace(v)
}

// lastReconcileSeconds 返回最近一次成功调谐的时间戳；从未成功过返回 0。
func (s *Server) lastReconcileSeconds() int64 {
	if s.lastReconcile == nil {
		return 0
	}
	t := s.lastReconcile()
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}
