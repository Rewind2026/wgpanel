package service

import (
	"context"
	"encoding/csv"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Rewind2026/wgpanel/internal/authn"
	"github.com/Rewind2026/wgpanel/internal/domain"
	"github.com/Rewind2026/wgpanel/internal/wgdriver"
)

// CSV 导入导出的列定义。
//
// 导出列顺序即导入时的表头顺序，前后端共用这一份约定：
// 用户在面板里导出的文件可以直接改完再导回来，不需要手工对齐列。
var csvHeader = []string{
	"name", "address", "allowedIps", "endpoint", "persistentKeepalive",
	"note", "tags", "trafficQuotaBytes", "quotaPeriod", "rateLimitMbps",
	"maxConnections", "enabled", "expiresAt",
}

// ExportCSV 导出 Peer 明细为 CSV。
//
// 不导出密钥：明文私钥进文件就等于把所有人的隧道凭据落到磁盘上，
// 需要凭据的场景走「导出客户端配置」或订阅链接。
func (s *PeerService) ExportCSV(ctx context.Context) ([]byte, error) {
	peers, err := s.peers.ListAllForExport(ctx)
	if err != nil {
		return nil, err
	}

	var buf strings.Builder
	w := csv.NewWriter(&buf)
	if err := w.Write(csvHeader); err != nil {
		return nil, fmt.Errorf("service: write csv header: %w", err)
	}
	for i := range peers {
		p := peers[i]
		expiresAt := ""
		if p.ExpiresAt != nil {
			expiresAt = p.ExpiresAt.UTC().Format(time.RFC3339)
		}
		row := []string{
			p.Name, p.Address, p.AllowedIPs, p.Endpoint,
			strconv.Itoa(p.PersistentKeepalive), p.Note,
			strings.Join(p.Tags, "|"),
			strconv.FormatInt(p.TrafficQuotaBytes, 10), string(p.QuotaPeriod),
			strconv.Itoa(p.RateLimitMbps), strconv.Itoa(p.MaxConnections),
			strconv.FormatBool(p.Enabled), expiresAt,
		}
		if err := w.Write(row); err != nil {
			return nil, fmt.Errorf("service: write csv row: %w", err)
		}
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return nil, fmt.Errorf("service: flush csv: %w", err)
	}
	return []byte(buf.String()), nil
}

// ImportCSV 从 CSV 批量创建 Peer。
//
// 语义：只要有一行能通过校验就继续处理其余行，逐行返回失败原因，
// 而不是整批回滚。运维一次导入几十个客户端时，
// 「前 47 个成功、第 3 行 IP 写错了」比「全部失败」有用得多。
func (s *PeerService) ImportCSV(ctx context.Context, data []byte, actor *authn.Principal) (domain.ImportResult, error) {
	res := domain.ImportResult{Errors: []domain.ImportRowError{}}

	r := csv.NewReader(strings.NewReader(string(data)))
	r.FieldsPerRecord = -1 // 允许用户手写时列数不齐，由下面的校验负责报错
	records, err := r.ReadAll()
	if err != nil {
		return res, domain.ErrInvalid("csv is malformed: %s", err.Error())
	}
	if len(records) == 0 {
		return res, domain.ErrInvalid("csv is empty")
	}

	header := records[0]
	index, headerErr := mapHeader(header)
	if headerErr != "" {
		return res, domain.ErrInvalid("%s", headerErr)
	}

	for i, rec := range records[1:] {
		line := i + 2
		if isBlankRow(rec) {
			// 空行计入 Skipped：用户导入了 50 行却只看到 47 个 Peer 时，
			// 必须能从返回值判断出另外 3 行是被跳过了，而不是悄悄消失。
			res.Skipped++
			continue
		}
		in, err := parseImportRow(rec, index)
		if err != nil {
			res.Failed++
			res.Errors = append(res.Errors, domain.ImportRowError{Line: line, Reason: err.Error()})
			continue
		}
		if _, err := s.Create(ctx, in, actor); err != nil {
			res.Failed++
			res.Errors = append(res.Errors, domain.ImportRowError{
				Line: line, Name: in.Name, Reason: err.Error(),
			})
			continue
		}
		res.Created++
	}

	if s.audit != nil && res.Created > 0 {
		_ = s.audit.Write(ctx, &domain.AuditEvent{
			ActorID: ptrID(actor), ActorName: actorName(actor), Action: domain.AuditPeerImport,
			Resource: "peer", Result: domain.AuditSuccess,
			Detail: fmt.Sprintf("created=%d failed=%d", res.Created, res.Failed),
		})
	}
	return res, nil
}

