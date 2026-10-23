package httpapi

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/Rewind2026/wgpanel/internal/domain"
)

// envelope 是所有 API 的统一响应包络。
type envelope struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data"`
}

// respondJSON 写出成功响应。
func respondJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(envelope{Code: 0, Message: "ok", Data: data}); err != nil {
		slog.Debug("write response failed", "error", err)
	}
}

// respondError 写出错误响应，并把内部错误细节留在服务端日志。
func respondError(w http.ResponseWriter, log *slog.Logger, err error) {
	de := domain.AsError(err)
	if de == nil {
		de = domain.ErrInternal(nil)
	}
	if de.Code == domain.CodeInternal {
		log.Error("request failed", "error", err)
	} else {
		log.Debug("request rejected", "code", de.Code, "message", de.Message)
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(de.HTTPStatus)
	_ = json.NewEncoder(w).Encode(envelope{Code: de.Code, Message: de.Message})
}

// decodeJSON 解析请求体，拒绝未知字段以避免静默拼写错误。
func decodeJSON(r *http.Request, dst any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return domain.ErrInvalid("malformed request body: %s", err.Error())
	}
	return nil
}

// pageResult 是分页响应的数据结构。
type pageResult struct {
	Items    any `json:"items"`
	Total    int `json:"total"`
	Page     int `json:"page"`
	PageSize int `json:"pageSize"`
}
