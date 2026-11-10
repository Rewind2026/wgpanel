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
//
// 用户名唯一性按大小写不敏感判定：Admin 与 admin 是同一个账号。
// 判重与插入写在同一条语句里——拆成「先 SELECT 再 INSERT」的话，
// 两个并发的建号请求可以同时通过判重，落库后就出现两个「Admin」，
// 而登录时到底命中哪一个是不确定的。
func (r *UserRepo) Create(ctx context.Context, u *domain.User) (int64, error) {
	now := nowUnix()
	// 落库前统一成规范形式：库里不该出现只有大小写不同的两行。
	// 业务层已经归一化过了，这里再兜一次——
	// 判重是唯一性的最终关口，它不能依赖「调用方都记得归一化」。
	name := domain.CanonicalUsername(u.Username)
	res, err := r.s.db.ExecContext(ctx,
		`INSERT INTO user (username, email, password_hash, role, enabled, created_at, updated_at)
		 SELECT ?,?,?,?,?,?,?
		 WHERE NOT EXISTS (SELECT 1 FROM user WHERE lower(username) = lower(?))`,
		name, u.Email, u.PasswordHash, string(u.Role), boolToInt(u.Enabled), now, now,
		name)
	if err != nil {
		return 0, translateUserError(err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	if affected == 0 {
		// 判重命中：大小写不同的同名账号已存在。
		return 0, domain.ErrConflict("username already exists")
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	// 回写规范形式：返回给调用方的用户名必须与库里存的那一行一致，
	// 否则调用方会拿着「Admin」去引用一个实际叫「admin」的账号。
	u.Username = name
	u.ID, u.CreatedAt, u.UpdatedAt = id, unixTime(now), unixTime(now)
	return id, nil
}

// GetByUsername 按用户名查询，不区分大小写。
//
// 折叠放在 SQL 侧的 lower()，与 domain.CanonicalUsername 的口径一致
// （只折叠 ASCII，与 SQLite 自身的 lower() 语义相同）：
// Go 侧算「相等」和库侧算「相等」必须是同一个判定。
//
// 命中多行时明确报错而不是取第一行：库里若出现只差大小写的两个账号
// （旧库遗留或备份恢复带入），取哪一行都是猜——
// 猜中的那一行持有哪个角色，登录者拿到的就是哪个权限。
// 这种状态下宁可登不进来，也不能把权限给错人。
func (r *UserRepo) GetByUsername(ctx context.Context, username string) (*domain.User, error) {
	rows, err := r.s.db.QueryContext(ctx,
		`SELECT `+userColumns+` FROM user WHERE lower(username) = lower(?) LIMIT 2`, username)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var found []*domain.User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		found = append(found, u)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	switch len(found) {
	case 0:
		return nil, domain.ErrNotFound("user not found")
	case 1:
		return found[0], nil
	default:
		return nil, domain.ErrConflict("username is ambiguous: multiple accounts differ only by case")
	}
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

// List 返回全部用户（按 id 升序）。仅用于管理端账号列表，
// 字段与 scanUser 一致，密码哈希不参与 JSON 序列化。
func (r *UserRepo) List(ctx context.Context) ([]domain.User, error) {
	rows, err := r.s.db.QueryContext(ctx, `SELECT `+userColumns+` FROM user ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var users []domain.User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		users = append(users, *u)
	}
	return users, rows.Err()
}

// UpdatePassword 只更新密码哈希与会话代号。
//
// 密码是凭据而非资料：自助改密与管理员重置密码都只落这两个字段，
// 不给调用方顺手改到 role/enabled 的机会——那类变更走
// UpdateRole / UpdateEnabled，各自的校验规则完全不同。
//
// 会话代号用 SQL 自增而不是写回调用方读到的「当前值 +1」：
// 并发两次变更时，先读后写的陈旧绝对值可能把代号写回低位，
// 让已经被前一次变更作废的令牌重新通过校验——自增天然单调。
func (r *UserRepo) UpdatePassword(ctx context.Context, u *domain.User) error {
	res, err := r.s.db.ExecContext(ctx,
		`UPDATE user SET password_hash=?, token_version=token_version+1, updated_at=? WHERE id=?`,
		u.PasswordHash, nowUnix(), u.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return domain.ErrNotFound("user not found")
	}
	return nil
}

// otherEnabledOwnerExists 判断除目标自身外是否还有其他启用中的 owner。
// 作为 WHERE 子句的一部分使用，让「最后一个 owner」的判断与写入
// 落在同一条语句里——拆成「先 COUNT 再 UPDATE」的话，
// 两个 owner 并发互相停用会双双通过检查，把系统变成零 owner。
const otherEnabledOwnerExists = `(role<>'owner' OR EXISTS (
	SELECT 1 FROM user u2 WHERE u2.role='owner' AND u2.enabled=1 AND u2.id<>?
))`

// UpdateRole 只更新角色列与会话代号，返回语句是否真正命中了目标行。
//
// 刻意不与「启用状态」合并成一条整行 UPDATE：并发下另一列的陈旧值
// 会冲掉别人刚做的变更（例如改角色时把刚被停用的账号重新启用）。
// 这里只改调用方意图改的那一列。
//
// protectLastOwner 为真时附带「还有其他启用 owner」的守卫；
// 守卫不成立时返回 false，由上层翻译成业务冲突。
func (r *UserRepo) UpdateRole(ctx context.Context, id int64, role domain.Role, protectLastOwner bool) (bool, error) {
	query := `UPDATE user SET role=?, token_version=token_version+1, updated_at=? WHERE id=?`
	args := []any{string(role), nowUnix(), id}
	if protectLastOwner {
		query += ` AND ` + otherEnabledOwnerExists
		args = append(args, id)
	}
	res, err := r.s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// UpdateEnabled 只更新启用状态与会话代号，返回语句是否真正命中了目标行。
//
// 停用 owner 时同样带「还有其他启用 owner」的守卫：
// 停用是把系统变成零 owner 的主要通道，判断必须与写入原子。
// 启用方向不设守卫——启用只会增加可用的 owner。
func (r *UserRepo) UpdateEnabled(ctx context.Context, id int64, enabled bool, protectLastOwner bool) (bool, error) {
	query := `UPDATE user SET enabled=?, token_version=token_version+1, updated_at=? WHERE id=?`
	args := []any{boolToInt(enabled), nowUnix(), id}
	if protectLastOwner {
		query += ` AND ` + otherEnabledOwnerExists
		args = append(args, id)
	}
	res, err := r.s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// Delete 删除用户，返回语句是否真正命中了目标行。
// refresh_token 对 user 是 ON DELETE CASCADE，会话令牌随之清空；
// 审计日志只存 actor_name 快照，不依赖 JOIN，不受影响。
// protectLastOwner 为真时，只有在还有其他启用 owner 时才删除。
func (r *UserRepo) Delete(ctx context.Context, id int64, protectLastOwner bool) (bool, error) {
	query := `DELETE FROM user WHERE id=?`
	args := []any{id}
	if protectLastOwner {
		query += ` AND ` + otherEnabledOwnerExists
		args = append(args, id)
	}
	res, err := r.s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
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
