package httpapi

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/Rewind2026/wgpanel/internal/domain"
)

type changePasswordRequest struct {
	Password string `json:"password"`
}

// handleGetProfile 返回当前登录账号的资料。
func (s *Server) handleGetProfile(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	if p == nil {
		respondError(w, s.log, domain.ErrUnauthorized("missing access token"))
		return
	}
	u, err := s.account.Profile(r.Context(), p.UserID)
	if err != nil {
		respondError(w, s.log, err)
		return
	}
	respondJSON(w, http.StatusOK, u)
}

// handleChangePassword 修改当前登录账号的密码。
//
// 目标用户 id 取自请求 principal，请求体里没有也不能有 userId——
// 这是「改自己的密码」，不是「改别人的密码」。
func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	if p == nil {
		respondError(w, s.log, domain.ErrUnauthorized("missing access token"))
		return
	}
	var in changePasswordRequest
	if err := decodeJSON(r, &in); err != nil {
		respondError(w, s.log, err)
		return
	}
	if err := s.account.ChangePassword(r.Context(), p.UserID, in.Password, p); err != nil {
		respondError(w, s.log, err)
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"changed": true})
}

// positiveID 解析路由中的正整数参数。
func positiveID(r *http.Request, key, label string) (int64, error) {
	raw := chi.URLParam(r, key)
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, domain.ErrInvalid("%s must be a positive integer", label)
	}
	return id, nil
}
