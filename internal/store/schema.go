package store

// schemaInit 是首批核心表结构。
const schemaInit = `
CREATE TABLE IF NOT EXISTS server_config (
  id                     INTEGER PRIMARY KEY CHECK (id = 1),
  interface_name         TEXT    NOT NULL DEFAULT 'wg0',
  listen_port            INTEGER NOT NULL DEFAULT 51820,
  private_key            TEXT    NOT NULL DEFAULT '',
  public_key             TEXT    NOT NULL DEFAULT '',
  preshared_key          TEXT    NOT NULL DEFAULT '',
  address_cidr           TEXT    NOT NULL DEFAULT '10.8.0.1/24',
  listen_ipv4            TEXT    NOT NULL DEFAULT '',
  listen_ipv6            TEXT    NOT NULL DEFAULT '',
  mtu                    INTEGER NOT NULL DEFAULT 1420,
  fwmark                 INTEGER NOT NULL DEFAULT 0,
  table_name             TEXT    NOT NULL DEFAULT 'auto',
  allowed_ips            TEXT    NOT NULL DEFAULT '0.0.0.0/0',
  dns                    TEXT    NOT NULL DEFAULT '',
  private_key_algorithm  TEXT    NOT NULL DEFAULT 'x25519',
  replace_peers          INTEGER NOT NULL DEFAULT 1,
  save_config            INTEGER NOT NULL DEFAULT 1,
  enable_ipv6            INTEGER NOT NULL DEFAULT 0,
  persistent_keepalive   INTEGER NOT NULL DEFAULT 25,
  post_up                TEXT    NOT NULL DEFAULT '',
  post_down              TEXT    NOT NULL DEFAULT '',
  revision               INTEGER NOT NULL DEFAULT 1,
  created_at             INTEGER NOT NULL,
  updated_at             INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS server_config_revision (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  revision    INTEGER NOT NULL,
  snapshot    TEXT    NOT NULL,
  operator_id INTEGER,
  created_at  INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS peer (
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

CREATE TABLE IF NOT EXISTS user (
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

CREATE TABLE IF NOT EXISTS refresh_token (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id    INTEGER NOT NULL REFERENCES user(id) ON DELETE CASCADE,
  token_hash TEXT    NOT NULL UNIQUE,
  expires_at INTEGER NOT NULL,
  revoked    INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS system_metric (
  ts             INTEGER PRIMARY KEY,
  cpu_percent    REAL    NOT NULL DEFAULT 0,
  mem_used_bytes INTEGER NOT NULL DEFAULT 0,
  mem_percent    REAL    NOT NULL DEFAULT 0,
  disk_used_bytes INTEGER NOT NULL DEFAULT 0,
  disk_percent   REAL    NOT NULL DEFAULT 0,
  net_rx_bytes   INTEGER NOT NULL DEFAULT 0,
  net_tx_bytes   INTEGER NOT NULL DEFAULT 0,
  uptime_seconds INTEGER NOT NULL DEFAULT 0,
  goroutines     INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS audit_log (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  actor_id    INTEGER,
  actor_name  TEXT    NOT NULL DEFAULT '',
  action      TEXT    NOT NULL,
  resource    TEXT    NOT NULL DEFAULT '',
  resource_id TEXT    NOT NULL DEFAULT '',
  detail      TEXT    NOT NULL DEFAULT '',
  ip          TEXT    NOT NULL DEFAULT '',
  ua          TEXT    NOT NULL DEFAULT '',
  result      TEXT    NOT NULL DEFAULT 'success',
  created_at  INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS config_snapshot (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  kind        TEXT    NOT NULL,
  payload     BLOB    NOT NULL,
  size        INTEGER NOT NULL DEFAULT 0,
  operator_id INTEGER,
  created_at  INTEGER NOT NULL
);
`

// schemaIndexes 集中管理查询索引。
const schemaIndexes = `
CREATE INDEX IF NOT EXISTS idx_peer_enabled       ON peer(enabled);
CREATE INDEX IF NOT EXISTS idx_peer_created_at    ON peer(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_peer_expires_at    ON peer(expires_at);
CREATE UNIQUE INDEX IF NOT EXISTS idx_peer_address    ON peer(address);

CREATE INDEX IF NOT EXISTS idx_refresh_token_user ON refresh_token(user_id);
CREATE INDEX IF NOT EXISTS idx_refresh_token_exp  ON refresh_token(expires_at);

CREATE INDEX IF NOT EXISTS idx_server_revision_rev ON server_config_revision(revision DESC);
`

// schemaTraffic 是流量时序相关表。
const schemaTraffic = `
CREATE TABLE IF NOT EXISTS traffic_sample (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  ts           INTEGER NOT NULL,
  peer_id      INTEGER,
  rx_bytes     INTEGER NOT NULL DEFAULT 0,
  tx_bytes     INTEGER NOT NULL DEFAULT 0,
  rx_pps       INTEGER NOT NULL DEFAULT 0,
  tx_pps       INTEGER NOT NULL DEFAULT 0,
  handshake_ts INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS traffic_rollup (
  bucket    TEXT    NOT NULL,
  ts        INTEGER NOT NULL,
  peer_id   INTEGER NOT NULL DEFAULT 0,
  rx_bytes  INTEGER NOT NULL DEFAULT 0,
  tx_bytes  INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (bucket, ts, peer_id)
);
`

