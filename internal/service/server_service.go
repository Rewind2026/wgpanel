package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"strings"

	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"

	"github.com/Rewind2026/wgpanel/internal/authn"
	"github.com/Rewind2026/wgpanel/internal/config"
	"github.com/Rewind2026/wgpanel/internal/domain"
	"github.com/Rewind2026/wgpanel/internal/store"
)

// ServerService 负责服务端参数的读写、校验与密钥管理。
type ServerService struct {
	repo      *store.ServerRepo
	cipher    *authn.Cipher
	audit     *store.AuditRepo
	baseCfg   config.Config
	reconcile func(ctx context.Context)
}

// NewServerService 构造服务端服务。
func NewServerService(repo *store.ServerRepo, c *authn.Cipher, audit *store.AuditRepo,
	baseCfg config.Config, reconcile func(ctx context.Context)) *ServerService {
	return &ServerService{repo: repo, cipher: c, audit: audit, baseCfg: baseCfg, reconcile: reconcile}
}

// EnsureInitialized 在数据库为空时写入初始配置并生成密钥。
func (s *ServerService) EnsureInitialized(ctx context.Context) error {
	if _, err := s.repo.Get(ctx); err == nil {
		return nil
	} else if de := domain.AsError(err); de.Code != domain.CodeNotFound {
		return err
	}

	key, err := wgtypes.GeneratePrivateKey()
	if err != nil {
		return fmt.Errorf("service: generate server key: %w", err)
	}
	cfg := &domain.ServerConfig{
		Interface:           s.baseCfg.Interface,
		ListenPort:          s.baseCfg.ListenPort,
		PrivateKey:          key.String(),
		PublicKey:           key.PublicKey().String(),
		AddressCIDR:         s.baseCfg.AddressCIDR,
		MTU:                 s.baseCfg.MTU,
		DNS:                 s.baseCfg.DNS,
		Table:               "auto",
		AllowedIPs:          "0.0.0.0/0",
		ReplacePeers:        true,
		SaveConfig:          true,
		PersistentKeepalive: 25,
	}
	cfg.Normalize()
	if err := s.validate(ctx, cfg); err != nil {
		return err
	}
	stored := *cfg
	if err := s.encrypt(&stored); err != nil {
		return err
	}
	return s.repo.Save(ctx, &stored, nil)
}

// SetReconcile 注入调谐回调。服务层写入后调用它，使变更立刻反映到内核。
func (s *ServerService) SetReconcile(fn func(ctx context.Context)) { s.reconcile = fn }

