//go:build linux

package netdriver

import (
	"os"
	"path/filepath"
	"testing"
)

// withForwardingPath 把 forwardingPath 指向临时文件，测试结束后恢复。
func withForwardingPath(t *testing.T, p string) {
	t.Helper()
	old := forwardingPath
	forwardingPath = p
	t.Cleanup(func() { forwardingPath = old })
}

// TestForwardingEnabledReflectsValue 确认读取的是实际取值，而不是文件是否存在。
// "文件存在但值为 0" 必须判为未开启，否则探针会把关闭状态误报为可用。
func TestForwardingEnabledReflectsValue(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{"值为 1", "1", true},
		{"值为 1 且带换行", "1\n", true},
		{"值为 0", "0", false},
		{"空文件", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "ip_forward")
			if err := os.WriteFile(p, []byte(c.body), 0o644); err != nil {
				t.Fatal(err)
			}
			withForwardingPath(t, p)
			if got := forwardingEnabled(); got != c.want {
				t.Errorf("forwardingEnabled() = %v, want %v", got, c.want)
			}
		})
	}
}

// TestForwardingEnabledFalseWhenUnreadable 确认读不到开关时按"未开启"处理，
// 让调用方继续走写入路径，而不是直接判定不可用。
func TestForwardingEnabledFalseWhenUnreadable(t *testing.T) {
	withForwardingPath(t, filepath.Join(t.TempDir(), "does-not-exist"))
	if forwardingEnabled() {
		t.Error("开关不存在时 forwardingEnabled() 应为 false")
	}
}

// stubForwardingWrite 替换转发写入实现，记录调用次数并返回指定错误。
// 替换在测试结束时自动还原。
func stubForwardingWrite(t *testing.T, err error) *int {
	t.Helper()
	old := forwardingWrite
	calls := 0
	forwardingWrite = func(string) error {
		calls++
		return err
	}
	t.Cleanup(func() { forwardingWrite = old })
	return &calls
}

// TestEnsureForwardingSkipsWriteWhenAlreadyEnabled 复现真机上发现的缺陷：
// Docker 通过 --sysctl net.ipv4.ip_forward=1 建 netns，开关已是 1 且不可写。
// 正确行为是直接成功，而不是因为"写不进去"把已生效的转发报成故障。
func TestEnsureForwardingSkipsWriteWhenAlreadyEnabled(t *testing.T) {
	p := filepath.Join(t.TempDir(), "ip_forward")
	if err := os.WriteFile(p, []byte("1"), 0o444); err != nil {
		t.Fatal(err)
	}
	withForwardingPath(t, p)

	// 模拟只读挂载：写必然失败。
	writes := stubForwardingWrite(t, os.ErrPermission)

	if err := New().EnsureForwarding(); err != nil {
		t.Fatalf("转发已开启且不可写时不应报错，得到 %v", err)
	}
	if *writes != 0 {
		t.Errorf("转发已开启时不应尝试写入，实际写了 %d 次", *writes)
	}
}

// TestEnsureForwardingWritesWhenDisabled 确认关闭状态下确实会写入 1。
func TestEnsureForwardingWritesWhenDisabled(t *testing.T) {
	p := filepath.Join(t.TempDir(), "ip_forward")
	if err := os.WriteFile(p, []byte("0"), 0o644); err != nil {
		t.Fatal(err)
	}
	withForwardingPath(t, p)

	if err := New().EnsureForwarding(); err != nil {
		t.Fatalf("EnsureForwarding: %v", err)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "1" {
		t.Errorf("开关应为 1，得到 %q", b)
	}
}

// TestEnsureForwardingFailsWhenDisabledAndReadOnly 确认"未开启且不可写"是真实故障，
// 必须报错，不能静默继续。
func TestEnsureForwardingFailsWhenDisabledAndReadOnly(t *testing.T) {
	p := filepath.Join(t.TempDir(), "ip_forward")
	if err := os.WriteFile(p, []byte("0"), 0o444); err != nil {
		t.Fatal(err)
	}
	withForwardingPath(t, p)
	writes := stubForwardingWrite(t, os.ErrPermission)

	if err := New().EnsureForwarding(); err == nil {
		t.Error("未开启且不可写时应返回错误")
	}
	if *writes != 1 {
		t.Errorf("应尝试写入一次，实际 %d 次", *writes)
	}
}
