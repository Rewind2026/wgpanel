package wgdriver

import (
	"context"
	"net"
	"time"

	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"

	"github.com/Rewind2026/wgpanel/internal/domain"
)

// PeerState 是单个 Peer 在内核侧的实际状态快照。
type PeerState struct {
	PublicKey                   wgtypes.Key
	AllowedIPs                  []net.IPNet
	Endpoint                    *net.UDPAddr
	PersistentKeepaliveInterval time.Duration
	RxBytes                     int64
	TxBytes                     int64
	LastHandshake               time.Time
}

// DeviceState 是接口的实际状态快照，用于调谐对比。
type DeviceState struct {
	Name         string
	ListenPort   int
	PublicKey    wgtypes.Key
	FirewallMark int
	Peers        []PeerState
}

// Driver 抽象 WireGuard 控制面，便于在非 Linux 环境打桩测试。
type Driver interface {
	// Available 报告驱动是否可用（内核模块/权限/平台）。
	Available() error
	// Device 读取接口当前状态。接口不存在时返回 nil, nil。
	Device(ctx context.Context, name string) (*DeviceState, error)
	// Up 创建/更新接口并下发全量配置。
	Up(ctx context.Context, cfg *domain.ServerConfig, peers []domain.Peer) error
	// Down 删除接口。
	Down(ctx context.Context, name string) error
	// SyncPeers 将期望态 Peer 集合同步到内核：新增/更新下发，缺失项删除。
	SyncPeers(ctx context.Context, name string, peers []domain.Peer) error
	// PeerStats 读取每个 Peer 的实时统计，key 为公钥字符串。
	PeerStats(name string) (map[string]*domain.PeerStats, error)
	// TotalCounters 返回接口级累计收发字节。
	TotalCounters(name string) (rx, tx int64, err error)
}
