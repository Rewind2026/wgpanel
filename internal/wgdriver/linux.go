//go:build linux

package wgdriver

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"golang.zx2c4.com/wireguard/wgctrl"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"

	"github.com/Rewind2026/wgpanel/internal/domain"
	"github.com/Rewind2026/wgpanel/internal/netdriver"
)

// handshakeTimeout 判定 Peer 在线的时间窗口。
const handshakeTimeout = 3 * time.Minute

// LinuxDriver 通过 netlink 操作内核 WireGuard 接口。
type LinuxDriver struct {
	client *wgctrl.Client
	net    netdriver.NetDriver
	mu     sync.Mutex
}

// New 返回 Linux 平台驱动。net 负责链路、地址与 MTU 的落地。
func New(net netdriver.NetDriver) (*LinuxDriver, error) {
	c, err := wgctrl.New()
	if err != nil {
		return nil, fmt.Errorf("wgdriver: open client: %w", err)
	}
	if net == nil {
		net = netdriver.Platform()
	}
	return &LinuxDriver{client: c, net: net}, nil
}

// Close 释放 netlink 资源。
func (d *LinuxDriver) Close() {
	if d.client != nil {
		_ = d.client.Close()
	}
}

// Available 探测内核模块与权限是否就绪。
func (d *LinuxDriver) Available() error {
	caps := d.net.Probe()
	if !caps.Usable() {
		// 这里只报 netlink 一项：它才是本函数判定失败的唯一原因，
		// 把 iptables/tc 之类的缺口混进来会让人误以为加了 iptables 就能修好。
		return fmt.Errorf("wgdriver: netlink access denied (需要 CAP_NET_ADMIN): %s",
			strings.Join(caps.Problems(netdriver.ModeManaged), "; "))
	}
	if _, err := d.client.Devices(); err != nil {
		return fmt.Errorf("wgdriver: wireguard control plane unavailable（宿主需已加载 wireguard 内核模块）: %w", err)
	}
	return nil
}

// Device 读取接口状态；接口不存在时返回 (nil, nil)。
func (d *LinuxDriver) Device(ctx context.Context, name string) (*DeviceState, error) {
	dev, err := d.client.Device(name)
	if err != nil {
		if isNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("wgdriver: read device %s: %w", name, err)
	}
	return fromDevice(dev), nil
}

// Up 创建/更新接口并下发全量配置。
// 顺序不可颠倒：先让链路存在并完成地址/MTU 绑定，再配置 WireGuard 控制面，
// 否则 ConfigureDevice 会因接口不存在而失败。
func (d *LinuxDriver) Up(ctx context.Context, cfg *domain.ServerConfig, peers []domain.Peer) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	priv, err := parseKey(cfg.PrivateKey)
	if err != nil {
		return fmt.Errorf("wgdriver: server private key invalid: %w", err)
	}

	if err := d.net.EnsureLink(ctx, cfg.Interface, cfg.MTU); err != nil {
		return err
	}
	if err := d.net.EnsureAddress(ctx, cfg.Interface, cfg.AddressCIDR); err != nil {
		return err
	}

	port := cfg.ListenPort
	mark := cfg.FwMark
	wgcfg := wgtypes.Config{
		PrivateKey:   &priv,
		ListenPort:   &port,
		FirewallMark: &mark,
		ReplacePeers: cfg.ReplacePeers,
	}

	peerCfgs, err := toPeerConfigs(peers)
	if err != nil {
		return err
	}
	wgcfg.Peers = peerCfgs

	if err := d.client.ConfigureDevice(cfg.Interface, wgcfg); err != nil {
		return fmt.Errorf("wgdriver: configure device %s: %w", cfg.Interface, err)
	}
	if err := d.net.SetLinkUp(ctx, cfg.Interface); err != nil {
		return err
	}
	return nil
}

// Down 删除接口，接口不存在视为成功。
func (d *LinuxDriver) Down(ctx context.Context, name string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.net.DeleteLink(ctx, name)
}