// mapHeader 把表头映射为列下标。
func mapHeader(header []string) (map[string]int, string) {
	index := make(map[string]int, len(header))
	for i, h := range header {
		index[strings.ToLower(strings.TrimSpace(h))] = i
	}
	for _, required := range []string{"name"} {
		if _, ok := index[required]; !ok {
			return nil, fmt.Sprintf("csv header must contain a %q column", required)
		}
	}
	return index, ""
}

// parseImportRow 把一行 CSV 转为创建入参。
func parseImportRow(rec []string, index map[string]int) (CreateInput, error) {
	// 表头在建索引时统一转小写，这里也必须小写查询。
	// 否则 quotaPeriod / expiresAt 这类驼峰列名取不到值，
	// 配额、有效期、限速会在导入时静默变成默认值——用户以为导入了，实际没有。
	get := func(name string) string {
		i, ok := index[strings.ToLower(name)]
		if !ok || i >= len(rec) {
			return ""
		}
		return strings.TrimSpace(rec[i])
	}

	name := get("name")
	if name == "" {
		return CreateInput{}, fmt.Errorf("name is empty")
	}
	in := CreateInput{
		Name:                name,
		Address:             get("address"),
		AllowedIPs:          get("allowedIps"),
		Endpoint:            get("endpoint"),
		Note:                get("note"),
		TrafficQuotaBytes:   parseInt64(get("trafficQuotaBytes")),
		QuotaPeriod:         domain.QuotaPeriod(get("quotaPeriod")),
		RateLimitMbps:       parseInt(get("rateLimitMbps")),
		PersistentKeepalive: parseInt(get("persistentKeepalive")),
	}
	// 列位置保留（导入旧 CSV 时不能错位），但值不再落入 Peer：
	// maxConnections 在 WireGuard 协议层无法实现，见 domain.Peer.MaxConnections。
	if tags := get("tags"); tags != "" {
		for _, t := range strings.Split(tags, "|") {
			if t = strings.TrimSpace(t); t != "" {
				in.Tags = append(in.Tags, t)
			}
		}
	}
	if raw := get("enabled"); raw != "" {
		enabled, err := strconv.ParseBool(raw)
		if err != nil {
			return CreateInput{}, fmt.Errorf("enabled must be true or false")
		}
		in.Enabled = &enabled
	}
	if raw := get("expiresAt"); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return CreateInput{}, fmt.Errorf("expiresAt must be RFC3339 formatted")
		}
		in.ExpiresAt = &t
	}
	if in.QuotaPeriod == "" {
		in.QuotaPeriod = domain.QuotaPeriodMonthly
	}
	if !in.QuotaPeriod.Valid() {
		return CreateInput{}, fmt.Errorf("invalid quotaPeriod %q", in.QuotaPeriod)
	}
	return in, nil
}

func isBlankRow(rec []string) bool {
	for _, f := range rec {
		if strings.TrimSpace(f) != "" {
			return false
		}
	}
	return true
}

