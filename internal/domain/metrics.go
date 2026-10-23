package domain

import "time"

// PeerStats 是单个 Peer 的实时运行指标。
// 必须携带身份字段：内核侧只给公钥，没有 PeerID 就无法把流量归因到具体客户端，
// 也就无法落库每 Peer 的历史曲线。
type PeerStats struct {
	PeerID           int64     `json:"peerId"`
	PublicKey        string    `json:"publicKey"`
	Online           bool      `json:"online"`
	AllowedIPs       []string  `json:"allowedIps"`
	RxBytes          int64     `json:"rxBytes"`
	TxBytes          int64     `json:"txBytes"`
	RxRateBps        int64     `json:"rxRateBps"`
	TxRateBps        int64     `json:"txRateBps"`
	LastHandshake    time.Time `json:"lastHandshake"`
	ConnectedSince   time.Time `json:"connectedSince"`
	ConnectedSeconds int64     `json:"connectedSeconds"`
}

// SystemMetrics 是容器内采集的主机资源指标。
type SystemMetrics struct {
	CPUPercent    float64 `json:"cpuPercent"`
	MemUsedBytes  int64   `json:"memUsedBytes"`
	MemPercent    float64 `json:"memPercent"`
	DiskUsedBytes int64   `json:"diskUsedBytes"`
	DiskPercent   float64 `json:"diskPercent"`
	NetRxBps      int64   `json:"netRxBps"`
	NetTxBps      int64   `json:"netTxBps"`
	NetRxBytes    int64   `json:"netRxBytes"`
	NetTxBytes    int64   `json:"netTxBytes"`
	UptimeSeconds int64   `json:"uptimeSeconds"`
	Goroutines    int     `json:"goroutines"`
}

// TunnelMetrics 是隧道整体指标。
type TunnelMetrics struct {
	Interface      string    `json:"interface"`
	ListenPort     int       `json:"listenPort"`
	PublicEndpoint string    `json:"publicEndpoint"`
	RxBytes        int64     `json:"rxBytes"`
	TxBytes        int64     `json:"txBytes"`
	RxRateBps      int64     `json:"rxRateBps"`
	TxRateBps      int64     `json:"txRateBps"`
	OnlinePeers    int       `json:"onlinePeers"`
	TotalPeers     int       `json:"totalPeers"`
	InterfaceUp    bool      `json:"interfaceUp"`
	CollectedAt    time.Time `json:"collectedAt"`
}

// MetricsSnapshot 是一次完整的监控快照，通过 SSE 单帧下发。
type MetricsSnapshot struct {
	System SystemMetrics `json:"system"`
	Tunnel TunnelMetrics `json:"tunnel"`
	Peers  []PeerStats   `json:"peers"`
}

// TrafficSample 是流量时序采样点。PeerID 为 0 表示隧道汇总行。
type TrafficSample struct {
	TS          int64 `json:"ts"`
	PeerID      int64 `json:"peerId"`
	RxBytes     int64 `json:"rxBytes"`
	TxBytes     int64 `json:"txBytes"`
	RxPps       int64 `json:"rxPps"`
	TxPps       int64 `json:"txPps"`
	HandshakeTS int64 `json:"handshakeTs,omitempty"`
}

// TrafficRange 表示历史查询的时间范围与聚合粒度。
type TrafficRange struct {
	// Window 取值 1h / 24h / 7d / 30d
	Window string `json:"window"`
	// Bucket 取值 raw / 10m / hour / day
	Bucket string `json:"bucket"`
}

// TrafficPoint 是历史曲线上的一个点。
type TrafficPoint struct {
	TS      int64 `json:"ts"`
	RxBytes int64 `json:"rxBytes"`
	TxBytes int64 `json:"txBytes"`
	RxRate  int64 `json:"rxRate"`
	TxRate  int64 `json:"txRate"`
}
