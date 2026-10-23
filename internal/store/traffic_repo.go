package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/Rewind2026/wgpanel/internal/domain"
)

// TrafficRepo 负责流量时序与系统指标。
type TrafficRepo struct{ s *Store }

// TrafficRepo 返回流量仓储。
func (s *Store) TrafficRepo() *TrafficRepo { return &TrafficRepo{s: s} }

// InsertSamples 批量写入采样点。
func (r *TrafficRepo) InsertSamples(ctx context.Context, samples []domain.TrafficSample) error {
	if len(samples) == 0 {
		return nil
	}
	tx, err := r.s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin samples tx: %w", err)
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx,
		`INSERT INTO traffic_sample (ts, peer_id, rx_bytes, tx_bytes, rx_pps, tx_pps, handshake_ts)
		 VALUES (?,?,?,?,?,?,?)`)
	if err != nil {
		return fmt.Errorf("store: prepare samples: %w", err)
	}
	defer stmt.Close()

	for _, sm := range samples {
		if _, err := stmt.ExecContext(ctx, sm.TS, sm.PeerID, sm.RxBytes, sm.TxBytes, sm.RxPps, sm.TxPps, sm.HandshakeTS); err != nil {
			return fmt.Errorf("store: insert sample: %w", err)
		}
	}
	return tx.Commit()
}

// QueryTraffic 按时间范围与粒度返回历史曲线。peerID 传 nil 表示隧道汇总。
func (r *TrafficRepo) QueryTraffic(ctx context.Context, from, to int64, bucket string, peerID *int64) ([]domain.TrafficPoint, error) {
	var (
		rows *sql.Rows
		err  error
	)
	switch bucket {
	case "10m", "hour", "day":
		rows, err = r.queryRollup(ctx, bucket, from, to, peerID)
	default:
		rows, err = r.queryRaw(ctx, from, to, peerID)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]domain.TrafficPoint, 0, 512)
	for rows.Next() {
		var p domain.TrafficPoint
		if err := rows.Scan(&p.TS, &p.RxBytes, &p.TxBytes); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	computeRates(out)
	return out, nil
}

// Rollup 把原始采样按粒度聚合到 rollup 表。
// 采样值是内核累计计数器，故聚合取 MAX 而非 SUM。
// SQLite 不允许在 DO UPDATE 子句里使用聚合函数，因此冲突时在应用侧比较取大。
func (r *TrafficRepo) Rollup(ctx context.Context, bucket string, since int64, stepSeconds int64) error {
	var source, filter string
	switch bucket {
	case "10m":
		source, filter = "traffic_sample", ""
	case "hour":
		source, filter = "traffic_rollup", "bucket = '10m'"
	case "day":
		source, filter = "traffic_rollup", "bucket = 'hour'"
	default:
		return fmt.Errorf("store: unsupported bucket %q", bucket)
	}
	// 源表可能自带 bucket 过滤条件，需与时间条件合并，避免拼出两个 WHERE。
	if filter != "" {
		filter += " AND "
	}

	q := `INSERT INTO traffic_rollup (bucket, ts, peer_id, rx_bytes, tx_bytes)
	      SELECT ?, (ts / ?) * ?, COALESCE(peer_id, 0), MAX(rx_bytes), MAX(tx_bytes)
	      FROM ` + source + ` WHERE ` + filter + `ts >= ?
	      GROUP BY (ts / ?), COALESCE(peer_id, 0)
	      ON CONFLICT(bucket, ts, peer_id) DO UPDATE SET
	        rx_bytes = CASE WHEN excluded.rx_bytes > rx_bytes THEN excluded.rx_bytes ELSE rx_bytes END,
	        tx_bytes = CASE WHEN excluded.tx_bytes > tx_bytes THEN excluded.tx_bytes ELSE tx_bytes END`
	if _, err := r.s.db.ExecContext(ctx, q, bucket, stepSeconds, stepSeconds, since, stepSeconds); err != nil {
		return fmt.Errorf("store: rollup %s: %w", bucket, err)
	}
	return nil
}

