package domain

import "time"

// AuditAction 定义审计动作常量。
type AuditAction string

const (
	AuditLogin          AuditAction = "auth.login"
	AuditLoginFailed    AuditAction = "auth.login_failed"
	AuditLogout         AuditAction = "auth.logout"
	AuditServerUpdate   AuditAction = "server.update"
	AuditServerResetKey AuditAction = "server.reset_key"
	AuditPeerCreate     AuditAction = "peer.create"
	AuditPeerUpdate     AuditAction = "peer.update"
	AuditPeerDelete     AuditAction = "peer.delete"
	AuditPeerRotateKey  AuditAction = "peer.rotate_key"
	AuditPeerBulkRevoke AuditAction = "peer.bulk_revoke"
	AuditPeerImport     AuditAction = "peer.import"
	AuditConfigExport   AuditAction = "config.export"
	// AuditPasswordChange 记录当前账号自助修改密码。
	AuditPasswordChange AuditAction = "user.password_change"
	AuditSettingsUpdate AuditAction = "settings.update"
	AuditBackupCreate   AuditAction = "backup.create"
	AuditRestoreApply   AuditAction = "restore.apply"
)

// AuditResult 表示审计结果。
type AuditResult string

const (
	AuditSuccess AuditResult = "success"
	AuditFailure AuditResult = "failure"
)

// AuditEvent 是一条审计记录。
type AuditEvent struct {
	ID         int64       `json:"id"`
	ActorID    *int64      `json:"actorId,omitempty"`
	ActorName  string      `json:"actorName"`
	Action     AuditAction `json:"action"`
	Resource   string      `json:"resource,omitempty"`
	ResourceID string      `json:"resourceId,omitempty"`
	Detail     string      `json:"detail,omitempty"`
	IP         string      `json:"ip,omitempty"`
	UserAgent  string      `json:"userAgent,omitempty"`
	Result     AuditResult `json:"result"`
	CreatedAt  time.Time   `json:"createdAt"`
}

// AuditQuery 是审计检索条件。
type AuditQuery struct {
	Action   string
	ActorID  *int64
	Resource string
	Result   string
	From     *time.Time
	To       *time.Time
	Page     int
	PageSize int
}
