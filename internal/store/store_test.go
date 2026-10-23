package store

import (
	"context"
	"testing"

	"github.com/Rewind2026/wgpanel/internal/domain"
)

// newTestStore 用内存数据库构造 Store。
func newTestStore(t *testing.T) *Store {
	t.Helper()
	raw, err := sqlOpenMemory()
	if err != nil {
		t.Fatalf("open in-memory sqlite: %v", err)
	}
	s := NewWithDB(raw)
	if err := s.migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { raw.Close() })
	return s
}

func TestPeerRepoCRUDRoundTrip(t *testing.T) {
	ctx := context.Background()
	repo := newTestStore(t).PeerRepo()

	peer := &domain.Peer{
		Name:                "laptop",
		PublicKey:           "dpmLZIKxpezJZNlZwlv6pLg8goGW8C6jasftaRxv8yQ=",
		PrivateKey:          "4I8V/IGyK1AnVuQtPUN0nmelG0DXNPGTHDR2aRFnRlI=",
		Address:             "10.8.0.2/32",
		AllowedIPs:          "0.0.0.0/0, ::/0",
		PersistentKeepalive: 25,
		Tags:                []string{"work"},
		TrafficQuotaBytes:   1024,
		QuotaPeriod:         domain.QuotaPeriodMonthly,
		Enabled:             true,
	}
	peer.Normalize()

	id, err := repo.Create(ctx, peer)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if id <= 0 {
		t.Fatalf("Create returned id=%d, want a positive id", id)
	}

	got, err := repo.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Name != "laptop" || got.Address != "10.8.0.2/32" {
		t.Fatalf("Get returned %+v", got)
	}
	if !got.Enabled {
		t.Fatal("Enabled should round-trip as true")
	}
	if len(got.Tags) != 1 || got.Tags[0] != "work" {
		t.Fatalf("Tags = %v, want [work]", got.Tags)
	}
	if got.CreatedAt.Unix() <= 0 || got.UpdatedAt.Unix() <= 0 {
		t.Fatalf("timestamps not populated: created=%v updated=%v", got.CreatedAt, got.UpdatedAt)
	}

	got.Note = "edited"
	got.Enabled = false
	if err := repo.Update(ctx, got); err != nil {
		t.Fatalf("Update: %v", err)
	}
	reloaded, err := repo.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get after Update: %v", err)
	}
	if reloaded.Note != "edited" || reloaded.Enabled {
		t.Fatalf("Update not persisted: %+v", reloaded)
	}

	if err := repo.Delete(ctx, id); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := repo.Get(ctx, id); domain.AsError(err).Code != domain.CodeNotFound {
		t.Fatalf("expected NotFound after delete, got %v", err)
	}
}

func TestPeerRepoRejectsDuplicateAddress(t *testing.T) {
	ctx := context.Background()
	repo := newTestStore(t).PeerRepo()

	first := &domain.Peer{
		Name: "a", PublicKey: "k1", Address: "10.8.0.5/32", AllowedIPs: "0.0.0.0/0",
	}
	first.Normalize()
	if _, err := repo.Create(ctx, first); err != nil {
		t.Fatalf("Create first: %v", err)
	}

	second := &domain.Peer{
		Name: "b", PublicKey: "k2", Address: "10.8.0.5/32", AllowedIPs: "0.0.0.0/0",
	}
	second.Normalize()
	_, err := repo.Create(ctx, second)
	if domain.AsError(err).Code != domain.CodeConflict {
		t.Fatalf("expected Conflict for duplicate address, got %v", err)
	}
}

