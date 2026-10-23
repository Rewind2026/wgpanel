package store

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Rewind2026/wgpanel/internal/config"
	"github.com/Rewind2026/wgpanel/internal/domain"
)

// M7 第 8 项：SQLite 文件增长可预期。
//
// 这组用例盯的是一个「跑得越久越慢」的缺陷：
//
// traffic_sample 每秒写入「1 行隧道汇总 + 每 Peer 1 行」，240 Peer 就是 241 行/秒，
// 一天的原始采样就是 2000 万行。表上原本没有任何索引，于是：
//
//   - 曲线查询（1h 窗口走 queryRaw）变成全表扫描 + 临时 B 树排序。
//     实测行数从 2.9 万涨到 14.5 万时，查询耗时从 5ms 涨到 20ms——
//     纯线性，且没有任何收敛迹象。72 小时后表里有 2000 万行，
//     单次查询要扫完全表，监控页会直接卡死。
//   - 保留策略清理（DELETE FROM traffic_sample WHERE ts < ?）同样是全表扫描，
//     每小时跑一次，耗时随运行时长线性增长。
//
// 也就是说「SQLite 文件增长可预期」这件事本身是成立的（有保留策略兜底），
// 但增长带来的读放大与清理成本不可预期——磁盘没满，页面先卡死了。

// TestTrafficQueriesUseIndexes 钉住查询计划。
//
// 用 EXPLAIN QUERY PLAN 而不是测耗时：耗时断言在 CI 上不稳定，
// 而查询计划是确定性的——出现 SCAN 就说明退化了。
func TestTrafficQueriesUseIndexes(t *testing.T) {
	st, err := OpenInMemory()
	if err != nil {
		t.Fatalf("open in-memory store: %v", err)
	}
	defer st.Close()

	cases := []struct {
		name  string
		query string
		// banned 是查询计划里不允许出现的关键词。
		banned []string
	}{
		{
			name:   "曲线查询按 peer_id + 时间窗定位",
			query:  `EXPLAIN QUERY PLAN SELECT ts, rx_bytes, tx_bytes FROM traffic_sample WHERE ts >= 1 AND ts <= 2 AND COALESCE(peer_id, 0) = 0 ORDER BY ts`,
			banned: []string{"SCAN traffic_sample"},
		},
		{
			name:   "曲线查询按指定 peer 定位",
			query:  `EXPLAIN QUERY PLAN SELECT ts, rx_bytes, tx_bytes FROM traffic_sample WHERE ts >= 1 AND ts <= 2 AND COALESCE(peer_id, 0) = 7 ORDER BY ts`,
			banned: []string{"SCAN traffic_sample"},
		},
		{
			name:   "保留策略清理按时间定位",
			query:  `EXPLAIN QUERY PLAN DELETE FROM traffic_sample WHERE ts < 1`,
			banned: []string{"SCAN traffic_sample"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan := explainPlan(t, st, tc.query)
			t.Logf("计划: %s", plan)
			for _, bad := range tc.banned {
				if contains(plan, bad) {
					t.Fatalf("查询退化为全表扫描（计划含 %q）：%s", bad, plan)
				}
			}
		})
	}
}

// TestTrafficQueryPlanIsStable 单独把「临时 B 树排序」拎出来。
//
// 有索引还不够：ORDER BY ts 若不能从索引顺序直接得到，SQLite 仍要
// 把命中的全部行塞进临时 B 树再排一遍。1h 窗口命中 3600 行隧道汇总
// 尚可，但按 peer 查且窗口放大时这一步是纯浪费。
func TestTrafficQueryPlanAvoidsTempSort(t *testing.T) {
	st, err := OpenInMemory()
	if err != nil {
		t.Fatalf("open in-memory store: %v", err)
	}
	defer st.Close()

	plan := explainPlan(t, st,
		`EXPLAIN QUERY PLAN SELECT ts, rx_bytes, tx_bytes FROM traffic_sample WHERE ts >= 1 AND ts <= 2 AND COALESCE(peer_id, 0) = 0 ORDER BY ts`)
	t.Logf("计划: %s", plan)
	if contains(plan, "TEMP B-TREE") {
		t.Fatalf("ORDER BY 仍需临时排序，索引顺序没被用上：%s", plan)
	}
}

// TestTrafficSampleGrowthIsBounded 验证增长幅度本身可预期。
//
// 每秒写入 1 + PeerCount 行；保留期由清理决定。断言「写入速率」而不是
// 「文件大小」：文件大小受页填充率与 WAL 影响，波动大；
// 行数是确定的，且能直接换算成磁盘占用上限。
func TestTrafficSampleGrowthIsBounded(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Default()
	cfg.DataDir = dir
	st, err := Open(context.Background(), cfg)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	ctx := context.Background()
	repo := st.TrafficRepo()
	const peers = 60

	base := time.Now().Unix() - 300
	for tick := 1; tick <= 300; tick++ {
		ts := base + int64(tick)
		samples := make([]domain.TrafficSample, 0, peers+1)
		samples = append(samples, domain.TrafficSample{TS: ts, PeerID: 0, RxBytes: int64(tick) << 20})
		for i := 1; i <= peers; i++ {
			samples = append(samples, domain.TrafficSample{
				TS: ts, PeerID: int64(i), RxBytes: int64(tick*i) * 1024,
				TxBytes: int64(tick*i) * 512, HandshakeTS: ts,
			})
		}
		if err := repo.InsertSamples(ctx, samples); err != nil {
			t.Fatalf("insert tick %d: %v", tick, err)
		}
	}

	var rows int
	if err := st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM traffic_sample`).Scan(&rows); err != nil {
		t.Fatalf("count: %v", err)
	}
	want := 300 * (peers + 1)
	if rows != want {
		t.Fatalf("行数应为 %d，实际 %d", want, rows)
	}

	// 换算成生产口径：240 Peer × 1 秒采样 × 24 小时保留。
	perDay := 241 * 86400
	t.Logf("300 拍写入 %d 行；按 241 行/秒推算，24 小时原始采样约 %.0f 万行",
		rows, float64(perDay)/1e4)

	// 清理后必须真的删掉，且保留窗口内的数据不受影响。
	from, to := base+250, base+300
	points, err := repo.QueryTraffic(ctx, from, to, "raw", nil)
	if err != nil {
		t.Fatalf("query before cleanup: %v", err)
	}
	if len(points) != 51 {
		t.Fatalf("清理前窗口内应有 51 个汇总点，实际 %d", len(points))
	}

	deleted, err := repo.DeleteOlderThan(ctx, "raw", base+300)
	if err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if deleted != 299*(peers+1) {
		t.Fatalf("应删除 %d 行，实际 %d", 299*(peers+1), deleted)
	}

	points, err = repo.QueryTraffic(ctx, from, to, "raw", nil)
	if err != nil {
		t.Fatalf("query after cleanup: %v", err)
	}
	if len(points) != 1 {
		t.Fatalf("清理后窗口内应只剩 1 个点，实际 %d", len(points))
	}
}

func explainPlan(t *testing.T, st *Store, query string) string {
	t.Helper()
	rows, err := st.DB().QueryContext(context.Background(), query)
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		t.Fatalf("columns: %v", err)
	}
	out := ""
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out += fmt.Sprint(vals[len(vals)-1]) + " | "
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	if out == "" {
		t.Fatal("查询计划为空，无法判定")
	}
	return out
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && indexOf(s, sub) >= 0
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
