package service

import (
	"context"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"

	"github.com/Rewind2026/wgpanel/internal/authn"
	"github.com/Rewind2026/wgpanel/internal/domain"
	"github.com/Rewind2026/wgpanel/internal/ipam"
	"github.com/Rewind2026/wgpanel/internal/store"
	"github.com/Rewind2026/wgpanel/internal/wgdriver"
)

// PeerService 负责 Peer 生命周期：校验、密钥生成、IP 分配、持久化与导出。
type PeerService struct {
	peers     *store.PeerRepo
	servers   *store.ServerRepo
	cipher    *authn.Cipher
	audit     *store.AuditRepo
	driver    wgdriver.Driver
	reconcile func(ctx context.Context)
	events    EventPublisher
}

// EventPublisher 发布领域事件。抽成接口而不是直接依赖 eventbus，
// 是为了让 service 层的测试不必拉起整个总线。
type EventPublisher interface {
	Publish(ctx context.Context, ev domain.Event)
}

// NewPeerService 构造 Peer 服务。
func NewPeerService(peers *store.PeerRepo, servers *store.ServerRepo, c *authn.Cipher,
	audit *store.AuditRepo, driver wgdriver.Driver, reconcile func(ctx context.Context)) *PeerService {
	return &PeerService{peers: peers, servers: servers, cipher: c, audit: audit,
		driver: driver, reconcile: reconcile}
}

// SetReconcile 注入调谐回调。Peer 变更后调用它，使内核状态立即一致。
func (s *PeerService) SetReconcile(fn func(ctx context.Context)) { s.reconcile = fn }

// SetPublisher 注入事件总线。
//
// Peer 的增删改是运维最需要被感知的一类变更，SSE 实时看板从这里取事件，
// 所以在这里发一次就够。
func (s *PeerService) SetPublisher(p EventPublisher) { s.events = p }

// publishPeerEvent 广播一条 Peer 相关事件，附带 PeerID 与名称，
// 让订阅方不必回查数据库就能说明「是谁出的事」。
func (s *PeerService) publishPeerEvent(ctx context.Context, t domain.EventType, p *domain.Peer, msg string) {
	if s.events == nil {
		return
	}
	ev := domain.Event{Type: t, Message: msg, OccurredAt: time.Now().UTC()}
	if p != nil {
		ev.PeerID, ev.PeerName = p.ID, p.Name
	}
	s.events.Publish(ctx, ev)
}

// List 返回全部 Peer（含运行态，驱动不可用时仅返回持久态）。
//
// 私钥与预共享密钥在这里被清空，而不是原样透传：
// 仓储层返回的是密文，对调用方毫无用处，却把加密后的凭据发到了浏览器、
// 写进了前端内存缓存，还可能被浏览器插件或抓包工具留存。
// 明文私钥只在两个地方出现——创建响应（一次性交付）与
// GET /peers/{id}/config（需要 config:export 权限），这是刻意的收敛。
func (s *PeerService) List(ctx context.Context) ([]domain.Peer, error) {
	peers, err := s.peers.List(ctx)
	if err != nil {
		return nil, err
	}
	stats, ok := s.liveStats(ctx, peers)
	for i := range peers {
		peers[i].PrivateKey = ""
		peers[i].PresharedKey = ""
		if !ok {
			continue
		}
		// 驱动只按公钥返回运行态，认不出数据库里的 peer 主键。
		// 这里补上 peerId 与 publicKey：前端把列表接口的 stats 与
		// SSE 帧按 peerId 对齐（MonitorPage 的 mergeStats），
		// peerId 为 0 时列表数据会与 SSE 数据对不上。
		if st, hit := stats[peers[i].PublicKey]; hit {
			st.PeerID = peers[i].ID
			st.PublicKey = peers[i].PublicKey
			peers[i].Stats = st
		} else {
			peers[i].Stats = &domain.PeerStats{
				PeerID:     peers[i].ID,
				PublicKey:  peers[i].PublicKey,
				AllowedIPs: []string{},
			}
		}
	}
	return peers, nil
}