// DeleteOlderThan 按保留策略清理数据，返回删除条数。
func (r *TrafficRepo) DeleteOlderThan(ctx context.Context, bucket string, before int64) (int64, error) {
	var q string
	switch bucket {
	case "raw":
		q = `DELETE FROM traffic_sample WHERE ts < ?`
	case "10m":
		q = `DELETE FROM traffic_rollup WHERE bucket = '10m' AND ts < ?`
	case "hour":
		q = `DELETE FROM traffic_rollup WHERE bucket = 'hour' AND ts < ?`
	case "day":
		q = `DELETE FROM traffic_rollup WHERE bucket = 'day' AND ts < ?`
	default:
		return 0, fmt.Errorf("store: unsupported bucket %q", bucket)
	}
	res, err := r.s.db.ExecContext(ctx, q, before)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// InsertSystemMetric 写入一条系统指标。
func (r *TrafficRepo) InsertSystemMetric(ctx context.Context, m domain.SystemMetrics) error {
	_, err := r.s.db.ExecContext(ctx,
		`INSERT INTO system_metric (ts, cpu_percent, mem_used_bytes, mem_percent, disk_used_bytes,
			disk_percent, net_rx_bytes, net_tx_bytes, uptime_seconds, goroutines)
		 VALUES (?,?,?,?,?,?,?,?,?,?)
		 ON CONFLICT(ts) DO UPDATE SET cpu_percent=excluded.cpu_percent,
			mem_used_bytes=excluded.mem_used_bytes, mem_percent=excluded.mem_percent`,
		nowUnix(), m.CPUPercent, m.MemUsedBytes, m.MemPercent, m.DiskUsedBytes,
		m.DiskPercent, m.NetRxBytes, m.NetTxBytes, m.UptimeSeconds, m.Goroutines)
	return err
}

// DeleteSystemMetricsBefore 清理过期系统指标。
func (r *TrafficRepo) DeleteSystemMetricsBefore(ctx context.Context, before int64) (int64, error) {
	res, err := r.s.db.ExecContext(ctx, `DELETE FROM system_metric WHERE ts < ?`, before)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// queryRaw 查询原始采样。汇总行用 peer_id = 0 标识（COALESCE 兼容历史 NULL 行）。
func (r *TrafficRepo) queryRaw(ctx context.Context, from, to int64, peerID *int64) (*sql.Rows, error) {
	if peerID != nil {
		return r.s.ro.QueryContext(ctx,
			`SELECT ts, rx_bytes, tx_bytes FROM traffic_sample
			 WHERE ts >= ? AND ts <= ? AND COALESCE(peer_id, 0) = ? ORDER BY ts`,
			from, to, *peerID)
	}
	return r.s.ro.QueryContext(ctx,
		`SELECT ts, rx_bytes, tx_bytes FROM traffic_sample
		 WHERE ts >= ? AND ts <= ? AND COALESCE(peer_id, 0) = 0 ORDER BY ts`,
		from, to)
}

// queryRollup 查询降采样聚合表。
func (r *TrafficRepo) queryRollup(ctx context.Context, bucket string, from, to int64, peerID *int64) (*sql.Rows, error) {
	if peerID != nil {
		return r.s.ro.QueryContext(ctx,
			`SELECT ts, rx_bytes, tx_bytes FROM traffic_rollup
			 WHERE bucket = ? AND ts >= ? AND ts <= ? AND peer_id = ? ORDER BY ts`,
			bucket, from, to, *peerID)
	}
	return r.s.ro.QueryContext(ctx,
		`SELECT ts, rx_bytes, tx_bytes FROM traffic_rollup
		 WHERE bucket = ? AND ts >= ? AND ts <= ? AND peer_id = 0 ORDER BY ts`,
		bucket, from, to)
}

// computeRates 由累计字节差分推算速率。
//
// 计数器回退保护与 rateTracker.compute 同理：接口重建、容器重启后
// 累计值从 0 重新开始，差分会是负数。不挡住的话历史曲线上会出现
// 向下的尖峰，读者会以为流量倒流，界面还会算出负的「合计流量」。
// 这类区间真实值无从得知（重启期间的字节没被记下来），按 0 处理。
func computeRates(points []domain.TrafficPoint) {
	for i := 1; i < len(points); i++ {
		dt := points[i].TS - points[i-1].TS
		if dt <= 0 {
			continue
		}
		drx := points[i].RxBytes - points[i-1].RxBytes
		dtx := points[i].TxBytes - points[i-1].TxBytes
		if drx < 0 || dtx < 0 {
			points[i].RxRate = 0
			points[i].TxRate = 0
			continue
		}
		points[i].RxRate = drx / dt
		points[i].TxRate = dtx / dt
	}
}

// WindowToRange 把 UI 传入的时间窗口映射为起止时间与聚合粒度。
func WindowToRange(window string) (from, to time.Time, bucket string) {
	now := time.Now()
	switch window {
	case "1h":
		return now.Add(-time.Hour), now, "raw"
	case "24h":
		return now.Add(-24 * time.Hour), now, "10m"
	case "7d":
		return now.Add(-7 * 24 * time.Hour), now, "hour"
	case "30d":
		return now.Add(-30 * 24 * time.Hour), now, "day"
	default:
		return now.Add(-24 * time.Hour), now, "10m"
	}
}