func TestServerRepoSaveCreatesRevisionHistory(t *testing.T) {
	ctx := context.Background()
	repo := newTestStore(t).ServerRepo()

	cfg := &domain.ServerConfig{
		Interface: "wg0", ListenPort: 51820, PrivateKey: "priv", PublicKey: "pub",
		AddressCIDR: "10.8.0.1/24", MTU: 1420, Table: "auto",
		AllowedIPs: "0.0.0.0/0",
	}
	cfg.Normalize()
	if err := repo.Save(ctx, cfg, nil); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if cfg.Revision != 1 {
		t.Fatalf("first Save revision = %d, want 1", cfg.Revision)
	}

	cfg.MTU = 1280
	if err := repo.Save(ctx, cfg, nil); err != nil {
		t.Fatalf("Save second: %v", err)
	}
	if cfg.Revision != 2 {
		t.Fatalf("second Save revision = %d, want 2", cfg.Revision)
	}

	loaded, err := repo.Get(ctx)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if loaded.MTU != 1280 {
		t.Fatalf("MTU = %d, want 1280", loaded.MTU)
	}

	history, err := repo.ListRevisions(ctx, 10)
	if err != nil {
		t.Fatalf("ListRevisions: %v", err)
	}
	if len(history) != 2 {
		t.Fatalf("history length = %d, want 2", len(history))
	}
	restored, err := repo.GetRevision(ctx, 1)
	if err != nil {
		t.Fatalf("GetRevision: %v", err)
	}
	if restored.MTU != 1420 {
		t.Fatalf("revision 1 MTU = %d, want 1420", restored.MTU)
	}
}

func TestUserRepoCreateAndLookup(t *testing.T) {
	ctx := context.Background()
	repo := newTestStore(t).UserRepo()

	u := &domain.User{
		Username: "admin", PasswordHash: "hash", Role: domain.RoleOwner, Enabled: true,
	}
	id, err := repo.Create(ctx, u)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if id <= 0 {
		t.Fatalf("Create id = %d", id)
	}

	found, err := repo.GetByUsername(ctx, "admin")
	if err != nil {
		t.Fatalf("GetByUsername: %v", err)
	}
	if found.Role != domain.RoleOwner || !found.Enabled {
		t.Fatalf("unexpected user %+v", found)
	}

	dup := &domain.User{Username: "admin", PasswordHash: "x", Role: domain.RoleViewer, Enabled: true}
	if _, err := repo.Create(ctx, dup); domain.AsError(err).Code != domain.CodeConflict {
		t.Fatalf("expected Conflict on duplicate username, got %v", err)
	}
}

func TestRefreshTokenIsSingleUse(t *testing.T) {
	ctx := context.Background()
	repo := newTestStore(t).UserRepo()

	u := &domain.User{Username: "bob", PasswordHash: "h", Role: domain.RoleViewer, Enabled: true}
	if _, err := repo.Create(ctx, u); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := repo.StoreRefreshToken(ctx, u.ID, "token-1", 1<<40); err != nil {
		t.Fatalf("StoreRefreshToken: %v", err)
	}

	uid, err := repo.ConsumeRefreshToken(ctx, "token-1")
	if err != nil {
		t.Fatalf("first Consume: %v", err)
	}
	if uid != u.ID {
		t.Fatalf("user id = %d, want %d", uid, u.ID)
	}
	if _, err := repo.ConsumeRefreshToken(ctx, "token-1"); err == nil {
		t.Fatal("refresh token must not be reusable")
	}
}

func TestTrafficRollupAndQuery(t *testing.T) {
	ctx := context.Background()
	repo := newTestStore(t).TrafficRepo()

	now := int64(1_700_000_000)
	samples := []domain.TrafficSample{
		{TS: now - 20, RxBytes: 100, TxBytes: 10},
		{TS: now - 10, RxBytes: 300, TxBytes: 30},
		{TS: now, RxBytes: 600, TxBytes: 60},
	}
	if err := repo.InsertSamples(ctx, samples); err != nil {
		t.Fatalf("InsertSamples: %v", err)
	}

	points, err := repo.QueryTraffic(ctx, now-60, now+60, "raw", nil)
	if err != nil {
		t.Fatalf("QueryTraffic: %v", err)
	}
	if len(points) != 3 {
		t.Fatalf("points = %d, want 3", len(points))
	}
	if points[0].RxRate != 0 || points[1].RxRate != 20 || points[2].RxRate != 30 {
		t.Fatalf("unexpected rates: %+v", points)
	}

	if err := repo.Rollup(ctx, "10m", now-3600, 600); err != nil {
		t.Fatalf("Rollup: %v", err)
	}
	rollups, err := repo.QueryTraffic(ctx, now-3600, now+600, "10m", nil)
	if err != nil {
		t.Fatalf("QueryTraffic rollup: %v", err)
	}
	if len(rollups) != 1 {
		t.Fatalf("rollup points = %d, want 1", len(rollups))
	}
	if rollups[0].RxBytes != 600 {
		t.Fatalf("rollup rx = %d, want 600", rollups[0].RxBytes)
	}
}
