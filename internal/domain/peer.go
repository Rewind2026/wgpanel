package domain

import "time"

// QuotaPeriod 表示流量配额的结算周期。
type QuotaPeriod string

const (
	QuotaPeriodDaily   QuotaPeriod = "daily"
	QuotaPeriodWeekly  QuotaPeriod = "weekly"
	QuotaPeriodMonthly QuotaPeriod = "monthly"
	QuotaPeriodNever   QuotaPeriod = "never"
)

// Valid 判断配额周期是否合法。
func (p QuotaPeriod) Valid() bool {
	switch p {
	case QuotaPeriodDaily, QuotaPeriodWeekly, QuotaPeriodMonthly, QuotaPeriodNever:
		return true
	}
	return false
}

// Peer 描述一个客户端。
type Peer struct {
	ID           int64  `json:"id"`
	Name         string `json:"name"`
	PublicKey    string `json:"publicKey"`
	PrivateKey   string `json:"privateKey,omitempty"`
	PresharedKey string `json:"presharedKey,omitempty"`

	Address string `json:"address"` // 10.8.0.2/32
	// AllowedIPs 是客户端侧语义：哪些流量走隧道，默认 0.0.0.0/0,::/0。
	// 它不会原样下发到服务端 [Peer]：WireGuard 内核要求服务端的 AllowedIPs
	// 在所有 peer 之间唯一，重复的网段只会被最后一个 peer 保留，其余变成
	// (none)，导致握手成功但流量被静默丢弃。服务端实际使用 Address。
	AllowedIPs          string `json:"allowedIps"`
	Endpoint            string `json:"endpoint,omitempty"`
	PersistentKeepalive int    `json:"persistentKeepalive"`

	Note string   `json:"note,omitempty"`
	Tags []string `json:"tags"`

	// 策略
	TrafficQuotaBytes int64       `json:"trafficQuotaBytes"` // 0 = 不限
	QuotaPeriod       QuotaPeriod `json:"quotaPeriod"`
	RateLimitMbps     int         `json:"rateLimitMbps"` // 0 = 不限
	Enabled           bool        `json:"enabled"`
	ExpiresAt         *time.Time  `json:"expiresAt,omitempty"`

	// MaxConnections 是历史遗留列，保留只为兼容既有数据库与 CSV 列序，
	// 运行时没有任何执行点：WireGuard 内核给每个 Peer 只保留一个 endpoint
	// 槽位（roaming 也只是替换那一个地址），协议层不存在「同时连接数」。
	// 面板此前把它渲染成一个输入框，并写着「超出后策略会终止超额连接」，
	// 而策略引擎里没有任何代码读它——用户填了、存了、导出了，唯独不生效。
	// UI 已移除该控件；新写入一律归零，避免历史值继续误导。
	MaxConnections int `json:"maxConnections"`

	// QuotaUsedBytes 是当前配额周期内已用流量，由采集器累加、周期切换时清零。
	QuotaUsedBytes int64 `json:"quotaUsedBytes"`
	// SuspendedBy 记录「是谁」停用了 Peer：策略引擎还是人工。
	// 人工停用（""）不会被周期重置自动恢复。
	SuspendedBy SuspensionReason `json:"suspendedBy,omitempty"`

	// SubscriptionToken 是订阅链接凭据，仅在签发接口返回，不出现在列表响应中。
	SubscriptionToken string `json:"-"`

	LastSeenAt *time.Time `json:"lastSeenAt,omitempty"`
	CreatedAt  time.Time  `json:"createdAt"`
	UpdatedAt  time.Time  `json:"updatedAt"`

	// Stats 为运行态字段，不落库。
	Stats *PeerStats `json:"stats,omitempty"`
}

// Validate 校验 Peer 配置。
func (p *Peer) Validate() error {
	if p.Name == "" {
		return ErrInvalid("name must not be empty")
	}
	if len(p.Name) > 64 {
		return ErrInvalid("name too long (max 64)")
	}
	if p.PublicKey == "" {
		return ErrInvalid("publicKey must not be empty")
	}
	if p.Address == "" {
		return ErrInvalid("address must not be empty")
	}
	if p.AllowedIPs == "" {
		p.AllowedIPs = "0.0.0.0/0,::/0"
	}
	if p.PersistentKeepalive < 0 || p.PersistentKeepalive > 65535 {
		return ErrInvalid("persistentKeepalive out of range")
	}
	if p.TrafficQuotaBytes < 0 {
		return ErrInvalid("trafficQuotaBytes must not be negative")
	}
	if p.RateLimitMbps < 0 {
		return ErrInvalid("rateLimitMbps must not be negative")
	}
	if p.MaxConnections < 0 {
		return ErrInvalid("maxConnections must not be negative")
	}
	if p.QuotaPeriod == "" {
		p.QuotaPeriod = QuotaPeriodMonthly
	}
	if !p.QuotaPeriod.Valid() {
		return ErrInvalid("invalid quotaPeriod")
	}
	return nil
}

// Normalize 归一化默认值。
func (p *Peer) Normalize() {
	if p.AllowedIPs == "" {
		p.AllowedIPs = "0.0.0.0/0,::/0"
	}
	if p.PersistentKeepalive == 0 {
		p.PersistentKeepalive = 25
	}
	if p.QuotaPeriod == "" {
		p.QuotaPeriod = QuotaPeriodMonthly
	}
	if p.Tags == nil {
		p.Tags = []string{}
	}
}

// Expired 判断 Peer 是否已过期。
func (p *Peer) Expired(now time.Time) bool {
	return p.ExpiresAt != nil && p.ExpiresAt.Before(now)
}

// Active 判断 Peer 当前是否应处于连接态。
func (p *Peer) Active(now time.Time) bool {
	return p.Enabled && !p.Expired(now)
}
