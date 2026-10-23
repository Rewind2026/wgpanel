package service

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"log/slog"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Rewind2026/wgpanel/internal/authn"
	"github.com/Rewind2026/wgpanel/internal/domain"
	"github.com/Rewind2026/wgpanel/internal/store"
	"github.com/Rewind2026/wgpanel/internal/wgdriver"
)

var testKey = []byte("0123456789abcdef0123456789abcdef")

// fakeDriver 是一个不碰内核的 wgdriver 实现。
// 单测关注的是服务层行为（导入、订阅、批量），内核下发由 reconciler 的测试覆盖。
type fakeDriver struct {
	available error
}

func (f *fakeDriver) Available() error { return f.available }
func (f *fakeDriver) Device(context.Context, string) (*wgdriver.DeviceState, error) {
	return nil, f.available
}
func (f *fakeDriver) Up(context.Context, *domain.ServerConfig, []domain.Peer) error {
	return nil
}
func (f *fakeDriver) Down(context.Context, string) error                     { return nil }
func (f *fakeDriver) SyncPeers(context.Context, string, []domain.Peer) error { return nil }
func (f *fakeDriver) PeerStats(string) (map[string]*domain.PeerStats, error) {
	return nil, f.available
}
func (f *fakeDriver) TotalCounters(string) (int64, int64, error) { return 0, 0, f.available }

type fixture struct {
	svc    *PeerService
	store  *store.Store
	cipher *authn.Cipher
	events *recorder
}

// recorder 记录发布出去的事件，供断言订阅链路。
type recorder struct {
	mu     sync.Mutex
	events []domain.Event
}

func (r *recorder) Publish(_ context.Context, ev domain.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, ev)
}

func (r *recorder) types() []domain.EventType {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]domain.EventType, 0, len(r.events))
	for _, e := range r.events {
		out = append(out, e.Type)
	}
	return out
}

