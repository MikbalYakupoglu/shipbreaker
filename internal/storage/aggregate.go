package storage

import (
	"context"
	"database/sql"
	"log/slog"
	"runtime"
	"time"
)

// Aggregator converts raw per-minute metrics into hourly buckets.
type Aggregator struct {
	w   *sql.DB
	r   *sql.DB
	log *slog.Logger
}

func NewAggregator(writer, reader *sql.DB, log *slog.Logger) *Aggregator {
	return &Aggregator{w: writer, r: reader, log: log}
}

// Run processes the current incomplete bucket plus any unprocessed past buckets.
// Each bucket is committed in its own short transaction (FIX 5 — chunked backfill).
func (a *Aggregator) Run(ctx context.Context) error {
	now := time.Now().UTC().Unix()
	currentBucket := now - (now % 3600)

	// Find the oldest unprocessed bucket in metrics_raw
	var oldestRaw sql.NullInt64
	err := a.r.QueryRowContext(ctx, `
		SELECT MIN(timestamp - (timestamp % 3600)) FROM metrics_raw
	`).Scan(&oldestRaw)
	if err != nil || !oldestRaw.Valid {
		return nil
	}

	for bucket := oldestRaw.Int64; bucket <= currentBucket; bucket += 3600 {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := a.processBucket(ctx, bucket); err != nil {
			a.log.Error("aggregate bucket", "bucket", bucket, "err", err)
		}
		runtime.Gosched() // yield between buckets
	}
	return nil
}

func (a *Aggregator) processBucket(ctx context.Context, bucket int64) error {
	bucketEnd := bucket + 3600

	// Fetch all container_ids that have raw data in this bucket
	rows, err := a.r.QueryContext(ctx, `
		SELECT DISTINCT container_id FROM metrics_raw
		WHERE timestamp >= ? AND timestamp < ?
	`, bucket, bucketEnd)
	if err != nil {
		return err
	}
	var containerIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		containerIDs = append(containerIDs, id)
	}
	rows.Close()

	for _, cid := range containerIDs {
		if err := a.upsertHourlyBucket(ctx, cid, bucket, bucketEnd); err != nil {
			a.log.Warn("upsert hourly bucket", "container", cid[:min12(cid)], "bucket", bucket, "err", err)
		}
	}
	return nil
}

func (a *Aggregator) upsertHourlyBucket(ctx context.Context, containerID string, bucket, bucketEnd int64) error {
	// Fetch prev bucket's last raw values for delta continuity (kovalar arası süreklilik)
	var prevNetRx, prevNetTx, prevBlkR, prevBlkW int64
	err := a.r.QueryRowContext(ctx, `
		SELECT net_rx_bytes, net_tx_bytes, blk_read_bytes, blk_write_bytes
		FROM metrics_raw
		WHERE container_id = ? AND timestamp < ?
		ORDER BY timestamp DESC LIMIT 1
	`, containerID, bucket).Scan(&prevNetRx, &prevNetTx, &prevBlkR, &prevBlkW)
	// no prev row → err (sql.ErrNoRows) → deltas start from 0, that's fine

	type rawRow struct {
		ts      int64
		cpu     float64
		memWS   int64
		netRx   int64
		netTx   int64
		blkR    int64
		blkW    int64
	}

	rows, err2 := a.r.QueryContext(ctx, `
		SELECT timestamp, cpu_percent, memory_ws_bytes,
		       net_rx_bytes, net_tx_bytes, blk_read_bytes, blk_write_bytes
		FROM metrics_raw
		WHERE container_id = ? AND timestamp >= ? AND timestamp < ?
		ORDER BY timestamp ASC
	`, containerID, bucket, bucketEnd)
	if err2 != nil {
		return err2
	}
	_ = err // prev lookup error is non-fatal

	var samples []rawRow
	for rows.Next() {
		var r rawRow
		if err := rows.Scan(&r.ts, &r.cpu, &r.memWS, &r.netRx, &r.netTx, &r.blkR, &r.blkW); err != nil {
			rows.Close()
			return err
		}
		samples = append(samples, r)
	}
	rows.Close()

	if len(samples) == 0 {
		return nil
	}

	// Compute aggregates
	var cpuSum, memSum float64
	var cpuMax float64
	var netRxDelta, netTxDelta, blkRDelta, blkWDelta int64

	prevRx, prevTx, prevBR, prevBW := prevNetRx, prevNetTx, prevBlkR, prevBlkW

	for _, s := range samples {
		cpuSum += s.cpu
		if s.cpu > cpuMax {
			cpuMax = s.cpu
		}
		memSum += float64(s.memWS)

		// reset-protected deltas
		if s.netRx >= prevRx {
			netRxDelta += s.netRx - prevRx
		} else {
			netRxDelta += s.netRx
		}
		if s.netTx >= prevTx {
			netTxDelta += s.netTx - prevTx
		} else {
			netTxDelta += s.netTx
		}
		if s.blkR >= prevBR {
			blkRDelta += s.blkR - prevBR
		} else {
			blkRDelta += s.blkR
		}
		if s.blkW >= prevBW {
			blkWDelta += s.blkW - prevBW
		} else {
			blkWDelta += s.blkW
		}

		prevRx = s.netRx
		prevTx = s.netTx
		prevBR = s.blkR
		prevBW = s.blkW
	}

	n := len(samples)
	cpuAvg := cpuSum / float64(n)
	memAvg := int64(memSum / float64(n))

	_, err = a.w.ExecContext(ctx, `
		INSERT INTO metrics_hourly
			(container_id, bucket, cpu_percent_avg, cpu_percent_max,
			 memory_ws_avg, net_rx_delta, net_tx_delta,
			 blk_read_delta, blk_write_delta, sample_count)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(container_id, bucket) DO UPDATE SET
			cpu_percent_avg  = excluded.cpu_percent_avg,
			cpu_percent_max  = excluded.cpu_percent_max,
			memory_ws_avg    = excluded.memory_ws_avg,
			net_rx_delta     = excluded.net_rx_delta,
			net_tx_delta     = excluded.net_tx_delta,
			blk_read_delta   = excluded.blk_read_delta,
			blk_write_delta  = excluded.blk_write_delta,
			sample_count     = excluded.sample_count
	`, containerID, bucket, cpuAvg, cpuMax, memAvg,
		netRxDelta, netTxDelta, blkRDelta, blkWDelta, n)
	return err
}

func min12(id string) int {
	if len(id) > 12 {
		return 12
	}
	return len(id)
}
