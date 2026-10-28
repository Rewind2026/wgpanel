package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 配置校验是容器启动的第一道闸门：一个非法的 WGP_MTU 会让容器直接退出，
// 而 Docker 默认只打印一行 exit code，现场很难判断是哪个变量写错了。
// 这组用例把边界值钉死，保证报错发生在启动早期且信息可定位。

func TestValidateAcceptsDefaults(t *testing.T) {
	if err := Default().Validate(); err != nil {
		t.Fatalf("默认配置必须合法: %v", err)
	}
}

// 四种部署形态都是合法配置：compose 文件按模式硬编码 WGP_NATIVE_MODE，
// systemd/K8s 清单声明 managed；校验一旦拒绝某一种，对应部署会以 exit 1
// 启动失败而不是降级运行。
func TestValidateAcceptsAllNetworkModes(t *testing.T) {
	for _, m := range []string{"host", "ipvlan", "macvlan", "managed"} {
		c := Default()
		c.NativeMode = m
		if err := c.Validate(); err != nil {
			t.Errorf("NativeMode=%q 应为合法模式: %v", m, err)
		}
	}
}

// bridge 部署模式已被移除：必须报错并给出迁移提示，而不是静默降级。
func TestValidateRejectsRemovedBridgeMode(t *testing.T) {
	c := Default()
	c.NativeMode = "bridge"
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "已移除") {
		t.Errorf("NativeMode=bridge 应报「已移除」错误，得到 %v", err)
	}
}

func TestValidateRejectsBadValues(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Config)
	}{
		{"空数据目录", func(c *Config) { c.DataDir = "" }},
		{"相对数据目录", func(c *Config) { c.DataDir = "data" }},
		{"空监听地址", func(c *Config) { c.ListenAddr = "" }},
		{"未知原生模式", func(c *Config) { c.NativeMode = "gre" }},
		{"端口为 0", func(c *Config) { c.ListenPort = 0 }},
		{"端口越界", func(c *Config) { c.ListenPort = 70000 }},
		{"MTU 过小", func(c *Config) { c.MTU = 1200 }},
		{"MTU 过大", func(c *Config) { c.MTU = 9001 }},
		{"非法 CIDR", func(c *Config) { c.AddressCIDR = "10.8.0.1" }},
		{"采样间隔过小", func(c *Config) { c.SampleInterval = 100 * time.Millisecond }},
		{"批量大小为 0", func(c *Config) { c.SampleBatchSize = 0 }},
		{"策略周期过短", func(c *Config) { c.PolicyInterval = time.Second }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := Default()
			tc.mutate(&c)
			if err := c.Validate(); err == nil {
				t.Fatal("期望校验失败，实际通过")
			}
		})
	}
}

// MTU 1280 是 IPv6 最小 MTU，1420 是 WireGuard 在常见 PPPoE 下的取值，
// 两端都必须被接受——把 1280 判为非法会让 IPv6-only 部署起不来。
func TestValidateMTUBoundaries(t *testing.T) {
	for _, mtu := range []int{1280, 1420, 9000} {
		c := Default()
		c.MTU = mtu
		if err := c.Validate(); err != nil {
			t.Errorf("MTU %d 应被接受: %v", mtu, err)
		}
	}
}

func TestValidateSampleIntervalBoundary(t *testing.T) {
	c := Default()
	c.SampleInterval = 200 * time.Millisecond
	if err := c.Validate(); err != nil {
		t.Fatalf("200ms 采样间隔应被接受: %v", err)
	}
}

func TestDNSListTrimsAndSkipsEmpty(t *testing.T) {
	c := Default()
	c.DNS = " 1.1.1.1 , ,8.8.8.8,  "
	got := c.DNSList()
	want := []string{"1.1.1.1", "8.8.8.8"}
	if len(got) != len(want) {
		t.Fatalf("DNSList() = %v，期望 %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("DNSList()[%d] = %q，期望 %q", i, got[i], want[i])
		}
	}
}

func TestDataDirPathsAreInsideDataDir(t *testing.T) {
	c := Default()
	c.DataDir = "/data"
	for name, got := range map[string]string{
		"db":     c.DBPath(),
		"master": c.MasterKeyPath(),
		"secret": c.SecretPath(),
	} {
		if filepath.Dir(got) != "/data" {
			t.Errorf("%s 路径 %q 不在数据目录内", name, got)
		}
	}
	if c.DBPath() == c.MasterKeyPath() {
		t.Error("数据库与主密钥不能是同一个文件")
	}
}

