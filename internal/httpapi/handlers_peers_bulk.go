package httpapi

import (
	"io"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/Rewind2026/wgpanel/internal/domain"
)

// maxImportBytes 是 CSV 导入的请求体上限。
//
// 一次导入几百个客户端是几十 KB 量级；给到 4 MiB 已经远超正常需要，
// 再大就只是给内存找麻烦。
const maxImportBytes = 4 << 20

// handleExportPeersCSV 导出 Peer 明细。
func (s *Server) handleExportPeersCSV(w http.ResponseWriter, r *http.Request) {
	data, err := s.peers.ExportCSV(r.Context())
	if err != nil {
		respondError(w, s.log, err)
		return
	}
	if s.audit != nil {
		p := principalFrom(r.Context())
		_ = s.audit.Write(r.Context(), &domain.AuditEvent{
			ActorID: actorID(p), ActorName: actorName(p), Action: domain.AuditConfigExport,
			Resource: "peer", Result: domain.AuditSuccess, Detail: "csv",
		})
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=wgpanel-peers.csv")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// handleImportPeersCSV 批量导入 Peer。
func (s *Server) handleImportPeersCSV(w http.ResponseWriter, r *http.Request) {
	// 限制读取长度而不是信任 Content-Length：后者可被伪造。
	body, err := io.ReadAll(io.LimitReader(r.Body, maxImportBytes+1))
	if err != nil {
		respondError(w, s.log, domain.ErrInvalid("cannot read request body"))
		return
	}
	if len(body) > maxImportBytes {
		respondError(w, s.log, domain.ErrInvalid("csv exceeds %d bytes", maxImportBytes))
		return
	}
	res, err := s.peers.ImportCSV(r.Context(), body, principalFrom(r.Context()))
	if err != nil {
		respondError(w, s.log, err)
		return
	}
	// 逐行导入刻意返回 200：HTTP 层没有「部分成功」的状态码，
	// 用 207 之类的自定义码会让前端与 curl 都得多写一层判断。
	respondJSON(w, http.StatusOK, res)
}

type bulkTagRequest struct {
	IDs    []int64 `json:"ids"`
	Tag    string  `json:"tag"`
	Remove bool    `json:"remove"`
}

// handleBulkTag 批量打标签。
func (s *Server) handleBulkTag(w http.ResponseWriter, r *http.Request) {
	var in bulkTagRequest
	if err := decodeJSON(r, &in); err != nil {
		respondError(w, s.log, err)
		return
	}
	if len(in.IDs) == 0 {
		respondError(w, s.log, domain.ErrInvalid("ids must not be empty"))
		return
	}
	n, err := s.peers.BatchTag(r.Context(), in.IDs, in.Tag, in.Remove, principalFrom(r.Context()))
	if err != nil {
		respondError(w, s.log, err)
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"changed": n})
}

type bulkEnableRequest struct {
	IDs     []int64 `json:"ids"`
	Enabled bool    `json:"enabled"`
}

// handleBulkEnable 批量启用或停用。
func (s *Server) handleBulkEnable(w http.ResponseWriter, r *http.Request) {
	var in bulkEnableRequest
	if err := decodeJSON(r, &in); err != nil {
		respondError(w, s.log, err)
		return
	}
	if len(in.IDs) == 0 {
		respondError(w, s.log, domain.ErrInvalid("ids must not be empty"))
		return
	}
	n, err := s.peers.BulkEnable(r.Context(), in.IDs, in.Enabled, principalFrom(r.Context()))
	if err != nil {
		respondError(w, s.log, err)
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"changed": n})
}

// handleIssueSubscription 签发订阅链接。
func (s *Server) handleIssueSubscription(w http.ResponseWriter, r *http.Request) {
	id, err := peerID(r)
	if err != nil {
		respondError(w, s.log, err)
		return
	}
	link, err := s.peers.IssueSubscription(r.Context(), id, s.publicBaseURL(r), s.publicEndpoint(),
		principalFrom(r.Context()))
	if err != nil {
		respondError(w, s.log, err)
		return
	}
	respondJSON(w, http.StatusCreated, link)
}

// handleRevokeSubscription 撤销订阅链接。
func (s *Server) handleRevokeSubscription(w http.ResponseWriter, r *http.Request) {
	id, err := peerID(r)
	if err != nil {
		respondError(w, s.log, err)
		return
	}
	if err := s.peers.RevokeSubscription(r.Context(), id, principalFrom(r.Context())); err != nil {
		respondError(w, s.log, err)
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"revoked": id})
}

// handleSubscriptionConfig 按订阅令牌返回客户端配置。
//
// 这个端点不带面板鉴权，令牌本身就是凭据：
// 客户端订阅器（v2rayNG、Shadowrocket 等）无法携带面板的 JWT。
// 因此令牌必须足够随机，且可随时撤销。
func (s *Server) handleSubscriptionConfig(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimSpace(chi.URLParam(r, "token"))
	if token == "" {
		respondError(w, s.log, domain.ErrNotFound("subscription not found"))
		return
	}
	conf, err := s.peers.SubscriptionConfig(r.Context(), token, s.publicEndpoint())
	if err != nil {
		respondError(w, s.log, err)
		return
	}
	// 订阅内容是长期凭据，任何中间缓存都不能留副本。
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Profile-Update-Interval", "24")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(conf))
}

// publicBaseURL 推断面板自身的可访问地址，供订阅链接拼装使用。
//
// 只能按当前请求的 scheme + host 反推，绝不能复用 publicEndpoint：
// 后者是 WireGuard 的 UDP 对端地址（1.2.3.4），与面板的 HTTP 地址毫无关系。
// 混用会拼出 "1.2.3.4/api/v1/subscription/<token>" 这种没有 scheme 的东西，
// 客户端订阅器解析不了，而面板看上去一切正常。
//
// 反推在反向代理场景下需要 X-Forwarded-*，且只在明确信任代理时才采信，
// 否则任何人伪造一个 Host 就能把订阅链接指向自己的服务器。
func (s *Server) publicBaseURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if fwd := r.Header.Get("X-Forwarded-Proto"); fwd != "" && s.trustProxy {
		scheme = fwd
	}
	host := r.Host
	if s.trustProxy {
		if v := r.Header.Get("X-Forwarded-Host"); v != "" {
			host = v
		}
	}
	return scheme + "://" + host
}
