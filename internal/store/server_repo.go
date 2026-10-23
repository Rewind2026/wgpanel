package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Rewind2026/wgpanel/internal/domain"
)

const serverColumns = `interface_name, listen_port, private_key, public_key, preshared_key,
	address_cidr, listen_ipv4, listen_ipv6, mtu, fwmark, table_name, allowed_ips, dns,
	replace_peers, save_config, enable_ipv6, persistent_keepalive,
	revision, created_at, updated_at`

// ServerRepo 负责 server_config 与其版本历史。
type ServerRepo struct{ s *Store }

// ServerRepo 返回服务端配置仓储。
func (s *Store) ServerRepo() *ServerRepo { return &ServerRepo{s: s} }

// Get 读取单例服务端配置。
func (r *ServerRepo) Get(ctx context.Context) (*domain.ServerConfig, error) {
	row := r.s.ro.QueryRowContext(ctx, `SELECT `+serverColumns+` FROM server_config WHERE id = 1`)
	cfg, err := scanServer(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.ErrNotFound("server config not initialized")
	}
	if err != nil {
		return nil, err
	}
	return cfg, nil
}

// Save 写入服务端配置（upsert），并记录一条版本快照。
func (r *ServerRepo) Save(ctx context.Context, cfg *domain.ServerConfig, operatorID *int64) error {
	return r.s.WithTx(ctx, func(tx *sql.Tx) error {
		now := nowUnix()
		if cfg.CreatedAt.IsZero() {
			cfg.CreatedAt = unixTime(now)
		}
		cfg.UpdatedAt = unixTime(now)
		createdAtUnix, updatedAtUnix := cfg.CreatedAt.Unix(), cfg.UpdatedAt.Unix()

		var existingRev int64
		err := tx.QueryRowContext(ctx, `SELECT revision FROM server_config WHERE id = 1`).Scan(&existingRev)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			cfg.Revision = 1
			if _, err := tx.ExecContext(ctx, `INSERT INTO server_config (id, `+serverColumns+`)
				VALUES (1, ?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
				cfg.Interface, cfg.ListenPort, cfg.PrivateKey, cfg.PublicKey, cfg.PresharedKey,
				cfg.AddressCIDR, cfg.ListenIPv4, cfg.ListenIPv6, cfg.MTU, cfg.FwMark, cfg.Table,
				cfg.AllowedIPs, cfg.DNS, cfg.ReplacePeers, cfg.SaveConfig,
				cfg.EnableIPv6, cfg.PersistentKeepalive,
				cfg.Revision, createdAtUnix, updatedAtUnix); err != nil {
				return fmt.Errorf("store: insert server config: %w", err)
			}
		case err != nil:
			return fmt.Errorf("store: read server revision: %w", err)
		default:
			cfg.Revision = existingRev + 1
			if _, err := tx.ExecContext(ctx, `UPDATE server_config SET
				interface_name=?, listen_port=?, private_key=?, public_key=?, preshared_key=?,
				address_cidr=?, listen_ipv4=?, listen_ipv6=?, mtu=?, fwmark=?, table_name=?,
				allowed_ips=?, dns=?, replace_peers=?, save_config=?,
				enable_ipv6=?, persistent_keepalive=?, revision=?, updated_at=?
				WHERE id = 1`,
				cfg.Interface, cfg.ListenPort, cfg.PrivateKey, cfg.PublicKey, cfg.PresharedKey,
				cfg.AddressCIDR, cfg.ListenIPv4, cfg.ListenIPv6, cfg.MTU, cfg.FwMark, cfg.Table,
				cfg.AllowedIPs, cfg.DNS, cfg.ReplacePeers, cfg.SaveConfig,
				cfg.EnableIPv6, cfg.PersistentKeepalive,
				cfg.Revision, updatedAtUnix); err != nil {
				return fmt.Errorf("store: update server config: %w", err)
			}
		}

		snapshot, err := json.Marshal(cfg)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO server_config_revision (revision, snapshot, operator_id, created_at) VALUES (?,?,?,?)`,
			cfg.Revision, string(snapshot), operatorID, now); err != nil {
			return fmt.Errorf("store: insert revision: %w", err)
		}
		return nil
	})
}

// ListRevisions 返回最近若干个版本摘要。
func (r *ServerRepo) ListRevisions(ctx context.Context, limit int) ([]domain.ServerConfig, error) {
	rows, err := r.s.db.QueryContext(ctx,
		`SELECT snapshot FROM server_config_revision ORDER BY revision DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []domain.ServerConfig
	for rows.Next() {
		var snap string
		if err := rows.Scan(&snap); err != nil {
			return nil, err
		}
		var cfg domain.ServerConfig
		if err := json.Unmarshal([]byte(snap), &cfg); err != nil {
			continue // 跳过损坏快照，不阻断列表
		}
		out = append(out, cfg)
	}
	return out, rows.Err()
}

// GetRevision 按版本号还原配置。
func (r *ServerRepo) GetRevision(ctx context.Context, revision int64) (*domain.ServerConfig, error) {
	var snap string
	err := r.s.db.QueryRowContext(ctx,
		`SELECT snapshot FROM server_config_revision WHERE revision = ?`, revision).Scan(&snap)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.ErrNotFound("revision %d not found", revision)
	}
	if err != nil {
		return nil, err
	}
	var cfg domain.ServerConfig
	if err := json.Unmarshal([]byte(snap), &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// rowScanner 抽象 *sql.Row 与 *sql.Rows 的公共扫描能力。
type rowScanner interface {
	Scan(dest ...any) error
}

func scanServer(row rowScanner) (*domain.ServerConfig, error) {
	var c domain.ServerConfig
	var createdAt, updatedAt int64
	err := row.Scan(&c.Interface, &c.ListenPort, &c.PrivateKey, &c.PublicKey, &c.PresharedKey,
		&c.AddressCIDR, &c.ListenIPv4, &c.ListenIPv6, &c.MTU, &c.FwMark, &c.Table,
		&c.AllowedIPs, &c.DNS, &c.ReplacePeers, &c.SaveConfig,
		&c.EnableIPv6, &c.PersistentKeepalive,
		&c.Revision, &createdAt, &updatedAt)
	if err != nil {
		return nil, err
	}
	c.CreatedAt, c.UpdatedAt = unixTime(createdAt), unixTime(updatedAt)
	return &c, nil
}