// parseInt 解析整数，非法值按 0（不限）处理：
// CSV 里填错限速值不应该让整行失败，静默忽略比导入中断更可控。
func parseInt(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

func parseInt64(s string) int64 {
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}

// IssueSubscription 为 Peer 签发订阅令牌并返回订阅链接。
//
// 令牌本身只在签发时返回一次；库里只存明文令牌以便按值查询，
// 因此订阅链接等价于长期凭据，需要撤销时重新签发即可。
func (s *PeerService) IssueSubscription(ctx context.Context, id int64, baseURL, endpoint string,
	actor *authn.Principal) (*domain.SubscriptionLink, error) {
	p, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	token, err := authn.RandomToken(24)
	if err != nil {
		return nil, err
	}
	if err := s.peers.SetSubscriptionToken(ctx, id, token); err != nil {
		return nil, err
	}
	if s.audit != nil {
		_ = s.audit.Write(ctx, &domain.AuditEvent{
			ActorID: ptrID(actor), ActorName: actorName(actor), Action: domain.AuditPeerUpdate,
			Resource: "peer", ResourceID: strconv.FormatInt(id, 10),
			Detail: "subscription issued",
		})
	}
	return &domain.SubscriptionLink{
		PeerID:   p.ID,
		Token:    token,
		URL:      strings.TrimRight(baseURL, "/") + "/api/v1/subscription/" + token,
		IssuedAt: time.Now().UTC().Format(time.RFC3339),
	}, nil
}

// RevokeSubscription 撤销订阅令牌。
func (s *PeerService) RevokeSubscription(ctx context.Context, id int64, actor *authn.Principal) error {
	if err := s.peers.SetSubscriptionToken(ctx, id, ""); err != nil {
		return err
	}
	if s.audit != nil {
		_ = s.audit.Write(ctx, &domain.AuditEvent{
			ActorID: ptrID(actor), ActorName: actorName(actor), Action: domain.AuditPeerUpdate,
			Resource: "peer", ResourceID: strconv.FormatInt(id, 10),
			Detail: "subscription revoked",
		})
	}
	return nil
}

// SubscriptionConfig 按订阅令牌返回客户端配置。
func (s *PeerService) SubscriptionConfig(ctx context.Context, token, endpoint string) (string, error) {
	p, err := s.peers.GetBySubscriptionToken(ctx, token)
	if err != nil {
		return "", err
	}
	server, err := s.servers.Get(ctx)
	if err != nil {
		return "", err
	}
	if err := s.decryptServer(server); err != nil {
		return "", err
	}
	if err := s.decrypt(p); err != nil {
		return "", err
	}
	return wgdriver.RenderPeerConfig(server, p, endpoint), nil
}

// BatchTag 批量追加或移除标签。
func (s *PeerService) BatchTag(ctx context.Context, ids []int64, tag string, remove bool,
	actor *authn.Principal) (int64, error) {
	if strings.TrimSpace(tag) == "" {
		return 0, domain.ErrInvalid("tag must not be empty")
	}
	n, err := s.peers.BatchTag(ctx, ids, strings.TrimSpace(tag), remove)
	if err != nil {
		return 0, err
	}
	if s.audit != nil {
		_ = s.audit.Write(ctx, &domain.AuditEvent{
			ActorID: ptrID(actor), ActorName: actorName(actor), Action: domain.AuditPeerUpdate,
			Resource: "peer", Result: domain.AuditSuccess,
			Detail: fmt.Sprintf("batch tag=%s remove=%t peers=%d changed=%d", tag, remove, len(ids), n),
		})
	}
	s.applyNow(ctx)
	return n, nil
}

// BulkEnable 批量启用或停用 Peer。
//
// 停用走 SetEnabledByPolicy 且 suspended_by 置空：这是人工操作，
// 不能被标记成策略停用，否则配额周期重置会把它自动打开。
func (s *PeerService) BulkEnable(ctx context.Context, ids []int64, enabled bool,
	actor *authn.Principal) (int, error) {
	changed := 0
	for _, id := range ids {
		if err := s.peers.SetEnabledByPolicy(ctx, id, enabled, domain.SuspendedNone); err != nil {
			return changed, err
		}
		changed++
	}
	if s.audit != nil {
		_ = s.audit.Write(ctx, &domain.AuditEvent{
			ActorID: ptrID(actor), ActorName: actorName(actor), Action: domain.AuditPeerUpdate,
			Resource: "peer", Result: domain.AuditSuccess,
			Detail: fmt.Sprintf("bulk enable=%t peers=%d", enabled, changed),
		})
	}
	s.applyNow(ctx)
	return changed, nil
}
