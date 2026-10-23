package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/Rewind2026/wgpanel/internal/domain"
)

// ExportQuery 是审计导出条件。与 AuditQuery 的区别是不分页但有硬上限。
type ExportQuery struct {
	Action   string
	Resource string
	Result   string
	From     int64
	To       int64
	Limit    int
}

// MaxExportRows 是单次审计导出的硬上限。
//
// 审计表只增不减，没有上限的导出可以一次拉走全部历史并把面板内存打满。
const MaxExportRows = 50000

// Export 按条件导出审计记录，不分页。
func (r *AuditRepo) Export(ctx context.Context, q ExportQuery) ([]domain.AuditEvent, error) {
	limit := q.Limit
	if limit < 1 || limit > MaxExportRows {
		limit = MaxExportRows
	}
	where := []string{"1=1"}
	args := []any{}
	if q.Action != "" {
		where = append(where, "action = ?")
		args = append(args, q.Action)
	}
	if q.Resource != "" {
		where = append(where, "resource = ?")
		args = append(args, q.Resource)
	}
	if q.Result != "" {
		where = append(where, "result = ?")
		args = append(args, q.Result)
	}
	if q.From > 0 {
		where = append(where, "created_at >= ?")
		args = append(args, q.From)
	}
	if q.To > 0 {
		where = append(where, "created_at <= ?")
		args = append(args, q.To)
	}

	args = append(args, limit)
	rows, err := r.s.ro.QueryContext(ctx,
		`SELECT id, actor_id, actor_name, action, resource, resource_id, detail, ip, ua, result, created_at
		 FROM audit_log WHERE `+joinConditions(where)+` ORDER BY id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, fmt.Errorf("store: export audit: %w", err)
	}
	defer rows.Close()

	out := make([]domain.AuditEvent, 0, 1024)
	for rows.Next() {
		var e domain.AuditEvent
		var actorID *int64
		var createdAt int64
		if err := rows.Scan(&e.ID, &actorID, &e.ActorName, &e.Action, &e.Resource, &e.ResourceID,
			&e.Detail, &e.IP, &e.UserAgent, &e.Result, &createdAt); err != nil {
			return nil, err
		}
		e.ActorID, e.CreatedAt = actorID, unixTime(createdAt)
		out = append(out, e)
	}
	if errors.Is(rows.Err(), context.Canceled) {
		return nil, rows.Err()
	}
	return out, rows.Err()
}
