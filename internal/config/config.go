package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Config 汇总进程级运行参数。取值优先级：环境变量 > 默认值。
type Config struct {
	// 运行期
	DataDir      string
	ListenAddr   string
	LogLevel     string
	LogFormat    string
	TrustedProxy string

	// 网络
	NativeMode  string // managed | host | ipvlan | macvlan
	Interface   string
	ListenPort  int
	MTU         int
	AddressCIDR string
	DNS         string

	// WireGuard 行为
	ReconcileInterval time.Duration
	// PolicyInterval 是配额与有效期判定周期。
	PolicyInterval time.Duration

	// 监控
	MetricsEnabled  bool
	SampleInterval  time.Duration
	RetentionRaw    time.Duration
	RetentionMin    time.Duration
	RetentionHour   time.Duration
	RetentionDay    time.Duration
	AuditRetention  time.Duration
	SampleBatchSize int

	// 安全
	MasterKey        string
	JWTSecret        string
	AdminUsername    string
	AdminPassword    string
	SessionTTL       time.Duration
	RefreshTTL       time.Duration
	LoginMaxAttempts int
	LoginLockWindow  time.Duration

	// Web
	WebAssetsDir string

	// CookieSecure 控制刷新令牌 Cookie 是否带 Secure 标记。
	// 默认部署是明文 HTTP，此时置 true 会导致浏览器直接丢弃 Cookie、刷新流程失效；
	// 仅在面板前置 TLS（Caddy/Nginx）或经 HTTPS 反代访问时才应开启。
	CookieSecure bool
}

// Default 返回全部字段的默认值。
func Default() Config {
	return Config{
		DataDir:           "/data",
		ListenAddr:        "0.0.0.0:8080",
		LogLevel:          "info",
		LogFormat:         "console",
		NativeMode:        "managed",
		Interface:         "wg0",
		ListenPort:        51820,
		MTU:               1420,
		AddressCIDR:       "10.8.0.1/24",
		DNS:               "1.1.1.1,8.8.8.8",
		ReconcileInterval: 2 * time.Second,
		PolicyInterval:    time.Minute,
		MetricsEnabled:    true,
		SampleInterval:    time.Second,
		RetentionRaw:      24 * time.Hour,
		RetentionMin:      7 * 24 * time.Hour,
		RetentionHour:     90 * 24 * time.Hour,
		RetentionDay:      730 * 24 * time.Hour,
		AuditRetention:    180 * 24 * time.Hour,
		SampleBatchSize:   200,
		AdminUsername:     "admin",
		SessionTTL:        15 * time.Minute,
		RefreshTTL:        30 * 24 * time.Hour,
		LoginMaxAttempts:  5,
		LoginLockWindow:   15 * time.Minute,
	}
}

// Load 依据环境变量覆盖默认值。
func Load() (Config, error) {
	c := Default()
	var err error

	envStr("WGP_DATA_DIR", &c.DataDir)
	envStr("WGP_LISTEN_ADDR", &c.ListenAddr)
	envStr("WGP_LOG_LEVEL", &c.LogLevel)
	envStr("WGP_LOG_FORMAT", &c.LogFormat)
	envStr("WGP_TRUSTED_PROXY", &c.TrustedProxy)

	envStr("WGP_NATIVE_MODE", &c.NativeMode)
	envStr("WGP_INTERFACE", &c.Interface)
	if err = envInt("WGP_LISTEN_PORT", &c.ListenPort); err != nil {
		return c, err
	}
	if err = envInt("WGP_MTU", &c.MTU); err != nil {
		return c, err
	}
	envStr("WGP_ADDRESS_CIDR", &c.AddressCIDR)
	envStr("WGP_DNS", &c.DNS)

	if err = envDur("WGP_RECONCILE_INTERVAL", &c.ReconcileInterval); err != nil {
		return c, err
	}
	if err = envDur("WGP_POLICY_INTERVAL", &c.PolicyInterval); err != nil {
		return c, err
	}

	if err = envBool("WGP_METRICS_ENABLED", &c.MetricsEnabled); err != nil {
		return c, err
	}
	if err = envDur("WGP_SAMPLE_INTERVAL", &c.SampleInterval); err != nil {
		return c, err
	}
	if err = envDur("WGP_RETENTION_RAW", &c.RetentionRaw); err != nil {
		return c, err
	}
	if err = envDur("WGP_RETENTION_MIN", &c.RetentionMin); err != nil {
		return c, err
	}
	if err = envDur("WGP_RETENTION_HOUR", &c.RetentionHour); err != nil {
		return c, err
	}
	if err = envDur("WGP_RETENTION_DAY", &c.RetentionDay); err != nil {
		return c, err
	}
	if err = envDur("WGP_AUDIT_RETENTION", &c.AuditRetention); err != nil {
		return c, err
	}
	if err = envInt("WGP_SAMPLE_BATCH", &c.SampleBatchSize); err != nil {
		return c, err
	}

	envStr("WGP_MASTER_KEY", &c.MasterKey)
	envStr("WGP_JWT_SECRET", &c.JWTSecret)
	envStr("WGP_ADMIN_USERNAME", &c.AdminUsername)
	envStr("WGP_ADMIN_PASSWORD", &c.AdminPassword)
	if err = envDur("WGP_SESSION_TTL", &c.SessionTTL); err != nil {
		return c, err
	}
	if err = envDur("WGP_REFRESH_TTL", &c.RefreshTTL); err != nil {
		return c, err
	}
	if err = envInt("WGP_LOGIN_MAX_ATTEMPTS", &c.LoginMaxAttempts); err != nil {
		return c, err
	}
	if err = envDur("WGP_LOGIN_LOCK_WINDOW", &c.LoginLockWindow); err != nil {
		return c, err
	}

	envStr("WGP_WEB_ASSETS_DIR", &c.WebAssetsDir)
	if err = envBool("WGP_COOKIE_SECURE", &c.CookieSecure); err != nil {
		return c, err
	}

	if err = c.Validate(); err != nil {
		return c, err
	}
	return c, nil
}