// Get 返回单个 Peer。
func (s *PeerService) Get(ctx context.Context, id int64) (*domain.Peer, error) {
	p, err := s.peers.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := s.decrypt(p); err != nil {
		return nil, err
	}
	return p, nil
}

// CreateInput 描述一次创建 Peer 的请求。
type CreateInput struct {
	Name string
	// Address 留空时自动从服务端网段分配。
	Address             string
	AllowedIPs          string
	Endpoint            string
	PresharedKey        string
	PersistentKeepalive int
	Note                string
	Tags                []string
	TrafficQuotaBytes   int64
	QuotaPeriod         domain.QuotaPeriod
	RateLimitMbps       int
	ExpiresAt           *time.Time
	Enabled             *bool
}

// Create 创建 Peer 并立即生效。
func (s *PeerService) Create(ctx context.Context, in CreateInput, actor *authn.Principal) (*domain.Peer, error) {
	server, err := s.servers.Get(ctx)
	if err != nil {
		return nil, err
	}

	priv, err := wgtypes.GeneratePrivateKey()
	if err != nil {
		return nil, fmt.Errorf("service: generate peer key: %w", err)
	}

	address := in.Address
	if address == "" {
		address, err = s.allocateAddress(ctx, server.AddressCIDR, 0)
		if err != nil {
			return nil, err
		}
	} else {
		// 必须把归一化结果写回，而不是只拿它做校验。
		// 存原始输入会让 "10.8.0.212" 这种不带掩码的写法落进数据库，
		// 而下游（wgctrl 的 AllowedIPs 解析、限速的地址匹配）都按 CIDR 解析，
		// 于是每轮调谐都在这个 Peer 上失败：
		// `invalid cidr "10.8.0.212"`。故障范围不止这一个 Peer——
		// 调谐是全量替换，一条 Peer 解析失败就挡住了所有 Peer 的下发，
		// 新建的限速、地址变更全部不生效，面板看起来只是"新增后没反应"。
		address, err = normalizePeerAddress(address)
		if err != nil {
			return nil, err
		}
		if !ipam.InPrefix(address, server.AddressCIDR) {
			return nil, domain.ErrInvalid("address %s is outside server subnet %s", address, server.AddressCIDR)
		}
		if address == ipam.HostAddress(server.AddressCIDR) {
			return nil, domain.ErrInvalid("address %s conflicts with the server address", address)
		}
		taken, err := s.peers.AddressExists(ctx, address, 0)
		if err != nil {
			return nil, err
		}
		if taken {
			return nil, domain.ErrConflict("address %s is already allocated", address)
		}
	}

	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}

	p := &domain.Peer{
		Name:                in.Name,
		PublicKey:           priv.PublicKey().String(),
		PrivateKey:          priv.String(),
		PresharedKey:        in.PresharedKey,
		Address:             address,
		AllowedIPs:          in.AllowedIPs,
		Endpoint:            in.Endpoint,
		PersistentKeepalive: in.PersistentKeepalive,
		Note:                in.Note,
		Tags:                in.Tags,
		TrafficQuotaBytes:   in.TrafficQuotaBytes,
		QuotaPeriod:         in.QuotaPeriod,
		RateLimitMbps:       in.RateLimitMbps,
		Enabled:             enabled,
		ExpiresAt:           in.ExpiresAt,
	}
	// 不接 MaxConnections：见 domain.Peer.MaxConnections 的说明，
	// 该字段在 WireGuard 协议层没有对应实现，接进来只会让用户以为它生效。
	p.Normalize()
	if err := p.Validate(); err != nil {
		return nil, err
	}
	if err := s.validateAllowedIPs(p.AllowedIPs); err != nil {
		return nil, err
	}

	stored := *p
	if err := s.encrypt(&stored); err != nil {
		return nil, err
	}
	if _, err := s.peers.Create(ctx, &stored); err != nil {
		return nil, err
	}
	p.ID, p.CreatedAt, p.UpdatedAt = stored.ID, stored.CreatedAt, stored.UpdatedAt
	if s.audit != nil {
		_ = s.audit.Write(ctx, &domain.AuditEvent{
			ActorID: ptrID(actor), ActorName: actorName(actor), Action: domain.AuditPeerCreate,
			Resource: "peer", ResourceID: strconv.FormatInt(stored.ID, 10),
			Result: domain.AuditSuccess, Detail: fmt.Sprintf("name=%s address=%s", stored.Name, stored.Address),
		})
	}
	s.publishPeerEvent(ctx, domain.EventPeerCreated, p,
		fmt.Sprintf("已创建 Peer %s（%s）", p.Name, p.Address))
	s.applyNow(ctx)
	return p, nil
}

