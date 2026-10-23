package store

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
)

// migration 是一个顺序迁移项。
type migration struct {
	version int
	name    string
	stmts   []string
}

// migrations 按版本升序排列。新增迁移时只追加，不修改历史项。
var migrations = []migration{
	{version: 1, name: "init", stmts: splitStatements(schemaInit)},
	{version: 2, name: "indexes", stmts: splitStatements(schemaIndexes)},
	{version: 3, name: "traffic", stmts: splitStatements(schemaTraffic)},
	{version: 4, name: "drop_fake_crypto_option", stmts: splitStatements(schemaV4)},
	{version: 5, name: "policy_and_ops", stmts: splitStatements(schemaPolicy)},
	{version: 6, name: "quota_period_start", stmts: splitStatements(schemaQuotaPeriodStart)},
	{version: 7, name: "traffic_sample_indexes", stmts: splitStatements(schemaTrafficIndex)},
	{version: 8, name: "user_token_version", stmts: splitStatements(schemaV8)},
	{version: 9, name: "drop_api_token", stmts: splitStatements(schemaV9)},
	{version: 10, name: "drop_webhook", stmts: splitStatements(schemaV10)},
}

// splitStatements 以分号拆分多语句 DDL。
func splitStatements(script string) []string {
	parts := strings.Split(script, ";")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if s := strings.TrimSpace(p); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// migrate 应用所有未执行的迁移。
func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS schema_migration (
  version    INTEGER PRIMARY KEY,
  name       TEXT NOT NULL,
  applied_at INTEGER NOT NULL
)`); err != nil {
		return fmt.Errorf("store: create schema_migration: %w", err)
	}

	applied := map[int]bool{}
	rows, err := s.db.QueryContext(ctx, `SELECT version FROM schema_migration`)
	if err != nil {
		return fmt.Errorf("store: read schema_migration: %w", err)
	}
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			rows.Close()
			return err
		}
		applied[v] = true
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	sort.Slice(migrations, func(i, j int) bool { return migrations[i].version < migrations[j].version })

	for _, m := range migrations {
		if applied[m.version] {
			continue
		}
		for _, stmt := range m.stmts {
			stmt = strings.TrimSpace(stmt)
			if stmt == "" {
				continue
			}
			if _, err := s.db.ExecContext(ctx, stmt); err != nil {
				return fmt.Errorf("store: migration %d (%s): %w\nstmt: %s", m.version, m.name, err, stmt)
			}
		}
		if _, err := s.db.ExecContext(ctx,
			`INSERT INTO schema_migration (version, name, applied_at) VALUES (?, ?, ?)`,
			m.version, m.name, nowUnix()); err != nil {
			return fmt.Errorf("store: record migration %d: %w", m.version, err)
		}
	}
	return nil
}

// migrateTx 是供测试使用的裸事务迁移入口。
func migrateTx(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migration (version INTEGER PRIMARY KEY, name TEXT NOT NULL, applied_at INTEGER NOT NULL)`)
	return err
}
