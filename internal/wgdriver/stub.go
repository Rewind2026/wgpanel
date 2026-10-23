package wgdriver

import (
	"context"
	"fmt"

	"github.com/Rewind2026/wgpanel/internal/domain"
)

// Stub 是无内核环境下的内存实现，用于开发与测试。
type Stub struct {
	devices map[string]*DeviceState
	last    *domain.ServerConfig
	peers   map[string]domain.Peer
}

// NewStub 返回内存态的假驱动。
func NewStub() *Stub {
	return &Stub{
		devices: map[string]*DeviceState{},
		peers:   map[string]domain.Peer{},
	}
}

// Available 始终返回可用。
func (s *Stub) Available() error { return nil }

// Close 无需释放资源。
func (s *Stub) Close() {}

// Device 返回内存中的接口状态。
func (s *Stub) Device(ctx context.Context, name string) (*DeviceState, error) {
	d, ok := s.devices[name]
	if !ok {
		return nil, nil
	}
	return d, nil
}

// Up 在内存中记录期望配置。
func (s *Stub) Up(ctx context.Context, cfg *domain.ServerConfig, peers []domain.Peer) error {
	s.devices[cfg.Interface] = &DeviceState{
		Name:         cfg.Interface,
		ListenPort:   cfg.ListenPort,
		FirewallMark: cfg.FwMark,
		Peers:        make([]PeerState, 0, len(peers)),
	}
	s.last = cfg
	s.peers = map[string]domain.Peer{}
	for _, p := range peers {
		s.peers[p.PublicKey] = p
	}
	return nil
}

// Down 删除内存中的接口。
func (s *Stub) Down(ctx context.Context, name string) error {
	delete(s.devices, name)
	return nil
}

// SyncPeers 同步内存态 Peer 集合。
func (s *Stub) SyncPeers(ctx context.Context, name string, peers []domain.Peer) error {
	if _, ok := s.devices[name]; !ok {
		return fmt.Errorf("wgdriver: interface %s not present", name)
	}
	s.peers = map[string]domain.Peer{}
	for _, p := range peers {
		s.peers[p.PublicKey] = p
	}
	return nil
}

// PeerStats 返回空运行态。
func (s *Stub) PeerStats(name string) (map[string]*domain.PeerStats, error) {
	out := make(map[string]*domain.PeerStats, len(s.peers))
	for key := range s.peers {
		out[key] = &domain.PeerStats{AllowedIPs: []string{}}
	}
	return out, nil
}

// TotalCounters 返回零值计数。
func (s *Stub) TotalCounters(name string) (int64, int64, error) { return 0, 0, nil }

// LastApplied 返回最后一次 Up 下发的配置，供测试断言。
func (s *Stub) LastApplied() *domain.ServerConfig { return s.last }
