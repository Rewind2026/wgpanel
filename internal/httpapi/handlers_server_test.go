package httpapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/Rewind2026/wgpanel/internal/authn"
	"github.com/Rewind2026/wgpanel/internal/domain"
	"github.com/Rewind2026/wgpanel/internal/service"
	"github.com/Rewind2026/wgpanel/internal/store"
	"github.com/Rewind2026/wgpanel/internal/wgdriver"
)

// 客户端配置导出必须原样透传管理员给的 endpoint。
//
// 背景：真实环境里管理员导出配置时会带上 `?endpoint=`，
// 因为面板推断出的公网地址在 NAT、端口映射、容器 NAT 网络场景下经常不是
// 客户端真正要连的地址。以前真机验证只用裸 IP，正好落在「补端口」这条
// 正确分支上，于是「已带端口被二次追加」这个缺陷一直藏着。
// 这条用例直接打 HTTP 入口，把参数传递链路和渲染一起钉住。
func TestPeerConfigEndpointQueryParam(t *testing.T) {
	s, peerID := newPeerConfigServer(t)

	cases := []struct {
		name  string
		query string
		want  string
	}{
		{"裸地址补端口", "?endpoint=192.168.50.63", "Endpoint = 192.168.50.63:51820"},
		{"已带端口不重复追加", "?endpoint=172.17.0.2:51820", "Endpoint = 172.17.0.2:51820"},
		{"裸 IPv6 加方括号", "?endpoint=2001:db8::1", "Endpoint = [2001:db8::1]:51820"},
		{"下载形态同样归一化", "?endpoint=172.17.0.2:51820&download=1", "Endpoint = 172.17.0.2:51820"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := getPeerConfig(t, s, peerID, tc.query)
			if !strings.Contains(body, tc.want) {
				t.Fatalf("期望包含 %q，实际：\n%s", tc.want, body)
			}
			if strings.Contains(body, ":51820:") {
				t.Fatalf("端口被重复追加：\n%s", body)
			}
		})
	}
}

// 不给 endpoint 时回退到推断出的公网地址。
// 这条与上一条互为对照：没有 endpoint 就必须补端口，
// 否则导出的配置连 Endpoint 都没有，客户端永远建不起连接。
func TestPeerConfigFallsBackToInferredEndpoint(t *testing.T) {
	s, peerID := newPeerConfigServer(t)

	body := getPeerConfig(t, s, peerID, "")
	if !strings.Contains(body, "Endpoint = vpn.example.com:51820") {
		t.Fatalf("未给 endpoint 时应回退到推断地址，实际：\n%s", body)
	}
}

// getPeerConfig 经 chi 路由打到 handlePeerConfig，保证 {id} 解析路径与线上一致。
func getPeerConfig(t *testing.T, s *Server, peerID int64, query string) string {
	t.Helper()

	r := chi.NewRouter()
	r.Get("/api/v1/peers/{id}/config", func(w http.ResponseWriter, req *http.Request) {
		req = req.WithContext(withPrincipal(req.Context(), &authn.Principal{
			UserID: 1, Username: "admin", Role: domain.RoleAdmin,
		}))
		s.handlePeerConfig(w, req)
	})

	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/peers/"+strconv.FormatInt(peerID, 10)+"/config"+query, nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("导出配置应 200，实际 %d：%s", rec.Code, rec.Body.String())
	}
	// download=1 返回纯文本，其余返回 JSON 包装。
	if strings.HasPrefix(rec.Header().Get("Content-Type"), "text/plain") {
		return rec.Body.String()
	}
	var out struct {
		Data struct {
			Config string `json:"config"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("解析响应失败: %v\n%s", err, rec.Body.String())
	}
	return out.Data.Config
}

// newPeerConfigServer 装配一个只够跑配置导出的最小 Server。
func newPeerConfigServer(t *testing.T) (*Server, int64) {
	t.Helper()
	st, err := store.OpenInMemory()
	if err != nil {
		t.Fatalf("open in-memory store: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	cipher, err := authn.NewCipher([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatalf("new cipher: %v", err)
	}
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

	peers := service.NewPeerService(st.PeerRepo(), st.ServerRepo(), cipher,
		st.AuditRepo(), stubDriver{}, func(context.Context) {})
	p, err := peers.Create(ctx, service.CreateInput{Name: "laptop"},
		&authn.Principal{UserID: 1, Username: "admin", Role: domain.RoleAdmin})
	if err != nil {
		t.Fatalf("create peer: %v", err)
	}

	return &Server{
		log:   slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
		peers: peers,
		endpoint: func() string {
			return "vpn.example.com"
		},
	}, p.ID
}

// stubDriver 不碰内核，只为满足装配。
type stubDriver struct{}

func (stubDriver) Available() error { return nil }
func (stubDriver) Device(context.Context, string) (*wgdriver.DeviceState, error) {
	return nil, nil
}
func (stubDriver) Up(context.Context, *domain.ServerConfig, []domain.Peer) error { return nil }
func (stubDriver) Down(context.Context, string) error                            { return nil }
func (stubDriver) SyncPeers(context.Context, string, []domain.Peer) error        { return nil }
func (stubDriver) PeerStats(string) (map[string]*domain.PeerStats, error)        { return nil, nil }
func (stubDriver) TotalCounters(string) (int64, int64, error)                    { return 0, 0, nil }
