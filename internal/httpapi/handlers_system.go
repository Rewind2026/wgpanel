package httpapi

import (
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Rewind2026/wgpanel/internal/authn"
	"github.com/Rewind2026/wgpanel/internal/domain"
	"github.com/Rewind2026/wgpanel/internal/netdriver"
)

// handleSystemNetwork 返回部署的网络形态诊断：
// 进程所处模式、各接口地址、上行接口与 IPv6 RA/SLAAC 链路状态。
//
// L2 模式（ipvlan/macvlan）的 IPv6 依赖上游路由器 RA 下发，链路上
// disable_ipv6 / accept_ra / forwarding 三颗内核开关缺一不可；
// 容器内 sysctl 可写时顺带自愈前两颗，只读（compose 未注入且 /proc/sys ro）
// 时原样报告问题，让界面给出「去改 compose」而不是无解的报错。
// host / managed（自管）模式的 RA 链路归宿主网络栈，EnsureRA 内部
// 只读返回（Applicable=false），不产生副作用。
func (s *Server) handleSystemNetwork(w http.ResponseWriter, r *http.Request) {
	mode := s.netMode
	uplink := netdriver.DefaultUplink()
	// EnsureRA 按 mode 分流：L2 自愈前两颗开关，host/managed 只读检查。
	ra := netdriver.EnsureRA(mode, uplink)
	respondJSON(w, http.StatusOK, map[string]any{
		"mode":       string(mode),
		"uplink":     uplink,
		"interfaces": netdriver.EnumerateInterfaces(),
		"ra":         ra,
		// 当前探测到的对外 Endpoint，与订阅/导出使用同一来源，便于对照排查。
		"endpoint": s.publicEndpoint(),
	})
}

// handleGetSettings 返回运行时设置。
func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.settings.LoadSettings(r.Context())
	if err != nil {
		respondError(w, s.log, err)
		return
	}
	respondJSON(w, http.StatusOK, cfg)
}

// handleUpdateSettings 保存运行时设置。
//
// 采样与调谐间隔保存后立即推给对应的后台循环，不重启进程：
// 这两项在界面上就是「改完立刻生效」的预期，只落库等于给用户一个
// 假的成功提示——设置里数字对了，采集节奏与调谐节奏没变。
func (s *Server) handleUpdateSettings(w http.ResponseWriter, r *http.Request) {
	in := domain.DefaultSettings()
	if err := decodeJSON(r, &in); err != nil {
		respondError(w, s.log, err)
		return
	}
	validated, err := in.Validate()
	if err != nil {
		respondError(w, s.log, err)
		return
	}
	validated.UpdatedAt = time.Now()
	if err := s.settings.SaveSettings(r.Context(), validated); err != nil {
		respondError(w, s.log, err)
		return
	}
	if s.collector != nil {
		s.collector.SetInterval(time.Duration(validated.SampleIntervalSeconds) * time.Second)
	}
	if s.setInterval != nil {
		s.setInterval(time.Duration(validated.ReconcileIntervalSeconds) * time.Second)
	}
	if s.audit != nil {
		_ = s.audit.Write(r.Context(), &domain.AuditEvent{
			ActorID: actorID(principalFrom(r.Context())), ActorName: actorName(principalFrom(r.Context())),
			Action: domain.AuditSettingsUpdate, Resource: "settings",
			Result: domain.AuditSuccess,
		})
	}
	respondJSON(w, http.StatusOK, validated)
}

// handleListBackups 返回备份索引。
func (s *Server) handleListBackups(w http.ResponseWriter, r *http.Request) {
	items, err := s.backups.List(r.Context())
	if err != nil {
		respondError(w, s.log, err)
		return
	}
	respondJSON(w, http.StatusOK, items)
}

// handleCreateBackup 立即生成一次备份。
func (s *Server) handleCreateBackup(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	var operator *int64
	if p != nil {
		operator = &p.UserID
	}
	rec, err := s.backups.Create(r.Context(), "manual", operator)
	if err != nil {
		respondError(w, s.log, err)
		return
	}
	if s.audit != nil {
		_ = s.audit.Write(r.Context(), &domain.AuditEvent{
			ActorID: actorID(p), ActorName: actorName(p), Action: domain.AuditBackupCreate,
			Resource: "backup", ResourceID: rec.Filename, Result: domain.AuditSuccess,
		})
	}
	s.publish(domain.Event{
		Type: domain.EventBackupCreated, Message: "备份已创建：" + rec.Filename,
	})
	respondJSON(w, http.StatusCreated, rec)
}

// handleDownloadBackup 下载备份文件。
func (s *Server) handleDownloadBackup(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "filename")
	path, err := s.backups.Path(r.Context(), name)
	if err != nil {
		respondError(w, s.log, err)
		return
	}
	f, err := os.Open(path)
	if err != nil {
		respondError(w, s.log, domain.ErrNotFound("backup file not found"))
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		respondError(w, s.log, domain.ErrInternal(err))
		return
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", "attachment; filename="+filepath.Base(path))
	http.ServeContent(w, r, filepath.Base(path), info.ModTime(), f)
}

// handleRestoreBackup 从备份恢复配置与身份数据。
func (s *Server) handleRestoreBackup(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "filename")
	if err := s.backups.Restore(r.Context(), name); err != nil {
		respondError(w, s.log, err)
		return
	}
	p := principalFrom(r.Context())
	if s.audit != nil {
		_ = s.audit.Write(r.Context(), &domain.AuditEvent{
			ActorID: actorID(p), ActorName: actorName(p), Action: domain.AuditRestoreApply,
			Resource: "backup", ResourceID: name, Result: domain.AuditSuccess,
		})
	}
	s.publish(domain.Event{Type: domain.EventRestoreApplied, Message: "配置已从备份恢复"})
	if s.reconcile != nil {
		s.reconcile()
	}
	respondJSON(w, http.StatusOK, map[string]any{
		"restored": name,
		// 不要在这里说「需要重新登录」：access token 每次鉴权都会重读 user 表，
		// JWT 签名密钥又不受恢复影响，所以发起恢复的这个会话在恢复后依然有效。
		// 真正的风险是反过来的——恢复会把备份之后重置的密码一并回退，
		// 管理员必须知道这件事。
		"notice": "配置已回滚到备份时刻。该备份之后重置的密码也已回退，请核对登录凭据",
	})
}

// handleDeleteBackup 删除备份文件与索引。
func (s *Server) handleDeleteBackup(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "filename")
	if err := s.backups.Delete(r.Context(), name); err != nil {
		respondError(w, s.log, err)
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"deleted": name})
}

// actorID 安全地取引用。
func actorID(p *authn.Principal) *int64 {
	if p == nil {
		return nil
	}
	return &p.UserID
}

// actorName 取用户名。
func actorName(p *authn.Principal) string {
	if p == nil {
		return ""
	}
	return p.Username
}