// Get 返回解密后的服务端配置。
func (s *ServerService) Get(ctx context.Context) (*domain.ServerConfig, error) {
	cfg, err := s.repo.Get(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.decrypt(cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

// GetRedacted 返回脱敏后的配置，供只读角色查看（不含私钥）。
func (s *ServerService) GetRedacted(ctx context.Context) (*domain.ServerConfig, error) {
	cfg, err := s.repo.Get(ctx)
	if err != nil {
		return nil, err
	}
	cfg.PrivateKey = maskSecret(cfg.PrivateKey)
	cfg.PresharedKey = maskSecret(cfg.PresharedKey)
	return cfg, nil
}

// Update 应用部分字段更新，随后触发一次调谐。
func (s *ServerService) Update(ctx context.Context, patch domain.ServerConfig, actor *authn.Principal) (*domain.ServerConfig, error) {
	current, err := s.repo.Get(ctx)
	if err != nil {
		return nil, err
	}
	next := *current

	if patch.Interface != "" {
		next.Interface = patch.Interface
	}
	if patch.ListenPort > 0 {
		next.ListenPort = patch.ListenPort
	}
	next.AddressCIDR = patch.AddressCIDR
	next.MTU = patch.MTU
	next.FwMark = patch.FwMark
	next.Table = patch.Table
	next.AllowedIPs = patch.AllowedIPs
	next.DNS = patch.DNS
	next.ListenIPv4 = patch.ListenIPv4
	next.ListenIPv6 = patch.ListenIPv6
	next.ReplacePeers = patch.ReplacePeers
	next.SaveConfig = patch.SaveConfig
	next.EnableIPv6 = patch.EnableIPv6
	next.PersistentKeepalive = patch.PersistentKeepalive

	// 私钥变更需要重新推导公钥；留空表示保持不变。
	if patch.PrivateKey != "" && patch.PrivateKey != current.PrivateKey {
		key, err := wgtypes.ParseKey(strings.TrimSpace(patch.PrivateKey))
		if err != nil {
			return nil, domain.ErrInvalid("privateKey is not a valid WireGuard key")
		}
		next.PrivateKey = key.String()
		next.PublicKey = key.PublicKey().String()
	}
	if patch.PresharedKey != "" {
		next.PresharedKey = patch.PresharedKey
	}

	next.Normalize()
	if err := s.validate(ctx, &next); err != nil {
		return nil, err
	}

	stored := next
	if err := s.encrypt(&stored); err != nil {
		return nil, err
	}
	var operatorID *int64
	if actor != nil {
		operatorID = &actor.UserID
	}
	if err := s.repo.Save(ctx, &stored, operatorID); err != nil {
		return nil, err
	}
	if s.audit != nil {
		_ = s.audit.Write(ctx, &domain.AuditEvent{
			ActorID: operatorID, ActorName: actorName(actor), Action: domain.AuditServerUpdate,
			Resource: "server", ResourceID: fmt.Sprint(stored.Revision), Result: domain.AuditSuccess,
			Detail: revisionSummary(current, &stored),
		})
	}
	if s.reconcile != nil {
		s.reconcile(ctx)
	}
	return &next, nil
}

// RotateKey 重置服务端私钥，所有客户端配置需重新下发。
func (s *ServerService) RotateKey(ctx context.Context, actor *authn.Principal) (*domain.ServerConfig, error) {
	current, err := s.repo.Get(ctx)
	if err != nil {
		return nil, err
	}
	key, err := wgtypes.GeneratePrivateKey()
	if err != nil {
		return nil, err
	}
	current.PrivateKey = key.String()
	current.PublicKey = key.PublicKey().String()

	stored := *current
	if err := s.encrypt(&stored); err != nil {
		return nil, err
	}
	var operatorID *int64
	if actor != nil {
		operatorID = &actor.UserID
	}
	if err := s.repo.Save(ctx, &stored, operatorID); err != nil {
		return nil, err
	}
	if s.audit != nil {
		_ = s.audit.Write(ctx, &domain.AuditEvent{
			ActorID: operatorID, ActorName: actorName(actor), Action: domain.AuditServerResetKey,
			Resource: "server", Result: domain.AuditSuccess,
			Detail: "server private key rotated; all peers must re-download config",
		})
	}
	if s.reconcile != nil {
		s.reconcile(ctx)
	}
	return current, nil
}

// Revisions 返回配置历史。
func (s *ServerService) Revisions(ctx context.Context, limit int) ([]domain.ServerConfig, error) {
	return s.repo.ListRevisions(ctx, limit)
}

// AddressPrefix 返回服务端隧道网段（已脱敏长度前缀）。
func (s *ServerService) AddressPrefix(ctx context.Context) (netip.Prefix, error) {
	cfg, err := s.Get(ctx)
	if err != nil {
		return netip.Prefix{}, err
	}
	p, err := netip.ParsePrefix(cfg.AddressCIDR)
	if err != nil {
		return netip.Prefix{}, domain.ErrInvalid("stored address cidr is invalid")
	}
	return netip.PrefixFrom(p.Addr(), p.Bits()), nil
}

// validate 校验服务端配置，并确认 Peer 地址不会越界。
func (s *ServerService) validate(ctx context.Context, cfg *domain.ServerConfig) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	if _, err := netip.ParsePrefix(cfg.AddressCIDR); err != nil {
		return domain.ErrInvalid("addressCidr is not a valid CIDR")
	}
	for _, cidr := range strings.Split(cfg.AllowedIPs, ",") {
		if _, err := netip.ParsePrefix(strings.TrimSpace(cidr)); err != nil {
			return domain.ErrInvalid("allowedIps contains an invalid entry: %q", strings.TrimSpace(cidr))
		}
	}
	for _, dns := range strings.Split(cfg.DNS, ",") {
		if d := strings.TrimSpace(dns); d != "" {
			if _, err := netip.ParseAddr(d); err != nil {
				return domain.ErrInvalid("dns contains an invalid address: %q", d)
			}
		}
	}
	return nil
}

// encrypt 加密敏感字段后返回副本。
func (s *ServerService) encrypt(cfg *domain.ServerConfig) error {
	if s.cipher == nil {
		return nil
	}
	priv, err := s.cipher.Encrypt(cfg.PrivateKey)
	if err != nil {
		return err
	}
	psk, err := s.cipher.Encrypt(cfg.PresharedKey)
	if err != nil {
		return err
	}
	cfg.PrivateKey, cfg.PresharedKey = priv, psk
	return nil
}

// decrypt 就地解密敏感字段。
func (s *ServerService) decrypt(cfg *domain.ServerConfig) error {
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

// maskSecret 对敏感串做展示脱敏。
func maskSecret(v string) string {
	if v == "" {
		return ""
	}
	if len(v) <= 8 {
		return "********"
	}
	return v[:4] + "********" + v[len(v)-4:]
}

// actorName 安全读取主体名。
func actorName(p *authn.Principal) string {
	if p == nil {
		return "system"
	}
	return p.Username
}

// revisionSummary 生成配置变更摘要，用于审计 detail。
func revisionSummary(old, next *domain.ServerConfig) string {
	changes := map[string][2]string{}
	compare := func(field string, o, n string) {
		if o != n {
			changes[field] = [2]string{o, n}
		}
	}
	compare("interface", old.Interface, next.Interface)
	compare("listenPort", fmt.Sprint(old.ListenPort), fmt.Sprint(next.ListenPort))
	compare("addressCidr", old.AddressCIDR, next.AddressCIDR)
	compare("mtu", fmt.Sprint(old.MTU), fmt.Sprint(next.MTU))
	compare("dns", old.DNS, next.DNS)
	compare("table", old.Table, next.Table)
	compare("allowedIps", old.AllowedIPs, next.AllowedIPs)
	compare("fwmark", fmt.Sprint(old.FwMark), fmt.Sprint(next.FwMark))
	compare("persistentKeepalive", fmt.Sprint(old.PersistentKeepalive), fmt.Sprint(next.PersistentKeepalive))
	if len(changes) == 0 {
		return "no field changed"
	}
	keys := make([]string, 0, len(changes))
	for k := range changes {
		keys = append(keys, k)
	}
	pairs := make([]string, 0, len(keys))
	for _, k := range keys {
		v := changes[k]
		pairs = append(pairs, fmt.Sprintf("%s:%q->%q", k, v[0], v[1]))
	}
	data, _ := json.Marshal(pairs)
	return string(data)
}
