package httpapi

import (
	"encoding/csv"
	"net/http"
	"strconv"
	"time"

	"github.com/Rewind2026/wgpanel/internal/domain"
	"github.com/Rewind2026/wgpanel/internal/store"
)

// auditCSVHeader 是审计导出的列定义。
var auditCSVHeader = []string{
	"id", "createdAt", "actorName", "action", "resource", "resourceID",
	"result", "ip", "userAgent", "detail",
}

// parseTimestampParam 解析时间参数，返回 Unix 秒。
//
// 优先 RFC3339（与 /audit 一致，也是前端直接透传的形式），
// 退而接受 Unix 秒以兼容脚本调用。
func parseTimestampParam(v string) (int64, error) {
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t.Unix(), nil
	}
	return strconv.ParseInt(v, 10, 64)
}

// handleExportAudit 导出审计日志为 CSV。
func (s *Server) handleExportAudit(w http.ResponseWriter, r *http.Request) {
	q := store.ExportQuery{
		Action:   r.URL.Query().Get("action"),
		Resource: r.URL.Query().Get("resource"),
		Result:   r.URL.Query().Get("result"),
	}
	// from/to 与 /audit 保持同一套解析规则（RFC3339），否则用户在检索页筛一个
	// 时间范围再点导出就会 400：同一个参数在兄弟端点上含义不同是最难排查的一类缺陷。
	// Unix 秒仍然接受，兼容按秒调用的脚本。
	if v := r.URL.Query().Get("from"); v != "" {
		n, err := parseTimestampParam(v)
		if err != nil {
			respondError(w, s.log, domain.ErrInvalid("from must be an RFC3339 timestamp or unix seconds"))
			return
		}
		q.From = n
	}
	if v := r.URL.Query().Get("to"); v != "" {
		n, err := parseTimestampParam(v)
		if err != nil {
			respondError(w, s.log, domain.ErrInvalid("to must be an RFC3339 timestamp or unix seconds"))
			return
		}
		q.To = n
	}
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 || n > store.MaxExportRows {
			respondError(w, s.log, domain.ErrInvalid("limit must be between 1 and %d", store.MaxExportRows))
			return
		}
		q.Limit = n
	}

	items, err := s.audit.Export(r.Context(), q)
	if err != nil {
		respondError(w, s.log, err)
		return
	}

	// 用流式 csv.Writer 而不是先拼 []byte 再写出：
	// 单次导出上限 5 万行，先入内存峰值会到几十 MB。
	filename := "wgpanel-audit-" + time.Now().UTC().Format("20060102T150405Z") + ".csv"
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	w.WriteHeader(http.StatusOK)

	cw := csv.NewWriter(w)
	_ = cw.Write(auditCSVHeader)
	for i := range items {
		e := items[i]
		_ = cw.Write([]string{
			strconv.FormatInt(e.ID, 10),
			e.CreatedAt.UTC().Format(time.RFC3339),
			e.ActorName, string(e.Action), e.Resource, e.ResourceID,
			string(e.Result), e.IP, e.UserAgent, e.Detail,
		})
	}
	cw.Flush()
	// 响应头已经发出，写失败无法再改状态码，只能记日志。
	if err := cw.Error(); err != nil {
		s.log.Warn("audit csv write failed", "error", err)
	}

	if s.audit != nil {
		p := principalFrom(r.Context())
		_ = s.audit.Write(r.Context(), &domain.AuditEvent{
			ActorID: actorID(p), ActorName: actorName(p),
			Action: domain.AuditConfigExport, Resource: "audit", Result: domain.AuditSuccess,
			Detail: "csv rows=" + strconv.Itoa(len(items)),
		})
	}
}
