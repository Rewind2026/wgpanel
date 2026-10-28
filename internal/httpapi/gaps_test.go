package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Rewind2026/wgpanel/internal/authn"
	"github.com/Rewind2026/wgpanel/internal/config"
	"github.com/Rewind2026/wgpanel/internal/domain"
	"github.com/Rewind2026/wgpanel/internal/eventbus"
	"github.com/Rewind2026/wgpanel/internal/monitor"
	"github.com/Rewind2026/wgpanel/internal/netdriver"
	"github.com/Rewind2026/wgpanel/internal/service"
	"github.com/Rewind2026/wgpanel/internal/store"
)

// 这一组用例钉住五个「界面上看着正常、实际没生效」的缺陷。
// 它们有共同特征：接口全部 200、日志没有 error、界面数字正确，
// 唯独功能不存在。单测里最容易漏，因为每一步断言都成立。

// --- 缺口 1：GET /settings 曾经不挂任何权限点 ---------------------------

// viewer 能读到备份目录、出口地址、策略开关等全部运行时设置。
//
// 同级的 /backups 整组挂了 settings:write，唯独 settings
// 只在写的那一行挂了权限，读的这一行漏了——护栏只装了一半。
//
// 这里刻意走完整 Router：权限中间件挂在路由树上，绕开它直接调 handler
// 就等于把要测的东西跳过了。
func TestGetSettingsRequiresSettingsWrite(t *testing.T) {
	env := newTestEnv(t, nil)

	cases := []struct {
		role domain.Role
		want int
	}{
		{domain.RoleOwner, http.StatusOK},
		{domain.RoleAdmin, http.StatusOK},
		{domain.RoleOperator, http.StatusForbidden},
		{domain.RoleViewer, http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(string(tc.role), func(t *testing.T) {
			rec := env.do(t, http.MethodGet, "/api/v1/settings/", tc.role, nil)
			if rec.Code != tc.want {
				t.Fatalf("%s 读设置应 %d，实际 %d：%s",
					tc.role, tc.want, rec.Code, rec.Body.String())
			}
		})
	}
}

// --- 缺口 2：批量吊销曾只有「指定」语义 ---------------------------------

// 全量吊销不能依赖前端勾选态。
//
// 前端勾选受虚拟滚动与搜索影响，密钥泄露应急时「漏掉没加载出来的那几台」
// 不会有任何提示。所以 {all:true} 必须由服务端自己去遍历全库。
func TestBulkRevokeAllCoversEveryPeer(t *testing.T) {
	env := newTestEnv(t, nil)
	st := env.store
	ctx := t.Context()
	admin := &authn.Principal{UserID: 1, Username: "admin", Role: domain.RoleAdmin}
	peers := env.server.peers

	var ids []int64
	for _, name := range []string{"a", "b", "c"} {
		p, err := peers.Create(ctx, service.CreateInput{Name: name}, admin)
		if err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		ids = append(ids, p.ID)
	}
	before := publicKeys(t, st, ids)

	rec := env.do(t, http.MethodPost, "/api/v1/peers/bulk-revoke",
		domain.RoleAdmin, map[string]any{"all": true})
	if rec.Code != http.StatusOK {
		t.Fatalf("全量吊销应 200，实际 %d：%s", rec.Code, rec.Body.String())
	}
	if got := revokedCount(t, rec); got != 3 {
		t.Fatalf("应吊销 3 个，实际 %d", got)
	}

	after := publicKeys(t, st, ids)
	for i, id := range ids {
		if before[i] == after[i] {
			t.Errorf("Peer %d 的公钥未轮换，仍是 %s", id, after[i])
		}
	}
}

// 空 ids 仍然必须被拒：它与 {all:true} 语义不同，
// 静默当成全量会让一次误操作吊销掉所有客户端。
func TestBulkRevokeRejectsEmptySelection(t *testing.T) {
	env := newTestEnv(t, nil)
	rec := env.do(t, http.MethodPost, "/api/v1/peers/bulk-revoke",
		domain.RoleAdmin, map[string]any{"ids": []int64{}})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("空 ids 应 400，实际 %d：%s", rec.Code, rec.Body.String())
	}
}

// --- 缺口 4：采样/调谐间隔曾只落库不生效 ---------------------------------

// 保存设置后，两个间隔必须真的推给后台循环。
//
// 修复前 Collector.interval 与 Reconciler.interval 都是启动期一次性写死的
// 普通字段，PUT /settings 只更新数据库——界面提示保存成功，节奏纹丝不动。
func TestSaveSettingsAppliesIntervalsImmediately(t *testing.T) {
	var gotReconcile time.Duration
	env := newTestEnv(t, func(d time.Duration) { gotReconcile = d })

	rec := env.do(t, http.MethodPut, "/api/v1/settings/",
		domain.RoleAdmin, settingsBody(5, 20))
	if rec.Code != http.StatusOK {
		t.Fatalf("保存设置应 200，实际 %d：%s", rec.Code, rec.Body.String())
	}
	if got := env.server.collector.Interval(); got != 5*time.Second {
		t.Errorf("采集间隔应变为 5s，实际 %v", got)
	}
	if gotReconcile != 20*time.Second {
		t.Errorf("调谐间隔应变为 20s，实际 %v", gotReconcile)
	}
}

