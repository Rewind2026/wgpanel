package httpapi

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Rewind2026/wgpanel/internal/domain"
	"github.com/Rewind2026/wgpanel/internal/service"
)

type serverPayload struct {
	Interface           string `json:"interface"`
	ListenPort          int    `json:"listenPort"`
	PrivateKey          string `json:"privateKey,omitempty"`
	PublicKey           string `json:"publicKey"`
	PresharedKey        string `json:"presharedKey,omitempty"`
	AddressCIDR         string `json:"addressCidr"`
	MTU                 int    `json:"mtu"`
	FwMark              int    `json:"fwmark"`
	Table               string `json:"table"`
	AllowedIPs          string `json:"allowedIps"`
	DNS                 string `json:"dns"`
	ListenIPv4          string `json:"listenIpv4"`
	ListenIPv6          string `json:"listenIpv6"`
	ReplacePeers        bool   `json:"replacePeers"`
	SaveConfig          bool   `json:"saveConfig"`
	EnableIPv6          bool   `json:"enableIpv6"`
	PersistentKeepalive int    `json:"persistentKeepalive"`
	Revision            int64  `json:"revision"`
}

// handleGetServer 返回服务端配置。只读角色看到脱敏后的密钥。
func (s *Server) handleGetServer(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	var (
		cfg *domain.ServerConfig
		err error
	)
	if p.Can(domain.PermServerWrite) {
		cfg, err = s.servers.Get(r.Context())
	} else {
		cfg, err = s.servers.GetRedacted(r.Context())
	}
	if err != nil {
		respondError(w, s.log, err)
		return
	}
	respondJSON(w, http.StatusOK, serverPayload{
		Interface:           cfg.Interface,
		ListenPort:          cfg.ListenPort,
		PrivateKey:          cfg.PrivateKey,
		PublicKey:           cfg.PublicKey,
		PresharedKey:        cfg.PresharedKey,
		AddressCIDR:         cfg.AddressCIDR,
		MTU:                 cfg.MTU,
		FwMark:              cfg.FwMark,
		Table:               cfg.Table,
		AllowedIPs:          cfg.AllowedIPs,
		DNS:                 cfg.DNS,
		ListenIPv4:          cfg.ListenIPv4,
		ListenIPv6:          cfg.ListenIPv6,
		ReplacePeers:        cfg.ReplacePeers,
		SaveConfig:          cfg.SaveConfig,
		EnableIPv6:          cfg.EnableIPv6,
		PersistentKeepalive: cfg.PersistentKeepalive,
		Revision:            cfg.Revision,
	})
}

// handleUpdateServer 应用全量参数更新。
func (s *Server) handleUpdateServer(w http.ResponseWriter, r *http.Request) {
	var in serverPayload
	if err := decodeJSON(r, &in); err != nil {
		respondError(w, s.log, err)
		return
	}
	updated, err := s.servers.Update(r.Context(), domain.ServerConfig{
		Interface:           in.Interface,
		ListenPort:          in.ListenPort,
		PrivateKey:          in.PrivateKey,
		PresharedKey:        in.PresharedKey,
		AddressCIDR:         in.AddressCIDR,
		MTU:                 in.MTU,
		FwMark:              in.FwMark,
		Table:               in.Table,
		AllowedIPs:          in.AllowedIPs,
		DNS:                 in.DNS,
		ListenIPv4:          in.ListenIPv4,
		ListenIPv6:          in.ListenIPv6,
		ReplacePeers:        in.ReplacePeers,
		SaveConfig:          in.SaveConfig,
		EnableIPv6:          in.EnableIPv6,
		PersistentKeepalive: in.PersistentKeepalive,
	}, principalFrom(r.Context()))
	if err != nil {
		respondError(w, s.log, err)
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"revision": updated.Revision})
}

// handleRotateServerKey 重置服务端私钥。
func (s *Server) handleRotateServerKey(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.servers.RotateKey(r.Context(), principalFrom(r.Context()))
	if err != nil {
		respondError(w, s.log, err)
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{
		"publicKey": cfg.PublicKey,
		"revision":  cfg.Revision,
		"notice":    "所有客户端需要重新导入配置",
	})
}