func (r *recorder) count(t domain.EventType) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, e := range r.events {
		if e.Type == t {
			n++
		}
	}
	return n
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	st, err := store.OpenInMemory()
	if err != nil {
		t.Fatalf("open in-memory store: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	c, err := authn.NewCipher(testKey)
	if err != nil {
		t.Fatalf("new cipher: %v", err)
	}
	ctx := t.Context()
	servers := st.ServerRepo()

	// 服务端私钥在库里也是密文，加密一次省得每条用例都绕。
	serverKey, err := c.Encrypt("QFHRFv7RvyHZ1bXfHvLPCEEZjbXfTGVvyHIY9ULBmqk=")
	if err != nil {
		t.Fatalf("encrypt server key: %v", err)
	}
	if err := servers.Save(ctx, &domain.ServerConfig{
		Interface:   "wg0",
		ListenPort:  51820,
		PrivateKey:  serverKey,
		PublicKey:   "u8TNGKrAlAxs7EE9y1JELUJLPCLDPF9lNs9n5uOO7IU=",
		AddressCIDR: "10.8.0.1/24",
		MTU:         1420,
		AllowedIPs:  "0.0.0.0/0, ::/0",
		DNS:         "1.1.1.1",
	}, nil); err != nil {
		t.Fatalf("save server config: %v", err)
	}

	_ = slog.Default
	svc := NewPeerService(st.PeerRepo(), servers, c, st.AuditRepo(),
		&fakeDriver{}, func(context.Context) {})
	rec := &recorder{}
	svc.SetPublisher(rec)

	return &fixture{svc: svc, store: st, cipher: c, events: rec}
}

func (f *fixture) create(t *testing.T, name string) *domain.Peer {
	t.Helper()
	p, err := f.svc.Create(t.Context(), CreateInput{Name: name}, &authn.Principal{
		UserID: 1, Username: "admin", Role: domain.RoleAdmin,
	})
	if err != nil {
		t.Fatalf("create peer %s: %v", name, err)
	}
	return p
}

func TestCreateAllocatesAddressAndEncryptsKeys(t *testing.T) {
	f := newFixture(t)
	p := f.create(t, "laptop")

	if p.Address == "" || p.Address == "10.8.0.1" {
		t.Errorf("应自动分配一个非服务端地址，实际 %q", p.Address)
	}
	if p.PublicKey == "" || p.PrivateKey == "" {
		t.Error("应生成密钥对")
	}
	// 落库必须是密文：数据库文件被拷走就等于泄露全部客户端凭据。
	stored, err := f.store.PeerRepo().Get(t.Context(), p.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if stored.PrivateKey == p.PrivateKey {
		t.Error("私钥未加密落库")
	}
	if !strings.Contains(stored.PrivateKey, "enc:") && stored.PrivateKey == p.PrivateKey {
		t.Error("私钥明文出现在库里")
	}
}

// TestCreateNormalizesBareAddress 锁定裸 IP 必须被补上 /32 后落库。
//
// 真机上暴露的缺陷：归一化结果只用于校验，落库的是原始输入。
// 于是 "10.8.0.212" 这样的写法进了数据库，而下游（wgctrl 解析 AllowedIPs、
// 限速按地址匹配）都按 CIDR 解析，每轮调谐都在这个 Peer 上失败：
// `invalid cidr "10.8.0.212"`。
// 故障范围远不止这一个 Peer——调谐是全量替换，一条 Peer 解析失败就挡住了
// 全部 Peer 的下发，新建 Peer 的限速、地址变更全部不生效，
// 面板看起来只是「新增后没反应」。
func TestCreateNormalizesBareAddress(t *testing.T) {
	f := newFixture(t)
	p, err := f.svc.Create(t.Context(), CreateInput{
		Name: "bare", Address: "10.8.0.212", AllowedIPs: "10.8.0.212/32",
	}, &authn.Principal{UserID: 1, Username: "admin", Role: domain.RoleAdmin})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if p.Address != "10.8.0.212/32" {
		t.Errorf("返回值地址 = %q，期望 10.8.0.212/32", p.Address)
	}
	stored, err := f.store.PeerRepo().Get(t.Context(), p.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if stored.Address != "10.8.0.212/32" {
		t.Errorf("落库地址 = %q，期望 10.8.0.212/32", stored.Address)
	}
	if _, err := netip.ParsePrefix(stored.Address); err != nil {
		t.Errorf("落库地址必须是合法 CIDR，否则下游解析必然失败: %v", err)
	}
}

// TestPeerLifecyclePublishesEvents 锁定 Peer 变更确实会发事件。
//
// 真机上暴露的缺陷：订阅项写的是 peer.create，但代码里
// 根本没有这个事件的发布点——配置保存成功、界面显示已启用，
// 却永远收不到任何通知。事件名拼错与事件根本没发布在这里是同一类故障：
// 静默，且用户无从察觉。
func TestPeerLifecyclePublishesEvents(t *testing.T) {
	f := newFixture(t)
	actor := &authn.Principal{UserID: 1, Username: "admin", Role: domain.RoleAdmin}
	ctx := t.Context()

	p := f.create(t, "laptop")
	if got := f.events.count(domain.EventPeerCreated); got != 1 {
		t.Fatalf("创建应发 1 条 peer.created，实际 %d（全部事件：%v）", got, f.events.types())
	}

	note := "updated"
	if _, err := f.svc.Update(ctx, p.ID, UpdateInput{Note: &note}, actor); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got := f.events.count(domain.EventPeerUpdated); got != 1 {
		t.Errorf("更新应发 1 条 peer.updated，实际 %d", got)
	}

	if _, err := f.svc.RotateKey(ctx, p.ID, actor); err != nil {
		t.Fatalf("RotateKey: %v", err)
	}
	// 轮换必须与普通更新分开：客户端需要立刻重新下发配置，
	// 混在 peer.updated 里收通知的人分不出轻重。
	if got := f.events.count(domain.EventPeerRotated); got != 1 {
		t.Errorf("轮换应发 1 条 peer.key_rotated，实际 %d", got)
	}

	if err := f.svc.Delete(ctx, p.ID, actor); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if got := f.events.count(domain.EventPeerDeleted); got != 1 {
		t.Errorf("删除应发 1 条 peer.deleted，实际 %d", got)
	}
}

// TestBulkRevokeCollapsesIntoOneEvent 确认批量吊销只发一条汇总事件。
//
// 逐条发的话，吊销 50 台设备就推 50 条通知，任何渠道都会被淹掉。
func TestBulkRevokeCollapsesIntoOneEvent(t *testing.T) {
	f := newFixture(t)
	actor := &authn.Principal{UserID: 1, Username: "admin", Role: domain.RoleAdmin}

	var ids []int64
	for _, n := range []string{"a", "b", "c"} {
		ids = append(ids, f.create(t, n).ID)
	}
	f.events.mu.Lock()
	f.events.events = nil
	f.events.mu.Unlock()

	n, err := f.svc.BulkRevoke(t.Context(), ids, actor)
	if err != nil {
		t.Fatalf("BulkRevoke: %v", err)
	}
	if n != 3 {
		t.Fatalf("应吊销 3 个，实际 %d", n)
	}
	if got := len(f.events.types()); got != 1 {
		t.Errorf("批量吊销应只发 1 条汇总事件，实际 %d 条：%v", got, f.events.types())
	}
	if got := f.events.count(domain.EventPeerRotated); got != 1 {
		t.Errorf("汇总事件类型应为 peer.key_rotated，实际 %d", got)
	}
}

// TestNoPublisherIsSafe 确认未注入总线时服务层不崩，
// 便于测试与未来的单二进制精简构建。
func TestNoPublisherIsSafe(t *testing.T) {
	st, err := store.OpenInMemory()
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	c, _ := authn.NewCipher(testKey)
	svc := NewPeerService(st.PeerRepo(), st.ServerRepo(), c, st.AuditRepo(),
		&fakeDriver{}, func(context.Context) {})
	// 没有 server_config，Create 会因取不到服务端配置而失败——这里只关心不 panic。
	_, _ = svc.Create(t.Context(), CreateInput{Name: "x"}, nil)
	_, _ = svc.BulkRevoke(t.Context(), []int64{1}, nil)
}

// TestListDoesNotLeakKeyMaterial 锁定列表接口不返回任何密钥材料。
//
// 真机发现的问题：List 原样透传仓储层的密文，响应里带着
// "privateKey":"<密文>"。密文对调用方毫无用处，却把加密凭据发到了浏览器。
func TestListDoesNotLeakKeyMaterial(t *testing.T) {
	f := newFixture(t)
	created := f.create(t, "laptop")

	peers, err := f.svc.List(t.Context())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(peers) != 1 {
		t.Fatalf("应返回 1 个 Peer，实际 %d", len(peers))
	}
	if peers[0].PrivateKey != "" {
		t.Errorf("列表不应返回私钥，实际 %q", peers[0].PrivateKey)
	}
	if peers[0].PresharedKey != "" {
		t.Errorf("列表不应返回预共享密钥，实际 %q", peers[0].PresharedKey)
	}
	// 公钥必须保留：调谐、限速与前端识别 Peer 都依赖它。
	if peers[0].PublicKey != created.PublicKey {
		t.Errorf("列表应保留公钥，期望 %q，实际 %q", created.PublicKey, peers[0].PublicKey)
	}
	// 序列化后也不能出现密钥字段（omitempty 保证空值不落到 JSON）。
	body, err := json.Marshal(peers[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"privateKey", "presharedKey"} {
		if strings.Contains(string(body), field) {
			t.Errorf("序列化结果仍含 %s：%s", field, body)
		}
	}
}

// TestListReturnsDecryptedKeysForAuthorizedConfigExport 确认收敛密钥没有误伤
// 真正需要明文的路径：按 id 取单个 Peer 仍应解开私钥。
func TestListReturnsDecryptedKeysForAuthorizedConfigExport(t *testing.T) {
	f := newFixture(t)
	created := f.create(t, "laptop")

	got, err := f.svc.Get(t.Context(), created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.PrivateKey != created.PrivateKey {
		t.Error("按 id 读取应返回明文私钥，供客户端配置导出使用")
	}
}

// TestListFillsPeerIdentityIntoStats 确认列表接口的 stats 带上 peerId 与公钥。
//
// 真机上暴露过的缺陷：驱动只按公钥返回运行态，认不出数据库主键，
// 服务层又原样透传，于是 /api/v1/peers 的每个 stats.peerId 都是 0。
// 前端把列表 stats 与 SSE 帧按 peerId 对齐（MonitorPage 的 mergeStats），
// peerId 为 0 时两份数据对不上，列表数据永远被 SSE 覆盖或错配。
func TestListFillsPeerIdentityIntoStats(t *testing.T) {
	f := newFixture(t)
	created := f.create(t, "laptop")

	// 换成会返回运行态的驱动：按公钥索引，与内核侧一致。
	drv := &statsDriver{stats: map[string]*domain.PeerStats{}}
	drv.stats[created.PublicKey] = &domain.PeerStats{
		Online: true, RxBytes: 100, TxBytes: 200, AllowedIPs: []string{"10.8.0.2"},
	}
	svc := NewPeerService(f.store.PeerRepo(), f.store.ServerRepo(), f.cipher,
		f.store.AuditRepo(), drv, func(context.Context) {})
	svc.SetPublisher(&recorder{})

	peers, err := svc.List(t.Context())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(peers) != 1 {
		t.Fatalf("应返回 1 个 Peer，实际 %d", len(peers))
	}
	if peers[0].Stats == nil {
		t.Fatal("驱动可用时列表应带运行态")
	}
	if !peers[0].Stats.Online {
		t.Error("运行态应被透传")
	}
	if peers[0].Stats.PeerID != created.ID {
		t.Errorf("stats.peerId 应为 %d，实际 %d", created.ID, peers[0].Stats.PeerID)
	}
	if peers[0].Stats.PublicKey != created.PublicKey {
		t.Errorf("stats.publicKey 应为 %q，实际 %q", created.PublicKey, peers[0].Stats.PublicKey)
	}
}

// statsDriver 返回预先塞好的运行态记录。
type statsDriver struct {
	fakeDriver
	stats map[string]*domain.PeerStats
}

func (s *statsDriver) PeerStats(string) (map[string]*domain.PeerStats, error) {
	out := make(map[string]*domain.PeerStats, len(s.stats))
	for k, v := range s.stats {
		out[k] = v
	}
	return out, nil
}

// TestListFillsPeerIdentityWhenPeerHasNoLiveStats 确认驱动没返回该 peer 时，
// 空运行态同样带上身份字段，而不是退化成 peerId=0 的裸壳。
func TestListFillsPeerIdentityWhenPeerHasNoLiveStats(t *testing.T) {
	f := newFixture(t)
	created := f.create(t, "laptop")

	drv := &emptyStatsDriver{}
	svc := NewPeerService(f.store.PeerRepo(), f.store.ServerRepo(), f.cipher,
		f.store.AuditRepo(), drv, func(context.Context) {})
	svc.SetPublisher(&recorder{})

	peers, err := svc.List(t.Context())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(peers) != 1 || peers[0].Stats == nil {
		t.Fatalf("应返回带运行态的 1 个 Peer，实际 %d", len(peers))
	}
	if peers[0].Stats.PeerID != created.ID {
		t.Errorf("空运行态的 peerId 也应为 %d，实际 %d", created.ID, peers[0].Stats.PeerID)
	}
}

type emptyStatsDriver struct {
	fakeDriver
}

func (e *emptyStatsDriver) PeerStats(string) (map[string]*domain.PeerStats, error) {
	return map[string]*domain.PeerStats{}, nil
}

func TestCreateRejectsDuplicateAddress(t *testing.T) {
	f := newFixture(t)
	first := f.create(t, "laptop")

	_, err := f.svc.Create(t.Context(), CreateInput{
		Name: "phone", Address: first.Address,
	}, &authn.Principal{UserID: 1, Username: "admin", Role: domain.RoleAdmin})
	if err == nil {
		t.Fatal("地址重复时应报错")
	}
	if code := domain.AsError(err).Code; code != domain.CodeConflict {
		t.Errorf("应为冲突错误，实际 %v", code)
	}
}

func TestCreateRejectsAddressOutsideSubnet(t *testing.T) {
	f := newFixture(t)
	_, err := f.svc.Create(t.Context(), CreateInput{
		Name: "evil", Address: "192.168.5.5",
	}, &authn.Principal{UserID: 1, Username: "admin", Role: domain.RoleAdmin})
	if err == nil {
		t.Fatal("分配到子网外的地址应被拒绝")
	}
}

func TestExportCSVNeverLeaksPrivateKey(t *testing.T) {
	f := newFixture(t)
	f.create(t, "laptop")

	data, err := f.svc.ExportCSV(t.Context())
	if err != nil {
		t.Fatalf("ExportCSV: %v", err)
	}
	text := string(data)
	if !strings.HasPrefix(text, "name,") {
		t.Errorf("首行应为表头，实际 %q", strings.SplitN(text, "\n", 2)[0])
	}
	// 明文私钥进文件就等于把所有人的隧道凭据写到磁盘上。
	if strings.Contains(text, "PrivateKey") || strings.Contains(text, "privateKey") {
		t.Error("导出列里出现了私钥")
	}
	records, err := csv.NewReader(strings.NewReader(text)).ReadAll()
	if err != nil {
		t.Fatalf("parse csv: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("应为表头 + 1 行数据，实际 %d 行", len(records))
	}
}

func TestCSVRoundTripPreservesSettings(t *testing.T) {
	f := newFixture(t)
	expires := time.Date(2027, 1, 2, 3, 4, 5, 0, time.UTC)
	enabled := false
	_, err := f.svc.Create(t.Context(), CreateInput{
		Name: "laptop", Note: "研发同学", Tags: []string{"vpn", "team-a"},
		TrafficQuotaBytes: 1 << 30, QuotaPeriod: domain.QuotaPeriodWeekly,
		RateLimitMbps: 20, PersistentKeepalive: 25,
		Enabled: &enabled, ExpiresAt: &expires,
	}, &authn.Principal{UserID: 1, Username: "admin", Role: domain.RoleAdmin})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	data, err := f.svc.ExportCSV(t.Context())
	if err != nil {
		t.Fatalf("ExportCSV: %v", err)
	}

	// 换一个空库导入，验证「导出的文件能原样导回来」这个约定。
	fresh := newFixture(t)
	res, err := fresh.svc.ImportCSV(t.Context(), data, &authn.Principal{
		UserID: 1, Username: "admin", Role: domain.RoleAdmin,
	})
	if err != nil {
		t.Fatalf("ImportCSV: %v", err)
	}
	if res.Created != 1 || res.Failed != 0 {
		t.Fatalf("导入应成功 1 行，实际 %+v", res)
	}

	peers, err := fresh.store.PeerRepo().List(t.Context())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	p := peers[0]
	if p.Note != "研发同学" {
		t.Errorf("备注应往返，实际 %q", p.Note)
	}
	if len(p.Tags) != 2 || p.Tags[0] != "vpn" || p.Tags[1] != "team-a" {
		t.Errorf("标签应往返，实际 %v", p.Tags)
	}
	if p.TrafficQuotaBytes != 1<<30 || p.QuotaPeriod != domain.QuotaPeriodWeekly {
		t.Errorf("配额应往返，实际 %d/%s", p.TrafficQuotaBytes, p.QuotaPeriod)
	}
	if p.RateLimitMbps != 20 || p.PersistentKeepalive != 25 {
		t.Errorf("限速/保活应往返，实际 %d/%d",
			p.RateLimitMbps, p.PersistentKeepalive)
	}
	// maxConnections 是历史遗留列：CSV 里仍有这一列以保证旧文件不错位，
	// 但导入不再把它写进 Peer（内核层无法实现，见 domain.Peer.MaxConnections）。
	if p.MaxConnections != 0 {
		t.Errorf("maxConnections 不应被导入为有效值，实际 %d", p.MaxConnections)
	}
	if p.Enabled {
		t.Error("enabled=false 应被往返保留")
	}
	if p.ExpiresAt == nil || !p.ExpiresAt.Equal(expires) {
		t.Errorf("有效期应往返，期望 %s，实际 %v", expires, p.ExpiresAt)
	}
	// 地址不复用：导入到新库后应重新分配，否则会和旧库对不上。
	if p.Address == "" {
		t.Error("导入后应分配地址")
	}
}

// 一次导入几十个客户端时，「前 47 个成功、第 3 行写错了」比整批回滚有用得多。
func TestImportCSVIsPartialSuccessWithRowReasons(t *testing.T) {
	f := newFixture(t)
	csvData := strings.Join([]string{
		"name,address,quotaPeriod,rateLimitMbps,enabled",
		"ok-1,,monthly,10,true",
		",,monthly,10,true",           // 缺 name
		"bad-period,,yearly,10,true",  // 非法周期
		"bad-enabled,,monthly,10,yes", // enabled 非布尔
		"ok-2,,monthly,20,true",
	}, "\n")

	res, err := f.svc.ImportCSV(t.Context(), []byte(csvData), &authn.Principal{
		UserID: 1, Username: "admin", Role: domain.RoleAdmin,
	})
	if err != nil {
		t.Fatalf("ImportCSV 不应整体失败: %v", err)
	}
	if res.Created != 2 || res.Failed != 3 {
		t.Fatalf("应成功 2 失败 3，实际 %+v", res)
	}
	// 每条失败都要指明行号与原因，否则用户只能一行行试。
	if len(res.Errors) != 3 {
		t.Fatalf("应有 3 条行级错误，实际 %d", len(res.Errors))
	}
	for _, e := range res.Errors {
		if e.Line < 2 {
			t.Errorf("错误行号应从 2 开始（表头占第 1 行），实际 %d", e.Line)
		}
		if e.Reason == "" {
			t.Errorf("第 %d 行缺少失败原因", e.Line)
		}
	}

	peers, _ := f.store.PeerRepo().List(t.Context())
	if len(peers) != 2 {
		t.Errorf("应有 2 个 Peer 入库，实际 %d", len(peers))
	}
}

func TestImportCSVRejectsBadInput(t *testing.T) {
	f := newFixture(t)
	actor := &authn.Principal{UserID: 1, Username: "admin", Role: domain.RoleAdmin}
	for name, data := range map[string]string{
		"empty":        "",
		"no-name-col":  "address\n10.8.0.5\n",
		"broken-quote": "name\n\"unterminated\n",
	} {
		if _, err := f.svc.ImportCSV(t.Context(), []byte(data), actor); err == nil {
			t.Errorf("输入 %q 应被拒绝", name)
		}
	}
}

func TestImportCSVIgnoresBlankRows(t *testing.T) {
	f := newFixture(t)
	// 中间插一个空行和一个只有空格的行。前者被 csv 解析器直接丢弃，
	// 后者会进到我们的 isBlankRow 判定里，计入 Skipped。
	csvData := "name\nlaptop\n\n   \nphone\n"
	res, err := f.svc.ImportCSV(t.Context(), []byte(csvData), &authn.Principal{
		UserID: 1, Username: "admin", Role: domain.RoleAdmin,
	})
	if err != nil {
		t.Fatalf("ImportCSV: %v", err)
	}
	if res.Created != 2 || res.Failed != 0 || res.Skipped != 1 {
		t.Errorf("空白行应被跳过并计数，实际 %+v", res)
	}
}

func TestSubscriptionIssueAndRevoke(t *testing.T) {
	f := newFixture(t)
	p := f.create(t, "laptop")
	actor := &authn.Principal{UserID: 1, Username: "admin", Role: domain.RoleAdmin}

	link, err := f.svc.IssueSubscription(t.Context(), p.ID, "https://panel.example.com/", "1.2.3.4", actor)
	if err != nil {
		t.Fatalf("IssueSubscription: %v", err)
	}
	if link.Token == "" {
		t.Fatal("应签发令牌")
	}
	// baseURL 末尾的斜杠必须被去掉，否则拼出双斜杠路径。
	if !strings.HasSuffix(link.URL, "/api/v1/subscription/"+link.Token) {
		t.Errorf("订阅链接格式错误：%s", link.URL)
	}
	if strings.Contains(link.URL, "//api") {
		t.Errorf("baseURL 末尾斜杠未处理：%s", link.URL)
	}

	cfg, err := f.svc.SubscriptionConfig(t.Context(), link.Token, "1.2.3.4")
	if err != nil {
		t.Fatalf("SubscriptionConfig: %v", err)
	}
	for _, want := range []string{
		"[Interface]", "[Peer]", "PrivateKey = ", "Endpoint = 1.2.3.4:51820",
		"DNS = 1.1.1.1", "AllowedIPs = 0.0.0.0/0,::/0",
	} {
		if !strings.Contains(cfg, want) {
			t.Errorf("客户端配置缺少 %q：\n%s", want, cfg)
		}
	}

	// 撤销后同一个令牌必须立刻失效。
	if err := f.svc.RevokeSubscription(t.Context(), p.ID, actor); err != nil {
		t.Fatalf("RevokeSubscription: %v", err)
	}
	if _, err := f.svc.SubscriptionConfig(t.Context(), link.Token, "1.2.3.4"); err == nil {
		t.Error("撤销后旧令牌必须失效")
	}
}

func TestSubscriptionTokenIsUniquePerPeer(t *testing.T) {
	f := newFixture(t)
	actor := &authn.Principal{UserID: 1, Username: "admin", Role: domain.RoleAdmin}
	a := f.create(t, "laptop")
	b := f.create(t, "phone")

	la, err := f.svc.IssueSubscription(t.Context(), a.ID, "https://x", "1.2.3.4", actor)
	if err != nil {
		t.Fatalf("IssueSubscription: %v", err)
	}
	lb, err := f.svc.IssueSubscription(t.Context(), b.ID, "https://x", "1.2.3.4", actor)
	if err != nil {
		t.Fatalf("IssueSubscription: %v", err)
	}
	if la.Token == lb.Token {
		t.Fatal("不同 Peer 的订阅令牌必须不同")
	}
}

func TestBatchTagAddAndRemove(t *testing.T) {
	f := newFixture(t)
	a := f.create(t, "laptop")
	b := f.create(t, "phone")
	actor := &authn.Principal{UserID: 1, Username: "admin", Role: domain.RoleAdmin}

	n, err := f.svc.BatchTag(t.Context(), []int64{a.ID, b.ID}, "prod", false, actor)
	if err != nil {
		t.Fatalf("BatchTag: %v", err)
	}
	if n != 2 {
		t.Errorf("应改动 2 个 Peer，实际 %d", n)
	}

	got, _ := f.store.PeerRepo().Get(t.Context(), a.ID)
	if len(got.Tags) != 1 || got.Tags[0] != "prod" {
		t.Errorf("标签应写入，实际 %v", got.Tags)
	}

	if n, err := f.svc.BatchTag(t.Context(), []int64{a.ID}, "prod", true, actor); err != nil || n != 1 {
		t.Fatalf("移除标签失败: n=%d err=%v", n, err)
	}
	got, _ = f.store.PeerRepo().Get(t.Context(), a.ID)
	if len(got.Tags) != 0 {
		t.Errorf("标签应被移除，实际 %v", got.Tags)
	}

	if _, err := f.svc.BatchTag(t.Context(), []int64{a.ID}, "  ", false, actor); err == nil {
		t.Error("空标签应被拒绝")
	}
}

// 批量停用是人工操作，绝不能被标记成策略停用，
// 否则配额周期重置会把它们全部自动打开——一个绕过审计的开关。
func TestBulkEnableIsNotMarkedAsPolicySuspension(t *testing.T) {
	f := newFixture(t)
	p := f.create(t, "laptop")
	actor := &authn.Principal{UserID: 1, Username: "admin", Role: domain.RoleAdmin}

	if _, err := f.svc.BulkEnable(t.Context(), []int64{p.ID}, false, actor); err != nil {
		t.Fatalf("BulkEnable: %v", err)
	}
	got, _ := f.store.PeerRepo().Get(t.Context(), p.ID)
	if got.Enabled {
		t.Fatal("应被停用")
	}
	if got.SuspendedBy != domain.SuspendedNone {
		t.Errorf("人工停用不应带策略标记，实际 %q", got.SuspendedBy)
	}

	if _, err := f.svc.BulkEnable(t.Context(), []int64{p.ID}, true, actor); err != nil {
		t.Fatalf("BulkEnable: %v", err)
	}
	got, _ = f.store.PeerRepo().Get(t.Context(), p.ID)
	if !got.Enabled {
		t.Error("应被重新启用")
	}
}
