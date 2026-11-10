package httpapi

import (
	"net/http"

	"github.com/Rewind2026/wgpanel/internal/domain"
	"github.com/Rewind2026/wgpanel/internal/service"
)

type createUserRequest struct {
	Username string      `json:"username"`
	Email    string      `json:"email"`
	Password string      `json:"password"`
	Role     domain.Role `json:"role"`
}

type updateUserRequest struct {
	// Role 与 Enabled 用指针区分「未提供」与「显式置空」：
	// 部分更新时只改传了的字段，避免调用方为改一个字段重发全量。
	Role    *domain.Role `json:"role,omitempty"`
	Enabled *bool        `json:"enabled,omitempty"`
}

type resetPasswordRequest struct {
	Password string `json:"password"`
}

// handleListUsers 返回全部账号。
func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.users.List(r.Context())
	if err != nil {
		respondError(w, s.log, err)
		return
	}
	if users == nil {
		users = []domain.User{}
	}
	respondJSON(w, http.StatusOK, users)
}

// handleCreateUser 新建账号。
func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	var in createUserRequest
	if err := decodeJSON(r, &in); err != nil {
		respondError(w, s.log, err)
		return
	}
	u, err := s.users.Create(r.Context(), service.CreateUserRequest{
		Username: in.Username,
		Email:    in.Email,
		Password: in.Password,
		Role:     in.Role,
	}, p)
	if err != nil {
		respondError(w, s.log, err)
		return
	}
	respondJSON(w, http.StatusCreated, u)
}

// handleUpdateUser 调整账号的角色或启用状态。
//
// 两个字段同时变更时按顺序执行，每一步独立过校验；
// 任意一步失败都会留下已完成的那一步，部分成功可见于审计。
func (s *Server) handleUpdateUser(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	id, err := positiveID(r, "id", "user id")
	if err != nil {
		respondError(w, s.log, err)
		return
	}
	var in updateUserRequest
	if err := decodeJSON(r, &in); err != nil {
		respondError(w, s.log, err)
		return
	}
	if in.Role != nil {
		if err := s.users.SetRole(r.Context(), id, *in.Role, p); err != nil {
			respondError(w, s.log, err)
			return
		}
	}
	if in.Enabled != nil {
		if err := s.users.SetEnabled(r.Context(), id, *in.Enabled, p); err != nil {
			respondError(w, s.log, err)
			return
		}
	}
	if in.Role == nil && in.Enabled == nil {
		respondError(w, s.log, domain.ErrInvalid("nothing to update: provide role or enabled"))
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"updated": true})
}

// handleResetUserPassword 管理员重置目标账号的密码。
func (s *Server) handleResetUserPassword(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	id, err := positiveID(r, "id", "user id")
	if err != nil {
		respondError(w, s.log, err)
		return
	}
	var in resetPasswordRequest
	if err := decodeJSON(r, &in); err != nil {
		respondError(w, s.log, err)
		return
	}
	if err := s.users.ResetPassword(r.Context(), id, in.Password, p); err != nil {
		respondError(w, s.log, err)
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"reset": true})
}

// handleDeleteUser 删除账号。
func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	id, err := positiveID(r, "id", "user id")
	if err != nil {
		respondError(w, s.log, err)
		return
	}
	if err := s.users.Delete(r.Context(), id, p); err != nil {
		respondError(w, s.log, err)
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"deleted": true})
}
