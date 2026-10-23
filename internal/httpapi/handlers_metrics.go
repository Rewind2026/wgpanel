package httpapi

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Rewind2026/wgpanel/internal/domain"
	"github.com/Rewind2026/wgpanel/internal/store"
)

// handleMetricsOverview 返回服务器 + 隧道汇总快照。
func (s *Server) handleMetricsOverview(w http.ResponseWriter, r *http.Request) {
	snap, ok := s.collector.Last()
	if !ok {
		// 采集器尚未产出首帧时立即采一次，避免页面空白。
		snap = s.collector.SnapshotNow(r.Context())
	}
	respondJSON(w, http.StatusOK, map[string]any{
		"system":   snap.System,
		"tunnel":   snap.Tunnel,
		"interval": s.cfg.SampleInterval.String(),
	})
}

// handlePeerMetrics 返回每个 Peer 的实时统计。
func (s *Server) handlePeerMetrics(w http.ResponseWriter, r *http.Request) {
	snap := s.collector.SnapshotNow(r.Context())
	respondJSON(w, http.StatusOK, snap.Peers)
}

// handleTrafficHistory 返回历史流量曲线。
func (s *Server) handleTrafficHistory(w http.ResponseWriter, r *http.Request) {
	from, to, bucket := store.WindowToRange(r.URL.Query().Get("range"))

	var peerID *int64
	if v := r.URL.Query().Get("peerId"); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			respondError(w, s.log, domain.ErrInvalid("peerId must be an integer"))
			return
		}
		peerID = &id
	}
	points, err := s.traffic.QueryTraffic(r.Context(), from.Unix(), to.Unix(), bucket, peerID)
	if err != nil {
		respondError(w, s.log, domain.ErrInternal(err))
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{
		"range":  r.URL.Query().Get("range"),
		"bucket": bucket,
		"from":   from.Unix(),
		"to":     to.Unix(),
		"points": points,
	})
}

// handleAuditQuery 返回审计日志检索结果。
func (s *Server) handleAuditQuery(w http.ResponseWriter, r *http.Request) {
	q := domain.AuditQuery{
		Action:   r.URL.Query().Get("action"),
		Resource: r.URL.Query().Get("resource"),
		Result:   r.URL.Query().Get("result"),
		Page:     1,
		PageSize: 50,
	}
	if v := r.URL.Query().Get("page"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			q.Page = n
		}
	}
	if v := r.URL.Query().Get("pageSize"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			q.PageSize = n
		}
	}
	// 时间格式错误必须报错而不是静默忽略：忽略会让用户看到「无结果」，
	// 却完全不知道是自己把日期格式填错了。
	if v := r.URL.Query().Get("from"); v != "" {
		n, err := parseTimestampParam(v)
		if err != nil {
			respondError(w, s.log, domain.ErrInvalid("from must be an RFC3339 timestamp or unix seconds"))
			return
		}
		from := time.Unix(n, 0).UTC()
		q.From = &from
	}
	if v := r.URL.Query().Get("to"); v != "" {
		n, err := parseTimestampParam(v)
		if err != nil {
			respondError(w, s.log, domain.ErrInvalid("to must be an RFC3339 timestamp or unix seconds"))
			return
		}
		to := time.Unix(n, 0).UTC()
		q.To = &to
	}

	items, total, err := s.audit.Query(r.Context(), q)
	if err != nil {
		respondError(w, s.log, domain.ErrInternal(err))
		return
	}
	respondJSON(w, http.StatusOK, pageResult{Items: items, Total: total, Page: q.Page, PageSize: q.PageSize})
}

// peerID 从路由参数解析 Peer id。
func peerID(r *http.Request) (int64, error) {
	raw := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, domain.ErrInvalid("peer id must be a positive integer")
	}
	return id, nil
}
