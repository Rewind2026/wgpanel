package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Rewind2026/wgpanel/internal/domain"
)

// SettingRepo 负责 app_setting 键值表。
type SettingRepo struct{ s *Store }

// SettingRepo 返回设置仓储。
func (s *Store) SettingRepo() *SettingRepo { return &SettingRepo{s: s} }

// settingsKey 是设置整体的存储键。
const settingsKey = "settings"

// LoadSettings 读取设置；从未保存过时返回内置默认值。
func (r *SettingRepo) LoadSettings(ctx context.Context) (domain.Settings, error) {
	var raw string
	err := r.s.ro.QueryRowContext(ctx,
		`SELECT value FROM app_setting WHERE key = ?`, settingsKey).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.DefaultSettings(), nil
	}
	if err != nil {
		return domain.Settings{}, fmt.Errorf("store: load settings: %w", err)
	}
	out := domain.DefaultSettings()
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		// 设置损坏时回落到默认值而不是让面板起不来：设置项都可重新填写，
		// 而因一条脏数据拒绝启动会把管理员挡在门外。
		return domain.DefaultSettings(), nil
	}
	return out, nil
}

// SaveSettings 覆盖保存设置。
func (r *SettingRepo) SaveSettings(ctx context.Context, s domain.Settings) error {
	raw, err := json.Marshal(s)
	if err != nil {
		return err
	}
	_, err = r.s.db.ExecContext(ctx,
		`INSERT INTO app_setting (key, value, updated_at) VALUES (?,?,?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		settingsKey, string(raw), nowUnix())
	if err != nil {
		return fmt.Errorf("store: save settings: %w", err)
	}
	return nil
}

// BackupRepo 负责备份索引。
type BackupRepo struct{ s *Store }

// BackupRepo 返回备份索引仓储。
func (s *Store) BackupRepo() *BackupRepo { return &BackupRepo{s: s} }

const backupColumns = `id, filename, size_bytes, sha256, schema_version, peer_count, user_count, kind, operator_id, created_at`

// Record 登记一次备份。
func (r *BackupRepo) Record(ctx context.Context, rec *domain.BackupRecord) (int64, error) {
	var op any
	if rec.OperatorID != nil {
		op = *rec.OperatorID
	}
	res, err := r.s.db.ExecContext(ctx,
		`INSERT INTO backup_record (filename, size_bytes, sha256, schema_version, peer_count,
		 user_count, kind, operator_id, created_at) VALUES (?,?,?,?,?,?,?,?,?)`,
		rec.Filename, rec.SizeBytes, rec.SHA256, rec.SchemaVersion, rec.PeerCount,
		rec.UserCount, rec.Kind, op, nowUnix())
	if err != nil {
		return 0, fmt.Errorf("store: record backup: %w", err)
	}
	id, _ := res.LastInsertId()
	rec.ID, rec.CreatedAt = id, unixTime(nowUnix())
	return id, nil
}

// List 返回备份索引，按时间倒序。
func (r *BackupRepo) List(ctx context.Context) ([]domain.BackupRecord, error) {
	rows, err := r.s.ro.QueryContext(ctx,
		`SELECT `+backupColumns+` FROM backup_record ORDER BY id DESC`)
	if err != nil {
		return nil, fmt.Errorf("store: list backups: %w", err)
	}
	defer rows.Close()

	out := make([]domain.BackupRecord, 0, 16)
	for rows.Next() {
		var rec domain.BackupRecord
		var op *int64
		var createdAt int64
		if err := rows.Scan(&rec.ID, &rec.Filename, &rec.SizeBytes, &rec.SHA256, &rec.SchemaVersion,
			&rec.PeerCount, &rec.UserCount, &rec.Kind, &op, &createdAt); err != nil {
			return nil, err
		}
		rec.OperatorID, rec.CreatedAt = op, unixTime(createdAt)
		out = append(out, rec)
	}
	return out, rows.Err()
}

// Delete 删除备份索引记录。
func (r *BackupRepo) Delete(ctx context.Context, id int64) error {
	res, err := r.s.db.ExecContext(ctx, `DELETE FROM backup_record WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return domain.ErrNotFound("backup %d not found", id)
	}
	return nil
}

// SchemaVersion 返回当前数据库版本号，供备份写入元信息并在恢复时校验。
func (r *Store) SchemaVersion(ctx context.Context) (int, error) {
	var v sql.NullInt64
	err := r.ro.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migration`).Scan(&v)
	if err != nil {
		return 0, fmt.Errorf("store: read schema version: %w", err)
	}
	if !v.Valid {
		return 0, nil
	}
	return int(v.Int64), nil
}

// TrimBackupIndex 只保留最近 keep 条备份索引，返回被淘汰的记录。
func (r *BackupRepo) TrimBackupIndex(ctx context.Context, keep int) ([]domain.BackupRecord, error) {
	all, err := r.List(ctx)
	if err != nil {
		return nil, err
	}
	if keep < 1 || len(all) <= keep {
		return nil, nil
	}
	return all[keep:], nil
}
