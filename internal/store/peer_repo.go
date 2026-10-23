package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Rewind2026/wgpanel/internal/domain"
)

const peerColumns = `id, name, public_key, private_key, preshared_key, address, allowed_ips, endpoint,
	persistent_keepalive, note, tags, traffic_quota_bytes, quota_period, rate_limit_mbps,
	max_connections, enabled, expires_at, quota_used_bytes, suspended_by, last_seen_at, created_at, updated_at`

// insertPeerColumns 与 peerColumns 保持相同顺序，供批量插入复用。
const insertPeerColumns = `name, public_key, private_key, preshared_key, address, allowed_ips, endpoint,
	persistent_keepalive, note, tags, traffic_quota_bytes, quota_period, rate_limit_mbps,
	max_connections, enabled, expires_at, quota_used_bytes, suspended_by, created_at, updated_at`

// PeerRepo 负责 peer 表的读写。
type PeerRepo struct{ s *Store }

// PeerRepo 返回 Peer 仓储。
func (s *Store) PeerRepo() *PeerRepo { return &PeerRepo{s: s} }

// List 返回全部 Peer，按 id 升序。
func (r *PeerRepo) List(ctx context.Context) ([]domain.Peer, error) {
	rows, err := r.s.ro.QueryContext(ctx, `SELECT `+peerColumns+` FROM peer ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("store: list peers: %w", err)
	}
	defer rows.Close()

	out := make([]domain.Peer, 0, 64)
	for rows.Next() {
		p, err := scanPeer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

// ListActive 返回启用且未过期的 Peer，用于下发内核。
func (r *PeerRepo) ListActive(ctx context.Context, now int64) ([]domain.Peer, error) {
	rows, err := r.s.ro.QueryContext(ctx,
		`SELECT `+peerColumns+` FROM peer WHERE enabled = 1 AND (expires_at IS NULL OR expires_at > ?) ORDER BY id`,
		now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]domain.Peer, 0, 64)
	for rows.Next() {
		p, err := scanPeer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

// Get 按 id 读取单个 Peer。
func (r *PeerRepo) Get(ctx context.Context, id int64) (*domain.Peer, error) {
	row := r.s.ro.QueryRowContext(ctx, `SELECT `+peerColumns+` FROM peer WHERE id = ?`, id)
	p, err := scanPeer(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.ErrNotFound("peer %d not found", id)
	}
	if err != nil {
		return nil, err
	}
	return p, nil
}

// Create 插入新 Peer。
func (r *PeerRepo) Create(ctx context.Context, p *domain.Peer) (int64, error) {
	tags, err := json.Marshal(p.Tags)
	if err != nil {
		return 0, err
	}
	now := nowUnix()
	res, err := r.s.db.ExecContext(ctx, `INSERT INTO peer (`+insertPeerColumns+`)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		p.Name, p.PublicKey, p.PrivateKey, p.PresharedKey, p.Address, p.AllowedIPs, p.Endpoint,
		p.PersistentKeepalive, p.Note, string(tags), p.TrafficQuotaBytes, string(p.QuotaPeriod),
		p.RateLimitMbps, p.MaxConnections, p.Enabled, nullableUnix(p.ExpiresAt),
		p.QuotaUsedBytes, string(p.SuspendedBy), now, now)
	if err != nil {
		return 0, translatePeerError(err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	p.ID, p.CreatedAt, p.UpdatedAt = id, unixTime(now), unixTime(now)
	return id, nil
}

// Update 更新 Peer 可变字段。
func (r *PeerRepo) Update(ctx context.Context, p *domain.Peer) error {
	tags, err := json.Marshal(p.Tags)
	if err != nil {
		return err
	}
	res, err := r.s.db.ExecContext(ctx, `UPDATE peer SET
		name=?, public_key=?, private_key=?, preshared_key=?, address=?, allowed_ips=?, endpoint=?,
		persistent_keepalive=?, note=?, tags=?, traffic_quota_bytes=?, quota_period=?,
		rate_limit_mbps=?, max_connections=?, enabled=?, expires_at=?, quota_used_bytes=?,
		suspended_by=?, updated_at=?
		WHERE id = ?`,
		p.Name, p.PublicKey, p.PrivateKey, p.PresharedKey, p.Address, p.AllowedIPs, p.Endpoint,
		p.PersistentKeepalive, p.Note, string(tags), p.TrafficQuotaBytes, string(p.QuotaPeriod),
		p.RateLimitMbps, p.MaxConnections, p.Enabled, nullableUnix(p.ExpiresAt), p.QuotaUsedBytes,
		string(p.SuspendedBy), nowUnix(), p.ID)
	if err != nil {
		return translatePeerError(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return domain.ErrNotFound("peer %d not found", p.ID)
	}
	return nil
}

// Delete 删除 Peer。
func (r *PeerRepo) Delete(ctx context.Context, id int64) error {
	res, err := r.s.db.ExecContext(ctx, `DELETE FROM peer WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return domain.ErrNotFound("peer %d not found", id)
	}
	return nil
}

// TouchLastSeen 更新最近活跃时间。
func (r *PeerRepo) TouchLastSeen(ctx context.Context, id int64, ts int64) error {
	_, err := r.s.db.ExecContext(ctx, `UPDATE peer SET last_seen_at = ? WHERE id = ?`, ts, id)
	return err
}

// CountAll 返回 Peer 总数。
func (r *PeerRepo) CountAll(ctx context.Context) (int, error) {
	var n int
	err := r.s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM peer`).Scan(&n)
	return n, err
}

// AddressExists 判断地址是否已被占用。
func (r *PeerRepo) AddressExists(ctx context.Context, address string, excludeID int64) (bool, error) {
	var n int
	err := r.s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM peer WHERE address = ? AND id <> ?`, address, excludeID).Scan(&n)
	return n > 0, err
}

// UsedAddresses 返回已占用的地址集合，供 IPAM 分配。
func (r *PeerRepo) UsedAddresses(ctx context.Context) (map[string]bool, error) {
	rows, err := r.s.ro.QueryContext(ctx, `SELECT address FROM peer`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string]bool{}
	for rows.Next() {
		var addr string
		if err := rows.Scan(&addr); err != nil {
			return nil, err
		}
		out[addr] = true
	}
	return out, rows.Err()
}

// PolicyPeer 是策略引擎判定所需的最小字段集合。
type PolicyPeer struct {
	ID            int64
	Name          string
	Address       string
	Enabled       bool
	TrafficQuota  int64
	QuotaUsed     int64
	QuotaPeriod   domain.QuotaPeriod
	ExpiresAt     *time.Time
	SuspendedBy   domain.SuspensionReason
	RateLimitMbps int
	// QuotaPeriodStart 是已结算到的周期起点（Unix 秒），0 表示从未结算。
	QuotaPeriodStart int64
}

// ListForPolicy 返回策略引擎要判定的 Peer 列表。
// 只取必要列：策略每分钟跑一次，没必要把私钥密文也读进内存。
func (r *PeerRepo) ListForPolicy(ctx context.Context) ([]PolicyPeer, error) {
	rows, err := r.s.ro.QueryContext(ctx,
		`SELECT id, name, address, enabled, traffic_quota_bytes, quota_used_bytes, quota_period,
		        expires_at, suspended_by, rate_limit_mbps, quota_period_start
		 FROM peer ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("store: list peers for policy: %w", err)
	}
	defer rows.Close()

	out := make([]PolicyPeer, 0, 64)
	for rows.Next() {
		var p PolicyPeer
		var period, suspendedBy string
		var enabled int
		var expiresAt sql.NullInt64
		if err := rows.Scan(&p.ID, &p.Name, &p.Address, &enabled, &p.TrafficQuota, &p.QuotaUsed,
			&period, &expiresAt, &suspendedBy, &p.RateLimitMbps, &p.QuotaPeriodStart); err != nil {
			return nil, err
		}
		p.Enabled = enabled == 1
		p.QuotaPeriod = domain.QuotaPeriod(period)
		p.SuspendedBy = domain.SuspensionReason(suspendedBy)
		if expiresAt.Valid {
			t := unixTime(expiresAt.Int64)
			p.ExpiresAt = &t
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// SetEnabledByPolicy 写入策略引擎的启停决定。
//
// enabled 与 suspended_by 必须同时更新：只改 enabled 会让人工停用与策略停用无法区分，
// 周期重置时就会把管理员手动关掉的 Peer 重新打开。
func (r *PeerRepo) SetEnabledByPolicy(ctx context.Context, id int64, enabled bool,
	reason domain.SuspensionReason) error {
	_, err := r.s.db.ExecContext(ctx,
		`UPDATE peer SET enabled = ?, suspended_by = ?, updated_at = ? WHERE id = ?`,
		boolToInt(enabled), string(reason), nowUnix(), id)
	return err
}

// ResetQuotaUsage 清零指定 Peer 的周期用量，并记录已结算到的周期起点。
//
// 周期起点必须与清零在同一语句里写入：分开写的话，进程在两条语句之间崩溃
// 会留下「已清零但没记起点」的状态，下一轮判定会把用量再清一次——
// 而采集器可能已经在这中间累加了新的流量，那部分会被无声抹掉。
func (r *PeerRepo) ResetQuotaUsage(ctx context.Context, id int64, periodStart int64) error {
	_, err := r.s.db.ExecContext(ctx,
		`UPDATE peer SET quota_used_bytes = 0, quota_period_start = ?, updated_at = ? WHERE id = ?`,
		periodStart, nowUnix(), id)
	return err
}

// AdoptQuotaPeriod 只记录已观测到的周期起点，不动用量。
//
// 用于「第一次看到这个 Peer」：它可能是新建的，也可能是从旧版本升级来的
// （quota_period_start 默认 0）。这两种情况下 quota_used_bytes 里的数字属于
// 当前周期，结算它等于凭空送用户一份免费额度，所以只能认领起点，不能清零。
func (r *PeerRepo) AdoptQuotaPeriod(ctx context.Context, id int64, periodStart int64) error {
	_, err := r.s.db.ExecContext(ctx,
		`UPDATE peer SET quota_period_start = ? WHERE id = ? AND quota_period_start = 0`,
		periodStart, id)
	return err
}

// AddQuotaUsage 累加 Peer 的周期用量，返回累加后的值。
// 用 SQL 端加法而非「读-改-写」，避免与采集协程并发时丢增量。
func (r *PeerRepo) AddQuotaUsage(ctx context.Context, id int64, delta int64) (int64, error) {
	if delta <= 0 {
		var cur int64
		err := r.s.db.QueryRowContext(ctx,
			`SELECT quota_used_bytes FROM peer WHERE id = ?`, id).Scan(&cur)
		return cur, err
	}
	var total int64
	err := r.s.db.QueryRowContext(ctx,
		`UPDATE peer SET quota_used_bytes = quota_used_bytes + ?, updated_at = ? WHERE id = ?
		 RETURNING quota_used_bytes`, delta, nowUnix(), id).Scan(&total)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, domain.ErrNotFound("peer %d not found", id)
	}
	return total, err
}

// SetSubscriptionToken 为 Peer 签发订阅凭据。空字符串表示撤销。
func (r *PeerRepo) SetSubscriptionToken(ctx context.Context, id int64, token string) error {
	res, err := r.s.db.ExecContext(ctx,
		`UPDATE peer SET subscription_token = ?, updated_at = ? WHERE id = ?`, token, nowUnix(), id)
	if err != nil {
		return translatePeerError(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return domain.ErrNotFound("peer %d not found", id)
	}
	return nil
}

// GetBySubscriptionToken 按订阅凭据查询 Peer。
func (r *PeerRepo) GetBySubscriptionToken(ctx context.Context, token string) (*domain.Peer, error) {
	row := r.s.ro.QueryRowContext(ctx,
		`SELECT `+peerColumns+` FROM peer WHERE subscription_token = ?`, token)
	p, err := scanPeer(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.ErrNotFound("subscription not found")
	}
	if err != nil {
		return nil, err
	}
	p.SubscriptionToken = token
	return p, nil
}

// ListEnabledForExport 返回导出用的启用 Peer（不含私钥解密，交由服务层处理）。
func (r *PeerRepo) ListAllForExport(ctx context.Context) ([]domain.Peer, error) {
	rows, err := r.s.ro.QueryContext(ctx, `SELECT `+peerColumns+` FROM peer ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("store: list peers for export: %w", err)
	}
	defer rows.Close()

	out := make([]domain.Peer, 0, 64)
	for rows.Next() {
		p, err := scanPeer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

// BatchTag 为一组 Peer 追加或移除标签，返回实际变更条数。
//
// 标签存为 JSON 数组，没有可用的集合运算，因此逐条读改写。
// 代价是 N 次写事务，故调用方应把批量操作放在单个后台任务里，而不是每个 Peer 一次请求。
func (r *PeerRepo) BatchTag(ctx context.Context, ids []int64, tag string, remove bool) (int64, error) {
	if len(ids) == 0 || tag == "" {
		return 0, nil
	}
	changed := int64(0)
	for _, id := range ids {
		p, err := r.Get(ctx, id)
		if err != nil {
			continue
		}
		next := make([]string, 0, len(p.Tags)+1)
		found := false
		for _, t := range p.Tags {
			if t == tag {
				found = true
				if remove {
					continue
				}
			}
			next = append(next, t)
		}
		if found == !remove {
			// 目标状态已经满足，无需写入：
			//   追加（remove=false）：标签已存在；
			//   移除（remove=true）：标签本来就不存在。
			continue
		}
		if !remove {
			next = append(next, tag)
		}
		tags, err := json.Marshal(next)
		if err != nil {
			continue
		}
		if _, err := r.s.db.ExecContext(ctx,
			`UPDATE peer SET tags = ?, updated_at = ? WHERE id = ?`,
			string(tags), nowUnix(), id); err != nil {
			continue
		}
		changed++
	}
	return changed, nil
}

// ListByIDs 按 id 集合读取 Peer，用于批量操作前的存在性校验。
func (r *PeerRepo) ListByIDs(ctx context.Context, ids []int64) ([]domain.Peer, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	placeholders := make([]byte, 0, len(ids)*2)
	args := make([]any, 0, len(ids))
	for i, id := range ids {
		if i > 0 {
			placeholders = append(placeholders, ',')
		}
		placeholders = append(placeholders, '?')
		args = append(args, id)
	}
	rows, err := r.s.ro.QueryContext(ctx,
		`SELECT `+peerColumns+` FROM peer WHERE id IN (`+string(placeholders)+`) ORDER BY id`, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list peers by ids: %w", err)
	}
	defer rows.Close()

	out := make([]domain.Peer, 0, len(ids))
	for rows.Next() {
		p, err := scanPeer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

func scanPeer(row rowScanner) (*domain.Peer, error) {
	var p domain.Peer
	var tags string
	var quotaPeriod string
	var enabled int
	var suspendedBy string
	var expiresAt, lastSeenAt sql.NullInt64
	var createdAt, updatedAt int64

	if err := row.Scan(&p.ID, &p.Name, &p.PublicKey, &p.PrivateKey, &p.PresharedKey, &p.Address,
		&p.AllowedIPs, &p.Endpoint, &p.PersistentKeepalive, &p.Note, &tags, &p.TrafficQuotaBytes,
		&quotaPeriod, &p.RateLimitMbps, &p.MaxConnections, &enabled, &expiresAt, &p.QuotaUsedBytes,
		&suspendedBy, &lastSeenAt, &createdAt, &updatedAt); err != nil {
		return nil, err
	}

	p.Enabled = enabled == 1
	p.QuotaPeriod = domain.QuotaPeriod(quotaPeriod)
	p.SuspendedBy = domain.SuspensionReason(suspendedBy)
	if tags != "" {
		_ = json.Unmarshal([]byte(tags), &p.Tags)
	}
	if p.Tags == nil {
		p.Tags = []string{}
	}
	if expiresAt.Valid {
		t := unixTime(expiresAt.Int64)
		p.ExpiresAt = &t
	}
	if lastSeenAt.Valid {
		t := unixTime(lastSeenAt.Int64)
		p.LastSeenAt = &t
	}
	p.CreatedAt, p.UpdatedAt = unixTime(createdAt), unixTime(updatedAt)
	return &p, nil
}

// nullableUnix 将可空时间转为可写入 SQL 的值。
func nullableUnix(t *time.Time) any {
	if t == nil || t.IsZero() {
		return nil
	}
	return t.Unix()
}

// unixTime 将 Unix 秒还原为 time.Time。
func unixTime(sec int64) time.Time { return time.Unix(sec, 0) }

// translatePeerError 将 SQLite 唯一约束冲突翻译为领域错误。
func translatePeerError(err error) error {
	if err == nil {
		return nil
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "unique") && strings.Contains(msg, "address"):
		return domain.ErrConflict("peer address already in use")
	case strings.Contains(msg, "unique") && strings.Contains(msg, "public_key"):
		return domain.ErrConflict("peer public key already exists")
	case strings.Contains(msg, "unique"):
		return domain.ErrConflict("peer violates a uniqueness constraint")
	default:
		return err
	}
}