// handleServerRevisions 返回配置版本历史。
func (s *Server) handleServerRevisions(w http.ResponseWriter, r *http.Request) {
	limit := 20
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 100 {
			limit = n
		}
	}
	items, err := s.servers.Revisions(r.Context(), limit)
	if err != nil {
		respondError(w, s.log, err)
		return
	}
	type rev struct {
		Revision   int64     `json:"revision"`
		Interface  string    `json:"interface"`
		ListenPort int       `json:"listenPort"`
		MTU        int       `json:"mtu"`
		UpdatedAt  time.Time `json:"updatedAt"`
	}
	out := make([]rev, 0, len(items))
	for _, c := range items {
		out = append(out, rev{c.Revision, c.Interface, c.ListenPort, c.MTU, c.UpdatedAt})
	}
	respondJSON(w, http.StatusOK, out)
}

type createPeerRequest struct {
	Name                string   `json:"name"`
	Address             string   `json:"address"`
	AllowedIPs          string   `json:"allowedIps"`
	Endpoint            string   `json:"endpoint"`
	PresharedKey        string   `json:"presharedKey"`
	PersistentKeepalive int      `json:"persistentKeepalive"`
	Note                string   `json:"note"`
	Tags                []string `json:"tags"`
	TrafficQuotaBytes   int64    `json:"trafficQuotaBytes"`
	QuotaPeriod         string   `json:"quotaPeriod"`
	RateLimitMbps       int      `json:"rateLimitMbps"`
	ExpiresAt           *string  `json:"expiresAt"`
	Enabled             *bool    `json:"enabled"`
}

// handleListPeers 返回 Peer 列表（含实时统计）。
func (s *Server) handleListPeers(w http.ResponseWriter, r *http.Request) {
	peers, err := s.peers.List(r.Context())
	if err != nil {
		respondError(w, s.log, err)
		return
	}
	respondJSON(w, http.StatusOK, peers)
}

// handleCreatePeer 创建 Peer。
func (s *Server) handleCreatePeer(w http.ResponseWriter, r *http.Request) {
	var in createPeerRequest
	if err := decodeJSON(r, &in); err != nil {
		respondError(w, s.log, err)
		return
	}
	expiresAt, err := parseOptionalTime(in.ExpiresAt)
	if err != nil {
		respondError(w, s.log, err)
		return
	}
	p, err := s.peers.Create(r.Context(), service.CreateInput{
		Name:                in.Name,
		Address:             in.Address,
		AllowedIPs:          in.AllowedIPs,
		Endpoint:            in.Endpoint,
		PresharedKey:        in.PresharedKey,
		PersistentKeepalive: in.PersistentKeepalive,
		Note:                in.Note,
		Tags:                in.Tags,
		TrafficQuotaBytes:   in.TrafficQuotaBytes,
		QuotaPeriod:         domain.QuotaPeriod(in.QuotaPeriod),
		RateLimitMbps:       in.RateLimitMbps,
		ExpiresAt:           expiresAt,
		Enabled:             in.Enabled,
	}, principalFrom(r.Context()))
	if err != nil {
		respondError(w, s.log, err)
		return
	}
	respondJSON(w, http.StatusCreated, p)
}

type updatePeerRequest struct {
	Name                *string   `json:"name"`
	Address             *string   `json:"address"`
	AllowedIPs          *string   `json:"allowedIps"`
	Endpoint            *string   `json:"endpoint"`
	PresharedKey        *string   `json:"presharedKey"`
	PersistentKeepalive *int      `json:"persistentKeepalive"`
	Note                *string   `json:"note"`
	Tags                *[]string `json:"tags"`
	TrafficQuotaBytes   *int64    `json:"trafficQuotaBytes"`
	QuotaPeriod         *string   `json:"quotaPeriod"`
	RateLimitMbps       *int      `json:"rateLimitMbps"`
	Enabled             *bool     `json:"enabled"`
	ExpiresAt           *string   `json:"expiresAt"`
	RotateKey           bool      `json:"rotateKey"`
}

// handleUpdatePeer 更新 Peer。
func (s *Server) handleUpdatePeer(w http.ResponseWriter, r *http.Request) {
	id, err := peerID(r)
	if err != nil {
		respondError(w, s.log, err)
		return
	}
	var in updatePeerRequest
	if err := decodeJSON(r, &in); err != nil {
		respondError(w, s.log, err)
		return
	}
	expiresAt, err := parseOptionalTime(in.ExpiresAt)
	if err != nil {
		respondError(w, s.log, err)
		return
	}
	var period *domain.QuotaPeriod
	if in.QuotaPeriod != nil {
		p := domain.QuotaPeriod(*in.QuotaPeriod)
		period = &p
	}
	p, err := s.peers.Update(r.Context(), id, service.UpdateInput{
		Name:                in.Name,
		Address:             in.Address,
		AllowedIPs:          in.AllowedIPs,
		Endpoint:            in.Endpoint,
		PresharedKey:        in.PresharedKey,
		PersistentKeepalive: in.PersistentKeepalive,
		Note:                in.Note,
		Tags:                in.Tags,
		TrafficQuotaBytes:   in.TrafficQuotaBytes,
		QuotaPeriod:         period,
		RateLimitMbps:       in.RateLimitMbps,
		Enabled:             in.Enabled,
		ExpiresAt:           expiresAt,
		RotateKey:           in.RotateKey,
	}, principalFrom(r.Context()))
	if err != nil {
		respondError(w, s.log, err)
		return
	}
	respondJSON(w, http.StatusOK, p)
}