func TestEnsureDataDirCreatesWithRestrictivePerm(t *testing.T) {
	base := t.TempDir()
	c := Default()
	c.DataDir = filepath.Join(base, "nested", "data")

	if err := c.EnsureDataDir(); err != nil {
		t.Fatalf("EnsureDataDir: %v", err)
	}
	info, err := os.Stat(c.DataDir)
	if err != nil {
		t.Fatalf("数据目录未创建: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("数据目录权限 %o，应为 700（内含全部私钥）", perm)
	}
	// 重复调用必须幂等。
	if err := c.EnsureDataDir(); err != nil {
		t.Fatalf("重复 EnsureDataDir 应成功: %v", err)
	}
}

// Load 必须把环境变量真正吃进去，而不是只读默认值。
func TestLoadReadsEnvironment(t *testing.T) {
	t.Setenv("WGP_LISTEN_PORT", "51821")
	t.Setenv("WGP_MTU", "1300")
	t.Setenv("WGP_RECONCILE_INTERVAL", "5s")
	t.Setenv("WGP_COOKIE_SECURE", "true")
	t.Setenv("WGP_DATA_DIR", t.TempDir())

	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.ListenPort != 51821 {
		t.Errorf("ListenPort = %d，期望 51821", c.ListenPort)
	}
	if c.MTU != 1300 {
		t.Errorf("MTU = %d，期望 1300", c.MTU)
	}
	if c.ReconcileInterval != 5*time.Second {
		t.Errorf("ReconcileInterval = %v，期望 5s", c.ReconcileInterval)
	}
	if !c.CookieSecure {
		t.Error("CookieSecure 应为 true")
	}
	// 未设置的环境变量必须保留默认值，而不是被清零。
	if c.NativeMode != "managed" {
		t.Errorf("NativeMode = %q，应保留默认值 managed", c.NativeMode)
	}
}

// 解析失败必须报错而不是静默忽略：忽略会让面板带着用户没预期的参数启动。
func TestLoadRejectsMalformedEnvironment(t *testing.T) {
	t.Setenv("WGP_DATA_DIR", t.TempDir())

	cases := map[string]struct{ key, val string }{
		"端口非整数":   {"WGP_LISTEN_PORT", "abc"},
		"布尔值非法":   {"WGP_COOKIE_SECURE", "yes-please"},
		"时长格式错":   {"WGP_RECONCILE_INTERVAL", "5 seconds"},
		"端口越界":    {"WGP_LISTEN_PORT", "70000"},
		"MTU 越界":  {"WGP_MTU", "500"},
		"CIDR 非法": {"WGP_ADDRESS_CIDR", "10.8.0.1"},
		"采样间隔过小":  {"WGP_SAMPLE_INTERVAL", "50ms"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Setenv(tc.key, tc.val)
			if _, err := Load(); err == nil {
				t.Fatalf("%s=%s 应导致启动失败", tc.key, tc.val)
			}
		})
	}
}

// 空字符串视作「未设置」，这样 docker-compose 里写空值不会把配置搞坏。
func TestEnvEmptyValueKeepsDefault(t *testing.T) {
	t.Setenv("WGP_DATA_DIR", t.TempDir())
	t.Setenv("WGP_LISTEN_PORT", "")

	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.ListenPort != Default().ListenPort {
		t.Errorf("空值应保留默认端口 %d，实际 %d", Default().ListenPort, c.ListenPort)
	}
}

func TestParseCIDR(t *testing.T) {
	p, addr, err := parseCIDR("10.8.0.1/24")
	if err != nil {
		t.Fatalf("合法 CIDR 应解析成功: %v", err)
	}
	if addr.String() != "10.8.0.1" {
		t.Errorf("地址 = %q，期望 10.8.0.1", addr)
	}
	if p.Bits() != 24 {
		t.Errorf("掩码 = %d，期望 24", p.Bits())
	}
	for _, bad := range []string{"", "10.8.0.1", "not/a/cidr", "10.8.0.1/33"} {
		if _, _, err := parseCIDR(bad); err == nil {
			t.Errorf("%q 应解析失败", bad)
		}
	}
}
