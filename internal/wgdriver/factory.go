//go:build !linux

package wgdriver

import (
	"context"
	"errors"

	"github.com/Rewind2026/wgpanel/internal/domain"
	"github.com/Rewind2026/wgpanel/internal/netdriver"
)

// errUnsupported 在非 Linux 平台统一返回。
var errUnsupported = errors.New("wgdriver: wireguard control plane is only supported on linux")

// New 在非 Linux 平台返回不支持错误。
func New(net netdriver.NetDriver) (*LinuxDriver, error) { return nil, errUnsupported }

// LinuxDriver 是占位类型，保证非 Linux 平台可编译。
type LinuxDriver struct{}

// Available 报告平台不支持。
func (d *LinuxDriver) Available() error { return errUnsupported }

// Close 无操作。
func (d *LinuxDriver) Close() {}

// Device 报告平台不支持。
func (d *LinuxDriver) Device(ctx context.Context, name string) (*DeviceState, error) {
	return nil, errUnsupported
}

// Up 报告平台不支持。
func (d *LinuxDriver) Up(ctx context.Context, cfg *domain.ServerConfig, peers []domain.Peer) error {
	return errUnsupported
}

// Down 报告平台不支持。
func (d *LinuxDriver) Down(ctx context.Context, name string) error { return errUnsupported }

// SyncPeers 报告平台不支持。
func (d *LinuxDriver) SyncPeers(ctx context.Context, name string, peers []domain.Peer) error {
	return errUnsupported
}

// PeerStats 报告平台不支持。
func (d *LinuxDriver) PeerStats(name string) (map[string]*domain.PeerStats, error) {
	return nil, errUnsupported
}

// TotalCounters 报告平台不支持。
func (d *LinuxDriver) TotalCounters(name string) (int64, int64, error) {
	return 0, 0, errUnsupported
}
