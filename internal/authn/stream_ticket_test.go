package authn

import (
	"testing"
	"time"

	"github.com/Rewind2026/wgpanel/internal/domain"
)

// SSE 票据是浏览器唯一的鉴权通道，一旦失效语义出错，
// 表现是「监控页一直连不上」而没有任何报错。

func TestStreamTicketRedeemCarriesPrincipal(t *testing.T) {
	s := NewStreamTicketStore()
	want := Principal{UserID: 7, Username: "ops", Role: domain.RoleAdmin}

	raw, err := s.Issue(want)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	got, ok := s.Redeem(raw)
	if !ok {
		t.Fatal("刚签发的票据必须能兑换")
	}
	if got.UserID != want.UserID || got.Username != want.Username || got.Role != want.Role {
		t.Fatalf("票据未还原身份: got %+v want %+v", got, want)
	}
}

func TestStreamTicketIsSingleUse(t *testing.T) {
	s := NewStreamTicketStore()
	raw, err := s.Issue(Principal{UserID: 1, Role: domain.RoleOwner})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if _, ok := s.Redeem(raw); !ok {
		t.Fatal("首次兑换应成功")
	}
	// 一次性是这道防线存在的全部理由：票据会出现在反向代理
	// 访问日志里，可重放就等于把监控数据长期挂在日志上等人捡。
	if _, ok := s.Redeem(raw); ok {
		t.Fatal("票据已消费，第二次兑换必须失败")
	}
}

func TestStreamTicketExpires(t *testing.T) {
	s := NewStreamTicketStore()
	now := time.Now()
	s.now = func() time.Time { return now }

	raw, err := s.Issue(Principal{UserID: 1, Role: domain.RoleViewer})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	// 推进到 TTL 之后。
	s.now = func() time.Time { return now.Add(StreamTicketTTL + time.Second) }
	if _, ok := s.Redeem(raw); ok {
		t.Fatal("过期票据必须被拒")
	}
}

func TestStreamTicketReplacesSameSubject(t *testing.T) {
	s := NewStreamTicketStore()
	p := Principal{UserID: 3, Username: "a", Role: domain.RoleOperator}

	first, err := s.Issue(p)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	second, err := s.Issue(p)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if first == second {
		t.Fatal("两次签发必须给出不同的票据")
	}
	// 换票会顶掉旧票：一个人开多个监控页不该在内存里堆几十条记录。
	if _, ok := s.Redeem(first); ok {
		t.Fatal("旧票据应已被新票据顶替")
	}
	if _, ok := s.Redeem(second); !ok {
		t.Fatal("最新票据必须可用")
	}
	if len(s.tickets) != 0 {
		t.Fatalf("兑换后应无残留票据，实际剩 %d 条", len(s.tickets))
	}
}

func TestStreamTicketRejectGarbage(t *testing.T) {
	s := NewStreamTicketStore()
	for _, raw := range []string{"", "not-a-ticket", "../../etc/passwd"} {
		if _, ok := s.Redeem(raw); ok {
			t.Errorf("非法票据 %q 必须被拒", raw)
		}
	}
}