// SyncPeers 同步 Peer 集合。wgctrl 的配置入口是整表 ReplacePeers，
// 因此这里的「同步」语义由内核侧一次性替换实现：缺失的 Peer 随之消失。
func (d *LinuxDriver) SyncPeers(ctx context.Context, name string, peers []domain.Peer) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if _, err := d.client.Device(name); err != nil {
		if isNotExist(err) {
			return fmt.Errorf("wgdriver: interface %s not present", name)
		}
		return fmt.Errorf("wgdriver: read device %s: %w", name, err)
	}

	upsert, err := toPeerConfigs(peers)
	if err != nil {
		return err
	}
	wgcfg := wgtypes.Config{
		ReplacePeers: true,
		Peers:        upsert,
	}
	if err := d.client.ConfigureDevice(name, wgcfg); err != nil {
		return fmt.Errorf("wgdriver: sync peers on %s: %w", name, err)
	}
	return nil
}

// PeerStats 读取每个 Peer 的流量与握手信息，key 为公钥字符串。
func (d *LinuxDriver) PeerStats(name string) (map[string]*domain.PeerStats, error) {
	dev, err := d.client.Device(name)
	if err != nil {
		if isNotExist(err) {
			return map[string]*domain.PeerStats{}, nil
		}
		return nil, fmt.Errorf("wgdriver: read device %s: %w", name, err)
	}
	out := make(map[string]*domain.PeerStats, len(dev.Peers))
	for _, p := range dev.Peers {
		out[p.PublicKey.String()] = peerStatsFrom(p)
	}
	return out, nil
}

// TotalCounters 返回接口级别的累计收发字节。
func (d *LinuxDriver) TotalCounters(name string) (rx, tx int64, err error) {
	dev, err := d.client.Device(name)
	if err != nil {
		if isNotExist(err) {
			return 0, 0, nil
		}
		return 0, 0, fmt.Errorf("wgdriver: read device %s: %w", name, err)
	}
	for _, p := range dev.Peers {
		rx += p.ReceiveBytes
		tx += p.TransmitBytes
	}
	return rx, tx, nil
}

func peerStatsFrom(p wgtypes.Peer) *domain.PeerStats {
	s := &domain.PeerStats{
		RxBytes:    p.ReceiveBytes,
		TxBytes:    p.TransmitBytes,
		AllowedIPs: []string{},
	}
	if p.LastHandshakeTime.IsZero() || time.Since(p.LastHandshakeTime) >= handshakeTimeout {
		return s
	}
	s.Online = true
	s.LastHandshake = p.LastHandshakeTime
	s.ConnectedSince = p.LastHandshakeTime
	s.ConnectedSeconds = int64(time.Since(p.LastHandshakeTime).Seconds())
	for _, ip := range p.AllowedIPs {
		if ip.IP.To4() != nil {
			s.AllowedIPs = append(s.AllowedIPs, ip.IP.String())
			break
		}
	}
	return s
}

func fromDevice(dev *wgtypes.Device) *DeviceState {
	st := &DeviceState{
		Name:         dev.Name,
		ListenPort:   dev.ListenPort,
		PublicKey:    dev.PublicKey,
		FirewallMark: dev.FirewallMark,
		Peers:        make([]PeerState, 0, len(dev.Peers)),
	}
	for _, p := range dev.Peers {
		st.Peers = append(st.Peers, PeerState{
			PublicKey:                   p.PublicKey,
			AllowedIPs:                  p.AllowedIPs,
			Endpoint:                    p.Endpoint,
			PersistentKeepaliveInterval: p.PersistentKeepaliveInterval,
			RxBytes:                     p.ReceiveBytes,
			TxBytes:                     p.TransmitBytes,
			LastHandshake:               p.LastHandshakeTime,
		})
	}
	return st
}

// isNotExist 识别「接口不存在」类错误。
func isNotExist(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "does not exist") ||
		strings.Contains(msg, "no such device") ||
		strings.Contains(msg, "link not found")
}
