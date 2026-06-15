package storage

import (
	"context"
	"database/sql"
	"log/slog"
	"time"
)

const deleteBatchSize = 5000

// Retention handles time-based pruning of raw and hourly metrics (FIX 2 + FIX 6).
type Retention struct {
	w              *sql.DB
	log            *slog.Logger
	rawDays        int
	hourlyDays     int
}

func NewRetention(writer *sql.DB, rawDays, hourlyDays int, log *slog.Logger) *Retention {
	return &Retention{w: writer, log: log, rawDays: rawDays, hourlyDays: hourlyDays}
}

// Run deletes expired metrics and then removes container rows with no remaining metrics.
// Order: aggregate must have run first (caller's responsibility).
func (r *Retention) Run(ctx context.Context) error {
	now := time.Now().UTC().Unix()
	rawCutoff := now - int64(r.rawDays)*86400
	hourlyCutoff := now - int64(r.hourlyDays)*86400

	if err := r.deleteRaw(ctx, rawCutoff); err != nil {
		return err
	}
	if err := r.deleteHourly(ctx, hourlyCutoff); err != nil {
		return err
	}
	if err := r.deleteOrphanedContainers(ctx); err != nil {
		return err
	}
	r.vacuum(ctx)
	return nil
}

func (r *Retention) deleteRaw(ctx context.Context, cutoff int64) error {
	return batchDelete(ctx, r.w, r.log,
		"metrics_raw", "timestamp", cutoff)
}

func (r *Retention) deleteHourly(ctx context.Context, cutoff int64) error {
	return batchDelete(ctx, r.w, r.log,
		"metrics_hourly", "bucket", cutoff)
}

// batchDelete deletes rows where timeCol < cutoff in batches to keep locks short (FIX 6).
func batchDelete(ctx context.Context, db *sql.DB, log *slog.Logger, table, timeCol string, cutoff int64) error {
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		res, err := db.ExecContext(ctx,
			`DELETE FROM `+table+` WHERE id IN (
				SELECT id FROM `+table+` WHERE `+timeCol+` < ? LIMIT ?
			)`, cutoff, deleteBatchSize)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		if n > 0 {
			log.Debug("retention batch", "table", table, "deleted", n)
		}
		if n < deleteBatchSize {
			break
		}
	}
	return nil
}

// deleteOrphanedContainers removes 'removed' containers that have no remaining metrics.
func (r *Retention) deleteOrphanedContainers(ctx context.Context) error {
	_, err := r.w.ExecContext(ctx, `
		DELETE FROM containers
		WHERE status = 'removed'
		  AND id NOT IN (SELECT container_id FROM metrics_raw)
		  AND id NOT IN (SELECT container_id FROM metrics_hourly)
	`)
	return err
}

func (r *Retention) vacuum(ctx context.Context) {
	if _, err := r.w.ExecContext(ctx, "PRAGMA incremental_vacuum"); err != nil {
		r.log.Warn("incremental_vacuum failed", "err", err)
	}
}
