package store

import (
	"context"
	"testing"
)

// TestMigrationDropsFakeCryptoColumn 验证 v4 迁移能把旧库里的 private_key_algorithm 删除。
//
// 这是一次破坏性 schema 变更：迁移失败会让服务在新库上直接启动不了，因此必须覆盖旧库升级路径。
func TestMigrationDropsFakeCryptoColumn(t *testing.T) {
	ctx := context.Background()
	raw, err := sqlOpenMemory()
	if err != nil {
		t.Fatalf("open memory sqlite: %v", err)
	}
	t.Cleanup(func() { raw.Close() })

	// 模拟已存在于生产环境的 v1～v3 数据库：手工建旧表并标记迁移已应用。
	legacy := `
CREATE TABLE server_config (
  id INTEGER PRIMARY KEY CHECK (id = 1),
  interface_name TEXT NOT NULL DEFAULT 'wg0',
  listen_port INTEGER NOT NULL DEFAULT 51820,
  private_key TEXT NOT NULL DEFAULT '',
  public_key TEXT NOT NULL DEFAULT '',
  preshared_key TEXT NOT NULL DEFAULT '',
  address_cidr TEXT NOT NULL DEFAULT '10.8.0.1/24',
  listen_ipv4 TEXT NOT NULL DEFAULT '',
  listen_ipv6 TEXT NOT NULL DEFAULT '',
  mtu INTEGER NOT NULL DEFAULT 1420,
  fwmark INTEGER NOT NULL DEFAULT 0,
  table_name TEXT NOT NULL DEFAULT 'auto',
  allowed_ips TEXT NOT NULL DEFAULT '0.0.0.0/0',
  dns TEXT NOT NULL DEFAULT '',
  private_key_algorithm TEXT NOT NULL DEFAULT 'x25519',
  replace_peers INTEGER NOT NULL DEFAULT 1,
  save_config INTEGER NOT NULL DEFAULT 1,
  enable_ipv6 INTEGER NOT NULL DEFAULT 0,
  persistent_keepalive INTEGER NOT NULL DEFAULT 25,
  post_up TEXT NOT NULL DEFAULT '',
  post_down TEXT NOT NULL DEFAULT '',
  revision INTEGER NOT NULL DEFAULT 1,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
INSERT INTO server_config (id, private_key, public_key, created_at, updated_at)
  VALUES (1, 'priv', 'pub', 1, 1);
CREATE TABLE peer (
  id                      INTEGER PRIMARY KEY AUTOINCREMENT,
  name                    TEXT    NOT NULL,
  public_key              TEXT    NOT NULL UNIQUE,
  private_key             TEXT    NOT NULL DEFAULT '',
  preshared_key           TEXT    NOT NULL DEFAULT '',
  address                 TEXT    NOT NULL,
  allowed_ips             TEXT    NOT NULL DEFAULT '0.0.0.0/0,::/0',
  endpoint                TEXT    NOT NULL DEFAULT '',
  persistent_keepalive    INTEGER NOT NULL DEFAULT 25,
  note                    TEXT    NOT NULL DEFAULT '',
  tags                    TEXT    NOT NULL DEFAULT '[]',
  traffic_quota_bytes     INTEGER NOT NULL DEFAULT 0,
  quota_period            TEXT    NOT NULL DEFAULT 'monthly',
  rate_limit_mbps         INTEGER NOT NULL DEFAULT 0,
  max_connections         INTEGER NOT NULL DEFAULT 0,
  enabled                 INTEGER NOT NULL DEFAULT 1,
  expires_at              INTEGER,
  last_seen_at            INTEGER,
  created_at              INTEGER NOT NULL,
  updated_at              INTEGER NOT NULL
);
INSERT INTO peer (name, public_key, private_key, address, created_at, updated_at)
  VALUES ('legacy', 'legacy-pub', 'legacy-priv', '10.8.0.9/32', 1, 1);
CREATE TABLE user (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  username      TEXT    NOT NULL UNIQUE,
  email         TEXT    NOT NULL DEFAULT '',
  password_hash TEXT    NOT NULL,
  role          TEXT    NOT NULL DEFAULT 'viewer',
  enabled       INTEGER NOT NULL DEFAULT 1,
  last_login_at INTEGER,
  created_at    INTEGER NOT NULL,
  updated_at    INTEGER NOT NULL
);
INSERT INTO user (username, password_hash, role, created_at, updated_at)
  VALUES ('legacy-owner', 'hash', 'owner', 1, 1);
CREATE TABLE schema_migration (version INTEGER PRIMARY KEY, name TEXT NOT NULL, applied_at INTEGER NOT NULL);
INSERT INTO schema_migration (version, name, applied_at) VALUES (1, 'init', 1);
INSERT INTO schema_migration (version, name, applied_at) VALUES (2, 'indexes', 1);
INSERT INTO schema_migration (version, name, applied_at) VALUES (3, 'traffic', 1);

-- v3 声明已应用就必须真的有这两张表。这段 DDL 原本是缺的，
-- 迁移 4~6 恰好都不碰流量表，所以一直没人发现；直到 v7 要给
-- traffic_sample 建索引，缺表才暴露出来。
-- 教训是：夹具不能只声明版本号，必须复现该版本真实留下的结构，
-- 否则「模拟旧库」测的是夹具自己，不是迁移。
CREATE TABLE traffic_sample (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  ts           INTEGER NOT NULL,
  peer_id      INTEGER,
  rx_bytes     INTEGER NOT NULL DEFAULT 0,
  tx_bytes     INTEGER NOT NULL DEFAULT 0,
  rx_pps       INTEGER NOT NULL DEFAULT 0,
  tx_pps       INTEGER NOT NULL DEFAULT 0,
  handshake_ts INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE traffic_rollup (
  bucket    TEXT    NOT NULL,
  ts        INTEGER NOT NULL,
  peer_id   INTEGER NOT NULL DEFAULT 0,
  rx_bytes  INTEGER NOT NULL DEFAULT 0,
  tx_bytes  INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (bucket, ts, peer_id)
);
INSERT INTO traffic_sample (ts, peer_id, rx_bytes, tx_bytes)
  VALUES (1700000000, 0, 100, 200);
INSERT INTO traffic_sample (ts, peer_id, rx_bytes, tx_bytes)
  VALUES (1700000000, NULL, 300, 400);
`
	if _, err := raw.Exec(legacy); err != nil {
		t.Fatalf("build legacy schema: %v", err)
	}

	s := NewWithDB(raw)
	if err := s.migrate(ctx); err != nil {
		t.Fatalf("migrate legacy database: %v", err)
	}

	var n int
	err = raw.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('server_config') WHERE name = 'private_key_algorithm'`).Scan(&n)
	if err != nil {
		t.Fatalf("inspect columns: %v", err)
	}
	if n != 0 {
		t.Error("旧库升级后不应再存在 private_key_algorithm 列")
	}
	for _, col := range []string{"post_up", "post_down"} {
		if err := raw.QueryRow(
			`SELECT COUNT(*) FROM pragma_table_info('server_config') WHERE name = ?`, col,
		).Scan(&n); err != nil {
			t.Fatalf("inspect column %s: %v", col, err)
		}
		if n != 0 {
			t.Errorf("旧库升级后不应再存在 %s 列", col)
		}
	}

	// 其余业务数据必须在迁移后仍然可读。
	cfg, err := s.ServerRepo().Get(ctx)
	if err != nil {
		t.Fatalf("Get after migration: %v", err)
	}
	if cfg.PublicKey != "pub" || cfg.Interface != "wg0" {
		t.Errorf("迁移破坏了已有数据：%+v", cfg)
	}

	// v5 给 peer 追加的列必须有默认值：老客户端没有这些字段，
	// 若缺省值不对，升级后所有 Peer 会凭空出现「已用配额」而被停用。
	var quotaUsed int64
	var suspended string
	var legacyID int64
	err = raw.QueryRow(`SELECT id, quota_used_bytes, suspended_by FROM peer WHERE name = 'legacy'`).
		Scan(&legacyID, &quotaUsed, &suspended)
	if err != nil {
		t.Fatalf("read legacy peer after migration: %v", err)
	}
	if quotaUsed != 0 || suspended != "" {
		t.Errorf("新增列的默认值不正确：quota_used=%d suspended_by=%q", quotaUsed, suspended)
	}
	if _, err := s.PeerRepo().Get(ctx, legacyID); err != nil {
		t.Errorf("迁移后旧 Peer 不可读：%v", err)
	}

	// v7 建的索引必须落在旧库上，且旧库里 peer_id IS NULL 的历史行
	// 仍要能被 COALESCE 归一后查到——表达式索引最容易在这里出问题：
	// 索引建上了但历史 NULL 行查不出来，等于旧数据在曲线上凭空消失。
	for _, idx := range []string{"idx_traffic_sample_ts", "idx_traffic_sample_peer_ts"} {
		var n int
		if err := raw.QueryRow(
			`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name = ?`, idx,
		).Scan(&n); err != nil {
			t.Fatalf("inspect index %s: %v", idx, err)
		}
		if n != 1 {
			t.Errorf("旧库升级后应建出索引 %s，实际未建出", idx)
		}
	}
	pts, err := s.TrafficRepo().QueryTraffic(ctx, 1699999999, 1700000001, "raw", nil)
	if err != nil {
		t.Fatalf("查询旧库流量：%v", err)
	}
	// peer_id = 0 与 peer_id IS NULL 两行都应归一到隧道汇总。
	if len(pts) != 2 {
		t.Errorf("旧库 NULL peer_id 行应被 COALESCE 归一后一并查出，实际 %d 行", len(pts))
	}

	// v8 给 user 追加的 token_version 必须存在且默认为 0：
	// 缺列会让 userColumns 的查询直接报错，缺默认值会让旧用户的
	// token_version 变成 NULL，鉴权时的 ver 比对随之失效。
	var tokenVersion int64
	if err := raw.QueryRow(
		`SELECT token_version FROM user WHERE username = 'legacy-owner'`).Scan(&tokenVersion); err != nil {
		t.Fatalf("read legacy user token_version: %v", err)
	}
	if tokenVersion != 0 {
		t.Errorf("token_version 默认值应为 0，实际 %d", tokenVersion)
	}
}

// TestMigrateIsIdempotent 重复执行迁移不应报错。
func TestMigrateIsIdempotent(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	if err := s.migrate(ctx); err != nil {
		t.Fatalf("second migrate: %v", err)
	}

	var applied int
	if err := s.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migration`).Scan(&applied); err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	if applied != len(migrations) {
		t.Errorf("应有 %d 条迁移记录，实际 %d", len(migrations), applied)
	}
}
