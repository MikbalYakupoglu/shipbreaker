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
// Each bucket is committed in one short transaction.
func (a *Aggregator) Run(ctx context.Context) error {
	now := time.Now().UTC().Unix()
	currentBucket := now - (now % 3600)

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
		runtime.Gosched()
	}
	return nil
}

type aggState struct {
	cpuSum, memSum        float64
	cpuMax                float64
	netRxDelta, netTxDelta int64
	blkRDelta, blkWDelta  int64
	count                 int
	prevRx, prevTx        int64
	prevBR, prevBW        int64
}

// processBucket aggregates one hourly bucket using 2 reader queries and 1 writer transaction
// instead of the previous 3N queries + N transactions pattern.
func (a *Aggregator) processBucket(ctx context.Context, bucket int64) error {
	bucketEnd := bucket + 3600

	// ── 1. Prev-bucket row per container (batch, for delta continuity) ────────
	type prevRow struct{ netRx, netTx, blkR, blkW int64 }
	prevMap := map[string]prevRow{}

	pRows, err := a.r.QueryContext(ctx, `
		SELECT m.container_id,
		       m.net_rx_bytes, m.net_tx_bytes, m.blk_read_bytes, m.blk_write_bytes
		FROM metrics_raw m
		JOIN (
		    SELECT container_id, MAX(timestamp) AS ts
		    FROM metrics_raw
		    WHERE timestamp < ?
		    GROUP BY container_id
		) sub ON m.container_id = sub.container_id AND m.timestamp = sub.ts
		WHERE m.container_id IN (
		    SELECT DISTINCT container_id FROM metrics_raw
		    WHERE timestamp >= ? AND timestamp < ?
		)
	`, bucket, bucket, bucketEnd)
	if err != nil {
		return err
	}
	for pRows.Next() {
		var cid string
		var p prevRow
		if err := pRows.Scan(&cid, &p.netRx, &p.netTx, &p.blkR, &p.blkW); err != nil {
			pRows.Close()
			return err
		}
		prevMap[cid] = p
	}
	pRows.Close()
	if err := pRows.Err(); err != nil {
		return err
	}

	// ── 2. All raw rows for this bucket, ordered for delta computation ─────────
	aggMap := map[string]*aggState{}

	dRows, err := a.r.QueryContext(ctx, `
		SELECT container_id, cpu_percent, memory_ws_bytes,
		       net_rx_bytes, net_tx_bytes, blk_read_bytes, blk_write_bytes
		FROM metrics_raw
		WHERE timestamp >= ? AND timestamp < ?
		ORDER BY container_id, timestamp ASC
	`, bucket, bucketEnd)
	if err != nil {
		return err
	}
	for dRows.Next() {
		var cid string
		var cpu float64
		var memWS, netRx, netTx, blkR, blkW int64
		if err := dRows.Scan(&cid, &cpu, &memWS, &netRx, &netTx, &blkR, &blkW); err != nil {
			dRows.Close()
			return err
		}
		s, ok := aggMap[cid]
		if !ok {
			p := prevMap[cid]
			s = &aggState{prevRx: p.netRx, prevTx: p.netTx, prevBR: p.blkR, prevBW: p.blkW}
			aggMap[cid] = s
		}
		s.cpuSum += cpu
		if cpu > s.cpuMax {
			s.cpuMax = cpu
		}
		s.memSum += float64(memWS)
		s.count++

		if netRx >= s.prevRx {
			s.netRxDelta += netRx - s.prevRx
		} else {
			s.netRxDelta += netRx
		}
		if netTx >= s.prevTx {
			s.netTxDelta += netTx - s.prevTx
		} else {
			s.netTxDelta += netTx
		}
		if blkR >= s.prevBR {
			s.blkRDelta += blkR - s.prevBR
		} else {
			s.blkRDelta += blkR
		}
		if blkW >= s.prevBW {
			s.blkWDelta += blkW - s.prevBW
		} else {
			s.blkWDelta += blkW
		}
		s.prevRx, s.prevTx, s.prevBR, s.prevBW = netRx, netTx, blkR, blkW
	}
	dRows.Close()
	if err := dRows.Err(); err != nil {
		return err
	}

	if len(aggMap) == 0 {
		return nil
	}

	// ── 3. Write all hourly rows in ONE transaction ───────────────────────────
	tx, err := a.w.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, `
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
	`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for cid, s := range aggMap {
		cpuAvg := s.cpuSum / float64(s.count)
		memAvg := int64(s.memSum / float64(s.count))
		if _, err := stmt.ExecContext(ctx, cid, bucket,
			cpuAvg, s.cpuMax, memAvg,
			s.netRxDelta, s.netTxDelta, s.blkRDelta, s.blkWDelta,
			s.count); err != nil {
			return err
		}
	}

	return tx.Commit()
}