// schemaV8 给 user 增加 token_version。
//
// 用途：让「强制下线」真正作废已签发的 access token。
// access token 是无状态 JWT，服务端不存它的任何记录，
// 单靠吊销 refresh token 只能阻止「续期」，管不住「已签发」的那一张。
// token_version 是用户级的会话代号：提升它之后，
// 此前签发的所有 access token（ver 落后）一律判为失效。
const schemaV8 = `
ALTER TABLE user ADD COLUMN token_version INTEGER NOT NULL DEFAULT 0;
`

// schemaV4 移除两处不成立的可配置项。
//
// private_key_algorithm 看似能配置加密套件，但 WireGuard 的密钥与套件由内核固定为
// Curve25519 + ChaCha20-Poly1305，没有任何运行时开关。
//
// post_up / post_down 看似能配置上下线钩子，但本面板通过 netlink 与固定参数的
// iptables 调用完成网络配置，不经过 shell；执行用户输入的命令等同于把 root shell
// 交给任何能改服务端配置的角色。保留一个可写却从不执行的字段比直接删除更危险。
//
// 新库直接不含这些列；旧库在本次迁移中删除。
const schemaV4 = `
ALTER TABLE server_config DROP COLUMN private_key_algorithm;
ALTER TABLE server_config DROP COLUMN post_up;
ALTER TABLE server_config DROP COLUMN post_down;
`

// schemaPolicy 落地 M4/M5 的策略与运维能力。
//
// 追加而非改写历史迁移：已部署的库必须能沿着 v1→vN 顺序升级。
const schemaPolicy = `
ALTER TABLE peer ADD COLUMN quota_used_bytes INTEGER NOT NULL DEFAULT 0;
ALTER TABLE peer ADD COLUMN suspended_by      TEXT    NOT NULL DEFAULT '';
ALTER TABLE peer ADD COLUMN subscription_token TEXT   NOT NULL DEFAULT '';

CREATE TABLE IF NOT EXISTS app_setting (
  key        TEXT PRIMARY KEY,
  value      TEXT    NOT NULL,
  updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS webhook (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  name       TEXT    NOT NULL,
  kind       TEXT    NOT NULL DEFAULT 'generic',
  url        TEXT    NOT NULL,
  events     TEXT    NOT NULL DEFAULT '[]',
  enabled    INTEGER NOT NULL DEFAULT 1,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS backup_record (
  id             INTEGER PRIMARY KEY AUTOINCREMENT,
  filename       TEXT    NOT NULL,
  size_bytes     INTEGER NOT NULL DEFAULT 0,
  sha256         TEXT    NOT NULL DEFAULT '',
  schema_version INTEGER NOT NULL DEFAULT 0,
  peer_count     INTEGER NOT NULL DEFAULT 0,
  user_count     INTEGER NOT NULL DEFAULT 0,
  kind           TEXT    NOT NULL DEFAULT 'manual',
  operator_id    INTEGER,
  created_at     INTEGER NOT NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_peer_sub_token ON peer(subscription_token)
  WHERE subscription_token <> '';
`

// schemaTrafficIndex 补上 traffic_sample 的查询索引。
//
// 为什么必须补：traffic_sample 每秒写入「1 行隧道汇总 + 每 Peer 1 行」，
// 240 Peer 即 241 行/秒，24 小时保留期对应约 2000 万行。这张表原本没有任何索引，
// 于是两条最关键的路径都是全表扫描：
//
//   - 曲线查询 queryRaw：SCAN traffic_sample + USE TEMP B-TREE FOR ORDER BY。
//     实测行数 2.9 万 → 14.5 万时单次查询 5ms → 20ms，纯线性增长且无收敛。
//     跑满一天后表里两千万行，监控页每次刷新都要扫全表，页面直接卡死。
//   - 保留策略清理 DELETE FROM traffic_sample WHERE ts < ?：同样 SCAN，
//     每小时跑一次，耗时随时长线性增长。
//
// 「SQLite 文件增长可预期」靠的是保留策略，磁盘不会满；但增长带来的
// 读放大与清理成本原本完全不可预期——磁盘还剩 80%，页面先卡死了。
//
// 索引形状说明：
//   - idx_traffic_sample_ts 单列 ts，服务清理与「不按 peer 归因」的查询；
//   - idx_traffic_sample_peer_ts 用表达式 (COALESCE(peer_id, 0), ts)。
//     peer_id 是可空列（历史行可能为 NULL），查询侧统一用
//     COALESCE(peer_id, 0) 归一，普通索引 (peer_id, ts) 不会被这种写法用上，
//     必须把表达式原样写进索引定义。首列是表达式、次列是 ts，
//     于是等值 + 范围 + ORDER BY ts 全部由索引顺序直接给出，
//     临时 B 树排序也随之消失。
const schemaTrafficIndex = `
CREATE INDEX IF NOT EXISTS idx_traffic_sample_ts       ON traffic_sample(ts);
CREATE INDEX IF NOT EXISTS idx_traffic_sample_peer_ts ON traffic_sample(COALESCE(peer_id, 0), ts);
`

// schemaQuotaPeriodStart 记录配额已结算到的周期起点。
//
// 为什么必须落库：周期结算的判据只能是「当前周期起点 ≠ 上次结算的起点」。
// 之前用「现在是否在周期起点后两分钟内」这种时间窗口来近似，一旦某次判定因为
// 数据库繁忙、GC 或容器重启而错过，整个周期的配额就永远不会被重置，
// 被停用的 Peer 也永远回不来——而且没有任何报错，只表现为「配额再也不涨了」。
// 落库之后判定是幂等的：重跑一百次也只有第一次生效。
const schemaQuotaPeriodStart = `
ALTER TABLE peer ADD COLUMN quota_period_start INTEGER NOT NULL DEFAULT 0;
`
