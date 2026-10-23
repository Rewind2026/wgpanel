package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/Rewind2026/wgpanel/internal/domain"
)

// UserRepo 负责 user 与 refresh_token。
type UserRepo struct{ s *Store }

// UserRepo 返回用户仓储。
func (s *Store) UserRepo() *UserRepo { return &UserRepo{s: s} }

const userColumns = `id, username, email, password_hash, role, enabled, last_login_at, created_at, updated_at, token_version`

// CountAll 返回用户总数。
func (r *UserRepo) CountAll(ctx context.Context) (int, error) {
	var n int
	err := r.s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM user`).Scan(&n)
	return n, err
}

// Create 插入用户。首个 owner 由 AuthService.EnsureOwner 调用，
// 是这个方法在生产路径上唯一的调用方。
func (r *UserRepo) Create(ctx context.Context, u *domain.User) (int64, error) {
	now := nowUnix()
	res, err := r.s.db.ExecContext(ctx,
		`INSERT INTO user (username, email, password_hash, role, enabled, created_at, updated_at)
		 VALUES (?,?,?,?,?,?,?)`,
		u.Username, u.Email, u.PasswordHash, string(u.Role), boolToInt(u.Enabled), now, now)
	if err != nil {
		return 0, translateUserError(err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	u.ID, u.CreatedAt, u.UpdatedAt = id, unixTime(now), unixTime(now)
	return id, nil
}

// GetByUsername 按用户名查询。
func (r *UserRepo) GetByUsername(ctx context.Context, username string) (*domain.User, error) {
	row := r.s.db.QueryRowContext(ctx, `SELECT `+userColumns+` FROM user WHERE username = ?`, username)
	u, err := scanUser(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.ErrNotFound("user not found")
	}
	if err != nil {
		return nil, err
	}
	return u, nil
}

// GetByID 按 id 查询。
func (r *UserRepo) GetByID(ctx context.Context, id int64) (*domain.User, error) {
	row := r.s.db.QueryRowContext(ctx, `SELECT `+userColumns+` FROM user WHERE id = ?`, id)
	u, err := scanUser(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.ErrNotFound("user not found")
	}
	if err != nil {
		return nil, err
	}
	return u, nil
}

// UpdatePassword 只更新密码哈希与会话代号。
//
// 刻意不提供「更新全部可变字段」的入口：面板是单人部署，没有角色分配
// 与账号启用的需求，留一条全字段 UPDATE 只会让调用方有机会改到不该改的列。
func (r *UserRepo) UpdatePassword(ctx context.Context, u *domain.User) error {
	res, err := r.s.db.ExecContext(ctx,
		`UPDATE user SET password_hash=?, token_version=?, updated_at=? WHERE id=?`,
		u.PasswordHash, u.TokenVersion, nowUnix(), u.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return domain.ErrNotFound("user not found")
	}
	return nil
}

// TouchLastLogin 更新最近登录时间。
func (r *UserRepo) TouchLastLogin(ctx context.Context, id int64) error {
	_, err := r.s.db.ExecContext(ctx, `UPDATE user SET last_login_at = ? WHERE id = ?`, nowUnix(), id)
	return err
}

// StoreRefreshToken 保存刷新令牌哈希。
func (r *UserRepo) StoreRefreshToken(ctx context.Context, userID int64, token string, expiresAt int64) error {
	_, err := r.s.db.ExecContext(ctx,
		`INSERT INTO refresh_token (user_id, token_hash, expires_at, revoked, created_at) VALUES (?,?,?,0,?)`,
		userID, HashToken(token), expiresAt, nowUnix())
	if err != nil {
		return fmt.Errorf("store: save refresh token: %w", err)
	}
	return nil
}

// ConsumeRefreshToken 校验并撤销刷新令牌，返回所属用户 id。
//
// 用单条 UPDATE ... RETURNING 完成「校验 + 撤销 + 取主」，让轮换在并发下保持一次性：
// 若拆成先查后撤，两个并发刷新都能通过校验，同一令牌会被使用两次。
func (r *UserRepo) ConsumeRefreshToken(ctx context.Context, token string) (int64, error) {
	var userID int64
	err := r.s.db.QueryRowContext(ctx,
		`UPDATE refresh_token SET revoked = 1
		 WHERE token_hash = ? AND revoked = 0 AND expires_at > ?
		 RETURNING user_id`,
		HashToken(token), nowUnix()).Scan(&userID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, domain.ErrUnauthorized("refresh token is invalid, revoked or expired")
	}
	if err != nil {
		return 0, fmt.Errorf("store: consume refresh token: %w", err)
	}
	return userID, nil
}

// HashToken 计算凭据的 SHA-256 十六进制哈希。
//
// 刷新令牌与 API 令牌都只存哈希：库被拖走也无法反推出可用凭据。
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// RevokeAllUserTokens 强制下线某用户的全部会话。
func (r *UserRepo) RevokeAllUserTokens(ctx context.Context, userID int64) error {
	_, err := r.s.db.ExecContext(ctx, `UPDATE refresh_token SET revoked = 1 WHERE user_id = ?`, userID)
	return err
}

// CleanupExpiredTokens 清理过期令牌，返回清理条数。
func (r *UserRepo) CleanupExpiredTokens(ctx context.Context) (int64, error) {
	res, err := r.s.db.ExecContext(ctx,
		`DELETE FROM refresh_token WHERE expires_at < ?`, nowUnix())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func scanUser(row rowScanner) (*domain.User, error) {
	var u domain.User
	var role string
	var enabled int
	var lastLoginAt sql.NullInt64
	var createdAt, updatedAt int64
	if err := row.Scan(&u.ID, &u.Username, &u.Email, &u.PasswordHash, &role, &enabled,
		&lastLoginAt, &createdAt, &updatedAt, &u.TokenVersion); err != nil {
		return nil, err
	}
	u.Role = domain.Role(role)
	u.Enabled = enabled == 1
	if lastLoginAt.Valid {
		t := unixTime(lastLoginAt.Int64)
		u.LastLoginAt = &t
	}
	u.CreatedAt, u.UpdatedAt = unixTime(createdAt), unixTime(updatedAt)
	return &u, nil
}

func translateUserError(err error) error {
	if err == nil {
		return nil
	}
	if strings.Contains(err.Error(), "UNIQUE") {
		return domain.ErrConflict("username already exists")
	}
	return err
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