// 重复保存同一份设置，间隔不应被冲成默认值或零值。
func TestSaveSettingsKeepsIntervalsWhenRepeated(t *testing.T) {
	var gotReconcile time.Duration
	env := newTestEnv(t, func(d time.Duration) { gotReconcile = d })

	for i := 0; i < 3; i++ {
		rec := env.do(t, http.MethodPut, "/api/v1/settings/",
			domain.RoleAdmin, settingsBody(3, 7))
		if rec.Code != http.StatusOK {
			t.Fatalf("第 %d 次保存应 200，实际 %d：%s", i, rec.Code, rec.Body.String())
		}
	}
	if got := env.server.collector.Interval(); got != 3*time.Second {
		t.Errorf("采集间隔应保持 3s，实际 %v", got)
	}
	if gotReconcile != 7*time.Second {
		t.Errorf("调谐间隔应保持 7s，实际 %v", gotReconcile)
	}
}

// --- 缺口 5：MaxConnections 曾是一个会骗人的输入框 -----------------------

// maxConnections 在 WireGuard 协议层无法实现：内核给每个 Peer 只保留一个
// endpoint 槽位，不存在「同时连接数」。面板却渲染了输入框并写着
// 「超出后策略会终止超额连接」——用户填了、存了、导出了，唯独不生效。
//
// 移除后 decodeJSON 的 DisallowUnknownFields 会把这个字段大声拒掉，
// 这比「静默忽略」好：旧客户端如果还在提交它，运维能看到 400 而不是
// 以为设置生效了。所以这里断言的是 400，而不是 201。
func TestCreatePeerRejectsMaxConnections(t *testing.T) {
	env := newTestEnv(t, nil)
	rec := env.do(t, http.MethodPost, "/api/v1/peers/", domain.RoleAdmin,
		map[string]any{"name": "laptop", "maxConnections": 5})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("提交已移除的 maxConnections 应 400，实际 %d：%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "maxConnections") {
		t.Errorf("错误信息应点名 maxConnections，实际：%s", rec.Body.String())
	}
}

