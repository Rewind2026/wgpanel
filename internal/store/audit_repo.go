package store

import (
	"context"

	"github.com/Rewind2026/wgpanel/internal/domain"
	"github.com/Rewind2026/wgpanel/internal/reqmeta"
)

// joinConditions 把 WHERE 片段拼成完整条件子句。
func joinConditions(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += " AND "
		}
		out += p
	}
	return out
}

// AuditRepo 负责审计日志写入与检索。
type AuditRepo struct{ s *Store }

// AuditRepo 返回审计仓储。
func (s *Store) AuditRepo() *AuditRepo { return &AuditRepo{s: s} }

// Write 写入一条审计记录。
//
// 来源 IP 与 User-Agent 在这里补齐，而不是要求每个调用方填：
// 调用方分布在 service 层，拿不到 *http.Request，填了也会漏。
// 非 HTTP 来源（定时任务、策略引擎）补不上，字段就留空，符合语义。
func (r *AuditRepo) Write(ctx context.Context, e *domain.AuditEvent) error {
	var actorID any
	if e.ActorID != nil {
		actorID = *e.ActorID
	}
	result := e.Result
	if result == "" {
		result = domain.AuditSuccess
	}
	reqmeta.FillIn(ctx, &e.IP, &e.UserAgent)
	_, err := r.s.db.ExecContext(ctx,
		`INSERT INTO audit_log (actor_id, actor_name, action, resource, resource_id, detail, ip, ua, result, created_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?)`,
		actorID, e.ActorName, string(e.Action), e.Resource, e.ResourceID, e.Detail, e.IP, e.UserAgent,
		string(result), nowUnix())
	return err
}

// Query 按条件分页检索审计记录。
func (r *AuditRepo) Query(ctx context.Context, q domain.AuditQuery) ([]domain.AuditEvent, int, error) {
	where := []string{"1=1"}
	args := []any{}

	if q.Action != "" {
		where = append(where, "action = ?")
		args = append(args, q.Action)
	}
	if q.ActorID != nil {
		where = append(where, "actor_id = ?")
		args = append(args, *q.ActorID)
	}
	if q.Resource != "" {
		where = append(where, "resource = ?")
		args = append(args, q.Resource)
	}
	if q.Result != "" {
		where = append(where, "result = ?")
		args = append(args, q.Result)
	}
	if q.From != nil {
		where = append(where, "created_at >= ?")
		args = append(args, q.From.Unix())
	}
	if q.To != nil {
		where = append(where, "created_at <= ?")
		args = append(args, q.To.Unix())
	}
	clause := joinConditions(where)

	var total int
	if err := r.s.ro.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_log WHERE `+clause, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	page, size := q.Page, q.PageSize
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 200 {
		size = 50
	}
	listArgs := append(append([]any{}, args...), size, (page-1)*size)
	rows, err := r.s.ro.QueryContext(ctx,
		`SELECT id, actor_id, actor_name, action, resource, resource_id, detail, ip, ua, result, created_at
		 FROM audit_log WHERE `+clause+` ORDER BY id DESC LIMIT ? OFFSET ?`, listArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	out := make([]domain.AuditEvent, 0, size)
	for rows.Next() {
		var e domain.AuditEvent
		var actorID *int64
		var createdAt int64
		if err := rows.Scan(&e.ID, &actorID, &e.ActorName, &e.Action, &e.Resource, &e.ResourceID,
			&e.Detail, &e.IP, &e.UserAgent, &e.Result, &createdAt); err != nil {
			return nil, 0, err
		}
		e.ActorID, e.CreatedAt = actorID, unixTime(createdAt)
		out = append(out, e)
	}
	return out, total, rows.Err()
}

// DeleteOlderThan 清理过期审计日志。
func (r *AuditRepo) DeleteOlderThan(ctx context.Context, before int64) (int64, error) {
	res, err := r.s.db.ExecContext(ctx, `DELETE FROM audit_log WHERE created_at < ?`, before)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// SaveSnapshot 保存一份配置快照（备份/导入导出）。
func (r *AuditRepo) SaveSnapshot(ctx context.Context, kind string, payload []byte, operatorID *int64) error {
	var op any
	if operatorID != nil {
		op = *operatorID
	}
	_, err := r.s.db.ExecContext(ctx,
		`INSERT INTO config_snapshot (kind, payload, size, operator_id, created_at) VALUES (?,?,?,?,?)`,
		kind, payload, len(payload), op, nowUnix())
	return err
}

// LoadLatestSnapshot 读取最近一次指定类型的快照。
func (r *AuditRepo) LoadLatestSnapshot(ctx context.Context, kind string) ([]byte, error) {
	var payload []byte
	err := r.s.ro.QueryRowContext(ctx,
		`SELECT payload FROM config_snapshot WHERE kind = ? ORDER BY id DESC LIMIT 1`, kind).Scan(&payload)
	if err != nil {
		return nil, err
	}
	return payload, nil
}
