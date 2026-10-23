package reqmeta_test

import (
	"context"
	"testing"

	"github.com/Rewind2026/wgpanel/internal/reqmeta"
)

func TestFromReturnsZeroOutsideRequest(t *testing.T) {
	got := reqmeta.From(context.Background())
	if got.IP != "" || got.UserAgent != "" {
		t.Fatalf("非 HTTP 上下文应返回零值，实际 %+v", got)
	}
}

func TestFromRoundTrip(t *testing.T) {
	want := reqmeta.Info{IP: "203.0.113.9", UserAgent: "curl/8.4.0"}
	got := reqmeta.From(reqmeta.With(context.Background(), want))
	if got != want {
		t.Fatalf("应原样取出，实际 %+v", got)
	}
}

// 派生 context 必须保留来源信息：service 层常用 context.WithTimeout 包装。
func TestInfoSurvivesDerivedContext(t *testing.T) {
	base := reqmeta.With(context.Background(), reqmeta.Info{IP: "198.51.100.4"})
	derived, cancel := context.WithCancel(base)
	defer cancel()
	if got := reqmeta.From(derived).IP; got != "198.51.100.4" {
		t.Fatalf("派生 context 应保留来源信息，实际 %q", got)
	}
}

// 补齐只填空位，不覆盖调用方已经填好的值。
func TestFillInOnlyFillsBlanks(t *testing.T) {
	ctx := reqmeta.With(context.Background(), reqmeta.Info{
		IP: "203.0.113.1", UserAgent: "Go-http-client/2.0",
	})

	ip, ua := "already-set", "already-set"
	reqmeta.FillIn(ctx, &ip, &ua)
	if ip != "already-set" || ua != "already-set" {
		t.Fatalf("已有值不应被覆盖，实际 ip=%q ua=%q", ip, ua)
	}

	emptyIP, emptyUA := "", ""
	reqmeta.FillIn(ctx, &emptyIP, &emptyUA)
	if emptyIP != "203.0.113.1" || emptyUA != "Go-http-client/2.0" {
		t.Fatalf("空位应被补齐，实际 ip=%q ua=%q", emptyIP, emptyUA)
	}
}

func TestFillInNoOpWithoutHTTPContext(t *testing.T) {
	ip, ua := "", ""
	reqmeta.FillIn(context.Background(), &ip, &ua)
	if ip != "" || ua != "" {
		t.Fatalf("无请求上下文时应保持空，实际 ip=%q ua=%q", ip, ua)
	}
}
