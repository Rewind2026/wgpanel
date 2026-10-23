package store

import (
	"context"
	"testing"

	"github.com/Rewind2026/wgpanel/internal/domain"
	"github.com/Rewind2026/wgpanel/internal/reqmeta"
)

func newAuditFixture(t *testing.T) *Store {
	t.Helper()
	st, err := OpenInMemory()
	if err != nil {
		t.Fatalf("open in-memory store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func readLastAudit(t *testing.T, st *Store) domain.AuditEvent {
	t.Helper()
	evs, _, err := st.AuditRepo().Query(context.Background(), domain.AuditQuery{})
	if err != nil {
		t.Fatalf("query audit: %v", err)
	}
	if len(evs) == 0 {
		t.Fatal("审计表为空")
	}
	return evs[len(evs)-1]
}

// HTTP 请求来源必须自动落到审计记录里。
//
// 真机上暴露的缺陷：ip 与 ua 两列全为空。原因是 AuditEvent 在 service 层
// 构造，而 *http.Request 只存在于 handler 层，写入时没有任何通道把来源
// 带过去——于是 287 条登录成功记录全是空，42 条登录失败却因为 handler
// 显式传了参数而有值。
func TestWritePicksUpRequestSourceFromContext(t *testing.T) {
	st := newAuditFixture(t)
	ctx := reqmeta.With(context.Background(), reqmeta.Info{
		IP:        "192.168.50.42",
		UserAgent: "Mozilla/5.0 (X11; Linux x86_64)",
	})

	if err := st.AuditRepo().Write(ctx, &domain.AuditEvent{
		ActorName: "ops", Action: domain.AuditPeerCreate, Resource: "peer",
	}); err != nil {
		t.Fatalf("write audit: %v", err)
	}

	got := readLastAudit(t, st)
	if got.IP != "192.168.50.42" {
		t.Errorf("IP 应自动补齐，实际 %q", got.IP)
	}
	if got.UserAgent != "Mozilla/5.0 (X11; Linux x86_64)" {
		t.Errorf("User-Agent 应自动补齐，实际 %q", got.UserAgent)
	}
}

// 调用方已填好的值不能被上下文覆盖：登录限流拿到的来源更权威。
func TestWriteKeepsExplicitSource(t *testing.T) {
	st := newAuditFixture(t)
	ctx := reqmeta.With(context.Background(), reqmeta.Info{
		IP:        "10.0.0.1",
		UserAgent: "from-context",
	})

	if err := st.AuditRepo().Write(ctx, &domain.AuditEvent{
		ActorName: "ops", Action: domain.AuditLoginFailed, Resource: "user",
		IP: "203.0.113.7", UserAgent: "from-caller",
	}); err != nil {
		t.Fatalf("write audit: %v", err)
	}

	got := readLastAudit(t, st)
	if got.IP != "203.0.113.7" || got.UserAgent != "from-caller" {
		t.Fatalf("显式值不应被覆盖，实际 ip=%q ua=%q", got.IP, got.UserAgent)
	}
}

// 定时任务、策略引擎这类非 HTTP 来源补不上，字段应保持空而不是写入垃圾。
func TestWriteLeavesSourceEmptyWithoutRequest(t *testing.T) {
	st := newAuditFixture(t)

	if err := st.AuditRepo().Write(context.Background(), &domain.AuditEvent{
		ActorName: "system", Action: domain.AuditPeerDelete, Resource: "peer",
	}); err != nil {
		t.Fatalf("write audit: %v", err)
	}

	got := readLastAudit(t, st)
	if got.IP != "" || got.UserAgent != "" {
		t.Fatalf("非请求来源应保持空，实际 ip=%q ua=%q", got.IP, got.UserAgent)
	}
}
