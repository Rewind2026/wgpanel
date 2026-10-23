package domain

import "time"

// SuspensionReason 说明 Peer 被自动停用的原因。
//
// 单独记录原因而不是直接改 enabled，是为了让「周期重置后自动恢复」成为可能：
// 人工停用的 Peer 不应被策略引擎悄悄打开。
type SuspensionReason string

const (
	// SuspendedNone 表示没有自动停用记录。
	SuspendedNone SuspensionReason = ""
	// SuspendedQuota 表示超出流量配额被停用。
	SuspendedQuota SuspensionReason = "quota"
	// SuspendedExpired 表示超过有效期被停用。
	SuspendedExpired SuspensionReason = "expired"
)

// EventType 是实时事件类型，通过 SSE 推送给前端。
type EventType string

const (
	// Peer 生命周期事件。这些是运维真正需要被告知的变更：
	// 上下线事件每几秒就可能触发一次，噪声太大，不能和它们混在一起。
	EventPeerCreated   EventType = "peer.created"
	EventPeerUpdated   EventType = "peer.updated"
	EventPeerDeleted   EventType = "peer.deleted"
	EventPeerRotated   EventType = "peer.key_rotated"
	EventPeerOnline    EventType = "peer.online"
	EventPeerOffline   EventType = "peer.offline"
	EventPeerQuota     EventType = "peer.quota_exceeded"
	EventPeerExpiring  EventType = "peer.expiring"
	EventPeerExpired   EventType = "peer.expired"
	EventPeerRestored  EventType = "peer.restored"
	EventServerUpdated EventType = "server.updated"
	EventBackupCreated EventType = "backup.created"
	// EventRestoreApplied 表示配置已从备份回滚。
	EventRestoreApplied EventType = "restore.applied"
)

// AllEventTypes 列出全部事件类型，用于接口自述与前端事件选择器。
//
// 手工维护容易漏，所以新增事件类型时必须同步这里——
// 漏了的后果是「文档和选择器里没有这个事件」，比报错更难发现。
func AllEventTypes() []EventType {
	return []EventType{
		EventPeerCreated, EventPeerUpdated, EventPeerDeleted, EventPeerRotated,
		EventPeerOnline, EventPeerOffline, EventPeerQuota, EventPeerExpiring,
		EventPeerExpired, EventPeerRestored,
		EventServerUpdated, EventBackupCreated, EventRestoreApplied,
	}
}

// Event 是一条实时事件。SSE 推送与策略引擎共用同一结构，避免两套事件语义。
type Event struct {
	Type       EventType      `json:"type"`
	PeerID     int64          `json:"peerId,omitempty"`
	PeerName   string         `json:"peerName,omitempty"`
	Message    string         `json:"message"`
	OccurredAt time.Time      `json:"occurredAt"`
	Data       map[string]any `json:"data,omitempty"`
}

// BackupRecord 描述一次备份。
type BackupRecord struct {
	ID            int64     `json:"id"`
	Filename      string    `json:"filename"`
	SizeBytes     int64     `json:"sizeBytes"`
	SHA256        string    `json:"sha256"`
	SchemaVersion int       `json:"schemaVersion"`
	PeerCount     int       `json:"peerCount"`
	UserCount     int       `json:"userCount"`
	Kind          string    `json:"kind"`
	OperatorID    *int64    `json:"operatorId,omitempty"`
	CreatedAt     time.Time `json:"createdAt"`
}

// ImportRowError 是一次批量导入中单行的失败原因。
//
// 行号从 2 开始：第 1 行是表头。用户需要的是「CSV 第几行错了」，
// 而不是「第几个数据条目错了」，两者在有注释行或空行时并不相同。
type ImportRowError struct {
	Line   int    `json:"line"`
	Name   string `json:"name,omitempty"`
	Reason string `json:"reason"`
}

// ImportResult 是一次批量导入的结果。
type ImportResult struct {
	Created int              `json:"created"`
	Skipped int              `json:"skipped"`
	Failed  int              `json:"failed"`
	Errors  []ImportRowError `json:"errors"`
}

// SubscriptionLink 描述一个客户端的订阅入口。
type SubscriptionLink struct {
	PeerID   int64  `json:"peerId"`
	Token    string `json:"token"`
	URL      string `json:"url"`
	IssuedAt string `json:"issuedAt"`
}