// handleDeletePeer 删除 Peer。
func (s *Server) handleDeletePeer(w http.ResponseWriter, r *http.Request) {
	id, err := peerID(r)
	if err != nil {
		respondError(w, s.log, err)
		return
	}
	if err := s.peers.Delete(r.Context(), id, principalFrom(r.Context())); err != nil {
		respondError(w, s.log, err)
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"deleted": id})
}

// handleRotatePeerKey 轮换 Peer 密钥。
func (s *Server) handleRotatePeerKey(w http.ResponseWriter, r *http.Request) {
	id, err := peerID(r)
	if err != nil {
		respondError(w, s.log, err)
		return
	}
	p, err := s.peers.RotateKey(r.Context(), id, principalFrom(r.Context()))
	if err != nil {
		respondError(w, s.log, err)
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"peerId": p.ID, "publicKey": p.PublicKey})
}

type bulkRevokeRequest struct {
	IDs []int64 `json:"ids"`
	// All 吊销全部 Peer。密钥泄露应急时，勾选全选仍可能漏掉
	// 列表没加载出来的那几台，所以「全量吊销」必须是显式意图而不是
	// 前端把当前页勾选项原样传上来。
	All bool `json:"all"`
}

// handleBulkRevoke 批量轮换密钥。
func (s *Server) handleBulkRevoke(w http.ResponseWriter, r *http.Request) {
	var in bulkRevokeRequest
	if err := decodeJSON(r, &in); err != nil {
		respondError(w, s.log, err)
		return
	}
	var (
		n   int
		err error
	)
	if in.All {
		n, err = s.peers.BulkRevokeAll(r.Context(), principalFrom(r.Context()))
	} else {
		if len(in.IDs) == 0 {
			respondError(w, s.log, domain.ErrInvalid("ids must not be empty"))
			return
		}
		n, err = s.peers.BulkRevoke(r.Context(), in.IDs, principalFrom(r.Context()))
	}
	if err != nil {
		respondError(w, s.log, err)
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"revoked": n})
}

// handlePeerConfig 导出单个客户端配置。
func (s *Server) handlePeerConfig(w http.ResponseWriter, r *http.Request) {
	id, err := peerID(r)
	if err != nil {
		respondError(w, s.log, err)
		return
	}
	// 查询参数优先，方便管理员临时指定别的地址导出；
	// 没给（或给了空串）时回退到服务端推断出的公网地址。
	//
	// 不回退的后果很隐蔽：导出的是一个没有 Endpoint 的配置，
	// wg-quick 能解析、界面也显示正常，但客户端永远建不起连接，
	// 用户只能自己去配置里手填——而面板没有任何地方提示这一点。
	endpoint := strings.TrimSpace(r.URL.Query().Get("endpoint"))
	if endpoint == "" {
		endpoint = s.publicEndpoint()
	}
	conf, err := s.peers.ClientConfig(r.Context(), id, endpoint)
	if err != nil {
		respondError(w, s.log, err)
		return
	}
	if r.URL.Query().Get("download") == "1" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Content-Disposition", "attachment; filename=wg0.conf")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(conf))
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"config": conf})
}

// handleServerConfigText 导出服务端 wg 配置全文。
func (s *Server) handleServerConfigText(w http.ResponseWriter, r *http.Request) {
	conf, err := s.peers.ServerConfigText(r.Context())
	if err != nil {
		respondError(w, s.log, err)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if r.URL.Query().Get("download") == "1" {
		w.Header().Set("Content-Disposition", "attachment; filename=wgpanel.conf")
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(conf))
}

// parseOptionalTime 解析可选的 RFC3339 时间字符串。
func parseOptionalTime(v *string) (*time.Time, error) {
	if v == nil || *v == "" {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339, *v)
	if err != nil {
		return nil, domain.ErrInvalid("expiresAt must be RFC3339 formatted")
	}
	return &t, nil
}
