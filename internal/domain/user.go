package domain

import (
	"strings"
	"time"
)

// Role 定义系统角色。
type Role string

const (
	RoleOwner    Role = "owner"
	RoleAdmin    Role = "admin"
	RoleOperator Role = "operator"
	RoleViewer   Role = "viewer"
)

// Valid 判断角色是否合法。
func (r Role) Valid() bool {
	switch r {
	case RoleOwner, RoleAdmin, RoleOperator, RoleViewer:
		return true
	}
	return false
}

// AllRoles 返回全部角色（按权限从高到低）。
func AllRoles() []Role { return []Role{RoleOwner, RoleAdmin, RoleOperator, RoleViewer} }

// Permission 权限点，命名 resource:action。
type Permission string

const (
	PermServerRead     Permission = "server:read"
	PermServerWrite    Permission = "server:write"
	PermPeerRead       Permission = "peer:read"
	PermPeerCreate     Permission = "peer:create"
	PermPeerUpdate     Permission = "peer:update"
	PermPeerDelete     Permission = "peer:delete"
	PermPeerRevoke     Permission = "peer:revoke"
	PermMetricsRead    Permission = "metrics:read"
	PermConfigExport   Permission = "config:export"
	PermConfigImport   Permission = "config:import"
	PermAuditRead      Permission = "audit:read"
	PermSettingsWrite  Permission = "settings:write"
	PermSystemShutdown Permission = "system:shutdown"
	PermUserManage     Permission = "user:manage"
)

// rolePermissions 是权限矩阵的唯一事实来源。
var rolePermissions = map[Role][]Permission{
	RoleOwner: {
		PermServerRead, PermServerWrite,
		PermPeerRead, PermPeerCreate, PermPeerUpdate, PermPeerDelete, PermPeerRevoke,
		PermMetricsRead,
		PermConfigExport, PermConfigImport,
		PermAuditRead,
		PermSettingsWrite, PermSystemShutdown,
		PermUserManage,
	},
	RoleAdmin: {
		PermServerRead, PermServerWrite,
		PermPeerRead, PermPeerCreate, PermPeerUpdate, PermPeerDelete, PermPeerRevoke,
		PermMetricsRead,
		PermConfigExport, PermConfigImport,
		PermAuditRead,
		PermSettingsWrite, PermSystemShutdown,
		PermUserManage,
	},
	RoleOperator: {
		PermServerRead, PermServerWrite,
		PermPeerRead, PermPeerCreate, PermPeerUpdate, PermPeerDelete, PermPeerRevoke,
		PermMetricsRead,
		PermConfigExport,
	},
	RoleViewer: {
		PermServerRead, PermPeerRead, PermMetricsRead,
	},
}

// Permissions 返回角色的权限集合。
func (r Role) Permissions() []Permission { return rolePermissions[r] }

// CanonicalUsername 返回用户名的规范形式：去首尾空白 + 折叠 ASCII 大小写。
//
// 用户名不区分大小写：Admin 与 admin 指向同一个账号。
// 否则两者可以并存，审计日志里会出现两条肉眼分不出来的记录，
// 「谁改了配置」这类问题也就永远说不清。
//
// 只折叠 ASCII，与 SQLite 的 lower() 语义一致：
// Go 侧算出的「相等」必须与库里查得到的那一行是同一个判定，
// 两边口径不一致时，限流桶与查重结果会对不上。
//
// 这是用户名的唯一归一入口：建号、登录、限流键、落库共用同一份口径。
func CanonicalUsername(s string) string {
	s = strings.TrimSpace(s)
	b := []byte(s)
	for i := range b {
		if b[i] >= 'A' && b[i] <= 'Z' {
			b[i] += 'a' - 'A'
		}
	}
	return string(b)
}

// Has 判断角色是否拥有指定权限。
func (r Role) Has(p Permission) bool {
	for _, item := range rolePermissions[r] {
		if item == p {
			return true
		}
	}
	return false
}

// User 表示一个管理端用户。
type User struct {
	ID           int64      `json:"id"`
	Username     string     `json:"username"`
	Email        string     `json:"email,omitempty"`
	PasswordHash string     `json:"-"`
	Role         Role       `json:"role"`
	Enabled      bool       `json:"enabled"`
	LastLoginAt  *time.Time `json:"lastLoginAt,omitempty"`
	CreatedAt    time.Time  `json:"createdAt"`
	UpdatedAt    time.Time  `json:"updatedAt"`
	// TokenVersion 是会话代号，用于作废已签发的 access token。
	// 改密、停用或强制下线时提升它，此前签发的 JWT 立即全部失效。
	// 不对外暴露：它不是用户资料，只是内部失效标记。
	TokenVersion int64 `json:"-"`
}

// Session 表示一次登录会话。
type Session struct {
	AccessToken  string    `json:"accessToken"`
	RefreshToken string    `json:"refreshToken"`
	ExpiresIn    int64     `json:"expiresIn"`
	User         *User     `json:"user"`
	IssuedAt     time.Time `json:"issuedAt"`
}