// UpdateInput 描述一次更新请求。nil 字段表示不修改。
type UpdateInput struct {
	Name                *string
	Address             *string
	AllowedIPs          *string
	Endpoint            *string
	PresharedKey        *string
	PersistentKeepalive *int
	Note                *string
	Tags                *[]string
	TrafficQuotaBytes   *int64
	QuotaPeriod         *domain.QuotaPeriod
	RateLimitMbps       *int
	Enabled             *bool
	ExpiresAt           *time.Time
	RotateKey           bool
}

// Update 更新 Peer 并立即生效。
func (s *PeerService) Update(ctx context.Context, id int64, in UpdateInput, actor *authn.Principal) (*domain.Peer, error) {
	return s.update(ctx, id, in, actor, nil)
}

// update 是 Update 的实现，batch 非 nil 时把逐条事件交给调用方汇总。
func (s *PeerService) update(ctx context.Context, id int64, in UpdateInput,
	actor *authn.Principal, batch *eventBatch) (*domain.Peer, error) {
	current, err := s.peers.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := s.decrypt(current); err != nil {
		return nil, err
	}
	server, err := s.servers.Get(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.decryptServer(server); err != nil {
		return nil, err
	}

	next := *current
	if in.Name != nil {
		next.Name = *in.Name
	}
	if in.Address != nil && *in.Address != current.Address {
		normalized, err := normalizePeerAddress(*in.Address)
		if err != nil {
			return nil, err
		}
		if !ipam.InPrefix(normalized, server.AddressCIDR) {
			return nil, domain.ErrInvalid("address %s is outside server subnet %s", normalized, server.AddressCIDR)
		}
		if normalized == ipam.HostAddress(server.AddressCIDR) {
			return nil, domain.ErrInvalid("address %s conflicts with the server address", normalized)
		}
		taken, err := s.peers.AddressExists(ctx, normalized, id)
		if err != nil {
			return nil, err
		}
		if taken {
			return nil, domain.ErrConflict("address %s is already allocated", normalized)
		}
		next.Address = normalized
	}
	if in.AllowedIPs != nil {
		next.AllowedIPs = *in.AllowedIPs
	}
	if in.Endpoint != nil {
		next.Endpoint = *in.Endpoint
	}
	if in.PresharedKey != nil {
		next.PresharedKey = *in.PresharedKey
	}
	if in.PersistentKeepalive != nil {
		next.PersistentKeepalive = *in.PersistentKeepalive
	}
	if in.Note != nil {
		next.Note = *in.Note
	}
	if in.Tags != nil {
		next.Tags = *in.Tags
	}
	if in.TrafficQuotaBytes != nil {
		next.TrafficQuotaBytes = *in.TrafficQuotaBytes
	}
	if in.QuotaPeriod != nil {
		next.QuotaPeriod = *in.QuotaPeriod
	}
	if in.RateLimitMbps != nil {
		next.RateLimitMbps = *in.RateLimitMbps
	}
	if in.Enabled != nil {
		next.Enabled = *in.Enabled
	}
	if in.ExpiresAt != nil {
		next.ExpiresAt = in.ExpiresAt
	}
	if in.RotateKey {
		key, err := wgtypes.GeneratePrivateKey()
		if err != nil {
			return nil, fmt.Errorf("service: rotate peer key: %w", err)
		}
		next.PrivateKey = key.String()
		next.PublicKey = key.PublicKey().String()
	}

	next.Normalize()
	if err := next.Validate(); err != nil {
		return nil, err
	}
	if err := s.validateAllowedIPs(next.AllowedIPs); err != nil {
		return nil, err
	}

	stored := next
	if err := s.encrypt(&stored); err != nil {
		return nil, err
	}
	if err := s.peers.Update(ctx, &stored); err != nil {
		return nil, err
	}

	action := domain.AuditPeerUpdate
	if in.RotateKey {
		action = domain.AuditPeerRotateKey
	}
	if s.audit != nil {
		_ = s.audit.Write(ctx, &domain.AuditEvent{
			ActorID: ptrID(actor), ActorName: actorName(actor), Action: action,
			Resource: "peer", ResourceID: strconv.FormatInt(id, 10), Result: domain.AuditSuccess,
		})
	}
	// 轮换密钥必须单独成事件：客户端需要立刻重新下发配置，
	// 和「改个备注」混在同一个事件里，收通知的人分不出轻重。
	// batch 非 nil 表示处于批量模式，逐条事件由调用方汇总。
	if batch == nil {
		if in.RotateKey {
			s.publishPeerEvent(ctx, domain.EventPeerRotated, &next,
				fmt.Sprintf("已轮换 Peer %s 的密钥，客户端需重新下发配置", next.Name))
		} else {
			s.publishPeerEvent(ctx, domain.EventPeerUpdated, &next,
				fmt.Sprintf("已更新 Peer %s", next.Name))
		}
	}
	s.applyNow(ctx)
	return &next, nil
}

// Delete 删除 Peer 并立即生效。
func (s *PeerService) Delete(ctx context.Context, id int64, actor *authn.Principal) error {
	p, err := s.peers.Get(ctx, id)
	if err != nil {
		return err
	}
	if err := s.peers.Delete(ctx, id); err != nil {
		return err
	}
	if s.audit != nil {
		_ = s.audit.Write(ctx, &domain.AuditEvent{
			ActorID: ptrID(actor), ActorName: actorName(actor), Action: domain.AuditPeerDelete,
			Resource: "peer", ResourceID: strconv.FormatInt(id, 10), Result: domain.AuditSuccess,
			Detail: fmt.Sprintf("name=%s address=%s", p.Name, p.Address),
		})
	}
	s.publishPeerEvent(ctx, domain.EventPeerDeleted, p,
		fmt.Sprintf("已删除 Peer %s（%s）", p.Name, p.Address))
	s.applyNow(ctx)
	return nil
}

// RotateKey 轮换 Peer 密钥对（密钥泄露应急）。
func (s *PeerService) RotateKey(ctx context.Context, id int64, actor *authn.Principal) (*domain.Peer, error) {
	yes := true
	return s.Update(ctx, id, UpdateInput{RotateKey: yes}, actor)
}

// BulkRevokeAll 吊销全部 Peer 的密钥，用于密钥泄露应急。
//
// 全量吊销必须与「吊销用户勾选的那几个」走同一条 rotateKey 路径：
// 另写一套批量逻辑就等于两处轮换语义可能漂移，而漂移的后果是
// 某些 Peer 的私钥没换、某些事件没发、某些审计没写，且都静默。
func (s *PeerService) BulkRevokeAll(ctx context.Context, actor *authn.Principal) (int, error) {
	peers, err := s.peers.List(ctx)
	if err != nil {
		return 0, err
	}
	ids := make([]int64, 0, len(peers))
	for _, p := range peers {
		ids = append(ids, p.ID)
	}
	return s.BulkRevoke(ctx, ids, actor)
}

// BulkRevoke 批量轮换密钥，用于全量吊销。
func (s *PeerService) BulkRevoke(ctx context.Context, ids []int64, actor *authn.Principal) (int, error) {
	// 批量操作要发一条汇总事件，而不是每个 Peer 一条：
	// 吊销 50 台设备就推 50 条通知，任何通知渠道都会被淹掉，
	// 真正需要的那条汇总反而被埋掉。
	batch := s.newBatch()
	revoked := 0
	for _, id := range ids {
		if _, err := s.rotateKey(ctx, id, actor, batch); err != nil {
			return revoked, err
		}
		revoked++
	}
	batch.flush(ctx, domain.EventPeerRotated,
		fmt.Sprintf("已批量轮换 %d 个 Peer 的密钥，客户端需重新下发配置", revoked))
	if s.audit != nil {
		_ = s.audit.Write(ctx, &domain.AuditEvent{
			ActorID: ptrID(actor), ActorName: actorName(actor), Action: domain.AuditPeerBulkRevoke,
			Resource: "peer", Result: domain.AuditSuccess,
			Detail: fmt.Sprintf("revoked=%d", revoked),
		})
	}
	return revoked, nil
}

// eventBatch 暂存批量操作的事件，flush 时合成一条。
type eventBatch struct {
	svc    *PeerService
	names  []string
	active bool
}

// newBatch 返回一个可关闭的事件批次；传 nil 表示不做汇总（单条操作）。
func (s *PeerService) newBatch() *eventBatch { return &eventBatch{svc: s, active: true} }

// flush 输出汇总事件；未处于批量模式时丢弃（逐条事件已在各自操作里发过）。
func (b *eventBatch) flush(ctx context.Context, t domain.EventType, msg string) {
	if b == nil || !b.active || b.svc.events == nil {
		return
	}
	b.svc.events.Publish(ctx, domain.Event{
		Type: t, Message: msg, OccurredAt: time.Now().UTC(),
		Data: map[string]any{"peerNames": b.names},
	})
}

// rotateKey 轮换单个 Peer 的密钥。batch 非 nil 时不逐条发事件。
func (s *PeerService) rotateKey(ctx context.Context, id int64, actor *authn.Principal, batch *eventBatch) (*domain.Peer, error) {
	p, err := s.update(ctx, id, UpdateInput{RotateKey: true}, actor, batch)
	if err != nil {
		return nil, err
	}
	if batch != nil && p != nil {
		batch.names = append(batch.names, p.Name)
	}
	return p, nil
}

// ClientConfig 返回单个 Peer 的客户端配置与二维码内容。
func (s *PeerService) ClientConfig(ctx context.Context, id int64, endpoint string) (string, error) {
	server, err := s.servers.Get(ctx)
	if err != nil {
		return "", err
	}
	if err := s.decryptServer(server); err != nil {
		return "", err
	}
	p, err := s.Get(ctx, id)
	if err != nil {
		return "", err
	}
	return wgdriver.RenderPeerConfig(server, p, endpoint), nil
}

// ServerConfigText 返回服务端 wg-quick 配置全文。
func (s *PeerService) ServerConfigText(ctx context.Context) (string, error) {
	server, err := s.servers.Get(ctx)
	if err != nil {
		return "", err
	}
	if err := s.decryptServer(server); err != nil {
		return "", err
	}
	peers, err := s.peers.ListActive(ctx, time.Now().Unix())
	if err != nil {
		return "", err
	}
	decrypted := make([]domain.Peer, 0, len(peers))
	for i := range peers {
		p := peers[i]
		if err := s.decrypt(&p); err != nil {
			return "", err
		}
		decrypted = append(decrypted, p)
	}
	return wgdriver.RenderServerConfig(server, decrypted), nil
}

// ActivePeers 返回下发内核所需的启用且未过期的 Peer。
func (s *PeerService) ActivePeers(ctx context.Context) ([]domain.Peer, error) {
	peers, err := s.peers.ListActive(ctx, time.Now().Unix())
	if err != nil {
		return nil, err
	}
	for i := range peers {
		if err := s.decrypt(&peers[i]); err != nil {
			return nil, err
		}
	}
	return peers, nil
}

// liveStats 读取驱动侧运行态；驱动不可用时返回 ok=false。
func (s *PeerService) liveStats(ctx context.Context, peers []domain.Peer) (map[string]*domain.PeerStats, bool) {
	if s.driver == nil || len(peers) == 0 {
		return nil, s.driver != nil
	}
	cfg, err := s.servers.Get(ctx)
	if err != nil {
		return nil, false
	}
	stats, err := s.driver.PeerStats(cfg.Interface)
	if err != nil {
		return nil, false
	}
	return stats, true
}

// applyNow 触发一次调谐，使变更对用户立即可见。
func (s *PeerService) applyNow(ctx context.Context) {
	if s.reconcile != nil {
		s.reconcile(ctx)
	}
}

// allocateAddress 在服务端网段内分配下一个空闲地址。
func (s *PeerService) allocateAddress(ctx context.Context, serverCIDR string, excludeID int64) (string, error) {
	prefix, err := netip.ParsePrefix(serverCIDR)
	if err != nil {
		return "", domain.ErrInvalid("server subnet is invalid")
	}
	taken, err := s.peers.UsedAddresses(ctx)
	if err != nil {
		return "", err
	}
	used := make(map[string]struct{}, len(taken)+1)
	for addr := range taken {
		used[ipam.HostAddress(addr)] = struct{}{}
	}
	used[ipam.HostAddress(serverCIDR)] = struct{}{}

	alloc, err := ipam.NewAllocator(netip.PrefixFrom(prefix.Addr(), prefix.Bits()), used)
	if err != nil {
		return "", domain.ErrInvalid("%s", err.Error())
	}
	return alloc.Next()
}

// validateAllowedIPs 校验 AllowedIPs 列表。
func (s *PeerService) validateAllowedIPs(list string) error {
	if strings.TrimSpace(list) == "" {
		return domain.ErrInvalid("allowedIps must not be empty")
	}
	for _, item := range strings.Split(list, ",") {
		entry := strings.TrimSpace(item)
		if entry == "" {
			continue
		}
		if _, err := netip.ParsePrefix(entry); err != nil {
			return domain.ErrInvalid("allowedIps contains an invalid CIDR: %q", entry)
		}
	}
	return nil
}

// normalizePeerAddress 把裸 IP 或 CIDR 统一为 host/32。
func normalizePeerAddress(addr string) (string, error) {
	entry := strings.TrimSpace(addr)
	if entry == "" {
		return "", domain.ErrInvalid("address must not be empty")
	}
	if p, err := netip.ParsePrefix(entry); err == nil {
		if !p.Addr().Is4() {
			return "", domain.ErrInvalid("only IPv4 addresses are supported in phase 1")
		}
		if p.Bits() != 32 {
			return "", domain.ErrInvalid("peer address must be a single host, e.g. 10.8.0.2/32")
		}
		return p.Addr().String() + "/32", nil
	}
	a, err := netip.ParseAddr(entry)
	if err != nil || !a.Is4() {
		return "", domain.ErrInvalid("address %q is not a valid IPv4 address", entry)
	}
	return a.String() + "/32", nil
}

// encrypt 加密 Peer 敏感字段后返回副本。
func (s *PeerService) encrypt(p *domain.Peer) error {
	if s.cipher == nil {
		return nil
	}
	priv, err := s.cipher.Encrypt(p.PrivateKey)
	if err != nil {
		return err
	}
	psk, err := s.cipher.Encrypt(p.PresharedKey)
	if err != nil {
		return err
	}
	p.PrivateKey, p.PresharedKey = priv, psk
	return nil
}

// decrypt 就地解密 Peer 敏感字段。
func (s *PeerService) decrypt(p *domain.Peer) error {
	if s.cipher == nil {
		return nil
	}
	priv, err := s.cipher.Decrypt(p.PrivateKey)
	if err != nil {
		return fmt.Errorf("service: decrypt peer private key: %w", err)
	}
	psk, err := s.cipher.Decrypt(p.PresharedKey)
	if err != nil {
		return fmt.Errorf("service: decrypt peer preshared key: %w", err)
	}
	p.PrivateKey, p.PresharedKey = priv, psk
	return nil
}

// decryptServer 就地解密服务端敏感字段。
func (s *PeerService) decryptServer(cfg *domain.ServerConfig) error {
	if s.cipher == nil {
		return nil
	}
	priv, err := s.cipher.Decrypt(cfg.PrivateKey)
	if err != nil {
		return fmt.Errorf("service: decrypt server private key: %w", err)
	}
	psk, err := s.cipher.Decrypt(cfg.PresharedKey)
	if err != nil {
		return fmt.Errorf("service: decrypt server preshared key: %w", err)
	}
	cfg.PrivateKey, cfg.PresharedKey = priv, psk
	return nil
}

// ptrID 安全地取引用。
func ptrID(p *authn.Principal) *int64 {
	if p == nil {
		return nil
	}
	return &p.UserID
}
