package domain

import "time"

// ServerConfig 描述服务端 WireGuard 接口的完整可配置参数。
// 字段与 wgctrl.Config 一一对应，确保 UI 可暴露全量参数。
type ServerConfig struct {
	// 身份
	Interface  string `json:"interface"`
	ListenPort int    `json:"listenPort"`

	// 密钥（写入 API 时 privateKey 为空表示不修改）
	PrivateKey   string `json:"privateKey,omitempty"`
	PublicKey    string `json:"publicKey,omitempty"`
	PresharedKey string `json:"presharedKey,omitempty"`

	// 网络
	AddressCIDR string `json:"addressCidr"`
	MTU         int    `json:"mtu"`
	FwMark      int    `json:"fwmark"`
	Table       string `json:"table"` // auto | off | 表名
	AllowedIPs  string `json:"allowedIps"`
	ListenIPv4  string `json:"listenIpv4,omitempty"`
	ListenIPv6  string `json:"listenIpv6,omitempty"`

	// 行为
	ReplacePeers bool   `json:"replacePeers"`
	SaveConfig   bool   `json:"saveConfig"`
	EnableIPv6   bool   `json:"enableIpv6"`
	DNS          string `json:"dns"`

	PersistentKeepalive int `json:"persistentKeepalive"`

	// 元数据
	Revision  int64     `json:"revision"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Validate 校验服务端配置。normalize 由调用方先行执行。
func (s *ServerConfig) Validate() error {
	if s.Interface == "" {
		return ErrInvalid("interface must not be empty")
	}
	if len(s.Interface) > 15 { // Linux IFNAMSIZ - 1
		return ErrInvalid("interface name too long (max 15)")
	}
	if s.ListenPort < 0 || s.ListenPort > 65535 {
		return ErrInvalid("listenPort out of range")
	}
	if s.MTU < 1280 || s.MTU > 9000 {
		return ErrInvalid("mtu out of range [1280, 9000]")
	}
	if s.AddressCIDR == "" {
		return ErrInvalid("addressCidr must not be empty")
	}
	if s.FwMark < 0 || s.FwMark > 0xFFFFFFFF {
		return ErrInvalid("fwmark out of range")
	}
	if s.PersistentKeepalive < 0 || s.PersistentKeepalive > 65535 {
		return ErrInvalid("persistentKeepalive out of range")
	}
	return nil
}

// Normalize 归一化字符串类字段。
func (s *ServerConfig) Normalize() {
	if s.Interface == "" {
		s.Interface = "wg0"
	}
	if s.Table == "" {
		s.Table = "auto"
	}
	if s.AllowedIPs == "" {
		s.AllowedIPs = "0.0.0.0/0"
	}
}