// Validate 校验配置合法性。
func (c Config) Validate() error {
	if c.DataDir == "" {
		return fmt.Errorf("config: data dir must not be empty")
	}
	if !filepath.IsAbs(c.DataDir) {
		return fmt.Errorf("config: data dir must be an absolute path, got %q", c.DataDir)
	}
	if c.ListenAddr == "" {
		return fmt.Errorf("config: listen addr must not be empty")
	}
	switch c.NativeMode {
	case "host", "ipvlan", "macvlan", "managed":
	default:
		// bridge 是被显式移除的历史模式，报错时给出迁移方向而不是一句裸报错。
		if strings.EqualFold(strings.TrimSpace(c.NativeMode), "bridge") {
			return fmt.Errorf("config: native mode %q 已移除（bridge 网络拿不到路由器 RA，IPv6 无法直连），请改用 host / ipvlan / macvlan", c.NativeMode)
		}
		return fmt.Errorf("config: native mode must be host, ipvlan, macvlan or managed, got %q", c.NativeMode)
	}
	if c.ListenPort < 1 || c.ListenPort > 65535 {
		return fmt.Errorf("config: listen port %d out of range", c.ListenPort)
	}
	if c.MTU < 1280 || c.MTU > 9000 {
		return fmt.Errorf("config: mtu %d out of range [1280, 9000]", c.MTU)
	}
	if _, _, err := parseCIDR(c.AddressCIDR); err != nil {
		return fmt.Errorf("config: invalid address cidr %q: %w", c.AddressCIDR, err)
	}
	if c.SampleInterval < 200*time.Millisecond {
		return fmt.Errorf("config: sample interval must be >= 200ms")
	}
	if c.SampleBatchSize < 1 {
		return fmt.Errorf("config: sample batch size must be >= 1")
	}
	if c.PolicyInterval < 10*time.Second {
		return fmt.Errorf("config: policy interval must be >= 10s")
	}
	return nil
}

// DBPath 返回 SQLite 文件路径。
func (c Config) DBPath() string { return filepath.Join(c.DataDir, "wgpanel.db") }

// MasterKeyPath 返回主密钥文件路径。
func (c Config) MasterKeyPath() string { return filepath.Join(c.DataDir, "master.key") }

// SecretPath 返回随机生成的运行时密钥持久化路径。
func (c Config) SecretPath() string { return filepath.Join(c.DataDir, "runtime.secret") }

// EnsureDataDir 创建并收紧数据目录权限。
func (c Config) EnsureDataDir() error {
	if err := os.MkdirAll(c.DataDir, 0o700); err != nil {
		return fmt.Errorf("config: create data dir: %w", err)
	}
	return nil
}

// DNSList 解析逗号分隔的 DNS 列表。
func (c Config) DNSList() []string {
	var out []string
	for _, s := range strings.Split(c.DNS, ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func envStr(key string, dst *string) {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		*dst = v
	}
}

func envInt(key string, dst *int) error {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fmt.Errorf("config: %s must be an integer: %w", key, err)
	}
	*dst = n
	return nil
}

func envBool(key string, dst *bool) error {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return fmt.Errorf("config: %s must be a boolean: %w", key, err)
	}
	*dst = b
	return nil
}

func envDur(key string, dst *time.Duration) error {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return fmt.Errorf("config: %s must be a duration: %w", key, err)
	}
	*dst = d
	return nil
}