// 正常创建时 MaxConnections 必须恒为 0，且库里存的也是 0。
//
// 上一条只证明「接口不再接受这个字段」，但字段仍在 domain.Peer 上
// （为兼容既有库表），所以还得证明没有任何写入路径能把它设成非零。
func TestCreatePeerLeavesMaxConnectionsZero(t *testing.T) {
	env := newTestEnv(t, nil)
	rec := env.do(t, http.MethodPost, "/api/v1/peers/", domain.RoleAdmin,
		map[string]any{"name": "laptop"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("建 Peer 应 201，实际 %d：%s", rec.Code, rec.Body.String())
	}
	var out struct {
		Data domain.Peer `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, rec.Body.String())
	}
	if out.Data.MaxConnections != 0 {
		t.Errorf("响应里的 maxConnections 应为 0，实际 %d", out.Data.MaxConnections)
	}
	stored, err := env.store.PeerRepo().Get(t.Context(), out.Data.ID)
	if err != nil {
		t.Fatalf("get peer: %v", err)
	}
	if stored.MaxConnections != 0 {
		t.Errorf("库里落盘的 maxConnections 应为 0，实际 %d", stored.MaxConnections)
	}
}

// --- 装配 ---------------------------------------------------------------

func testLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func testCipher(t *testing.T) *authn.Cipher {
	t.Helper()
	c, err := authn.NewCipher([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatalf("new cipher: %v", err)
	}
	return c
}

func testStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.OpenInMemory()
	if err != nil {
		t.Fatalf("open in-memory store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// testEnv 是一次完整装配，含可签发任意角色 JWT 的 issuer。
type testEnv struct {
	server *Server
	store  *store.Store
	issuer *authn.TokenIssuer
	// 每个角色对应一个真实落库的用户。
	//
	// 不能只在 JWT 里换角色：AuthService.AuthenticateAccessToken 拿到
	// claims 后会按 UserID 回库重读用户，并以库里的角色为准
	// （auth_service.go）。这是刻意的设计——令牌里自称的角色不可信，
	// 否则改一次角色就能靠一枚旧令牌长期提权。
	// 后果是：只改 claim 里的 role 而不动数据库，RBAC 根本不会被触发，
	// 测试会拿到「所有角色都 200」的假绿灯。
	userIDs map[domain.Role]int64
}

// newTestEnv 走完整的 New(Deps{...}) + Handler()，
// 因为这五个缺陷里有四个的根因就在「路由树少挂了中间件」或
// 「Server 上少接了一个回调」——绕开装配直接调 handler 等于把被测对象换掉。
func newTestEnv(t *testing.T, setReconcile func(time.Duration)) *testEnv {
	t.Helper()
	st := testStore(t)
	cipher := testCipher(t)
	log := testLog()
	ctx := t.Context()

	encKey, err := cipher.Encrypt("QFHRFv7RvyHZ1bXfHvLPCEEZjbXfTGVvyHIY9ULBmqk=")
	if err != nil {
		t.Fatalf("encrypt server key: %v", err)
	}
	if err := st.ServerRepo().Save(ctx, &domain.ServerConfig{
		Interface:   "wg0",
		ListenPort:  51820,
		PrivateKey:  encKey,
		PublicKey:   "u8TNGKrAlAxs7EE9y1JELUJLPCLDPF9lNs9n5uOO7IU=",
		AddressCIDR: "10.8.0.1/24",
		MTU:         1420,
		AllowedIPs:  "0.0.0.0/0, ::/0",
	}, nil); err != nil {
		t.Fatalf("save server config: %v", err)
	}

	issuer := authn.NewTokenIssuer([]byte("0123456789abcdef0123456789abcdef"), time.Hour)
	authSvc := service.NewAuthService(st.UserRepo(), issuer,
		authn.NewLoginLimiter(10, time.Minute), time.Hour, time.Hour, st.AuditRepo())

	// 每个角色建一个真实用户，RBAC 判定走的就是库里的这一列。
	userIDs := make(map[domain.Role]int64, 4)
	for _, role := range []domain.Role{
		domain.RoleOwner, domain.RoleAdmin, domain.RoleOperator, domain.RoleViewer,
	} {
		u := &domain.User{
			Username: "tester-" + string(role), PasswordHash: "x", Role: role, Enabled: true,
		}
		if _, err := st.UserRepo().Create(ctx, u); err != nil {
			t.Fatalf("create %s user: %v", role, err)
		}
		userIDs[role] = u.ID
	}

	drv := stubDriver{}
	peerSvc := service.NewPeerService(st.PeerRepo(), st.ServerRepo(), cipher,
		st.AuditRepo(), drv, func(context.Context) {})
	srvSvc := service.NewServerService(st.ServerRepo(), cipher, st.AuditRepo(),
		config.Config{}, nil)
	bus := eventbus.New(log, 64)
	peerSvc.SetPublisher(bus)

	return &testEnv{
		store:   st,
		issuer:  issuer,
		userIDs: userIDs,
		server: New(Deps{
			Config:    config.Config{SampleInterval: time.Second},
			Log:       log,
			Store:     st,
			Auth:      authSvc,
			Account:   service.NewAccountService(st.UserRepo(), st.AuditRepo()),
			Servers:   srvSvc,
			Peers:     peerSvc,
			Collector: monitor.New(nil, nil, st.TrafficRepo(), time.Second, log),
			Traffic:   st.TrafficRepo(),
			Audit:     st.AuditRepo(),
			Driver:    drv,
			Events:    bus,
			Settings:  st.SettingRepo(),
			Version:   "test",
			NetMode:   netdriver.ModeManaged,
			Tickets:   authn.NewStreamTicketStore(),
			// 记录回调收到的调谐间隔，避免断言依赖真实后台协程的时序。
			SetReconcileInterval: setReconcile,
		}),
	}
}

// token 为指定角色签发一枚真实 JWT，让鉴权中间件走完整链路。
func (e *testEnv) token(t *testing.T, role domain.Role) string {
	t.Helper()
	uid, ok := e.userIDs[role]
	if !ok {
		t.Fatalf("没有 %s 角色的测试用户", role)
	}
	u, err := e.store.UserRepo().GetByID(t.Context(), uid)
	if err != nil {
		t.Fatalf("load %s user: %v", role, err)
	}
	tok, err := e.issuer.Issue(u, time.Now())
	if err != nil {
		t.Fatalf("issue token for %s: %v", role, err)
	}
	return tok
}

func (e *testEnv) do(t *testing.T, method, path string, role domain.Role,
	body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader = strings.NewReader("")
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		reader = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+e.token(t, role))
	rec := httptest.NewRecorder()
	e.server.Handler().ServeHTTP(rec, req)
	return rec
}

// settingsBody 以默认设置为底，只改两个间隔。
func settingsBody(sample, reconcile int) map[string]any {
	d := domain.DefaultSettings()
	d.SampleIntervalSeconds = sample
	d.ReconcileIntervalSeconds = reconcile
	raw, _ := json.Marshal(d)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	return m
}

func revokedCount(t *testing.T, rec *httptest.ResponseRecorder) int {
	t.Helper()
	var out struct {
		Data struct {
			Revoked int `json:"revoked"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, rec.Body.String())
	}
	return out.Data.Revoked
}

func publicKeys(t *testing.T, st *store.Store, ids []int64) []string {
	t.Helper()
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		p, err := st.PeerRepo().Get(t.Context(), id)
		if err != nil {
			t.Fatalf("get peer %d: %v", id, err)
		}
		out = append(out, p.PublicKey)
	}
	return out
}
