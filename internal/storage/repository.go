package storage

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/mikbal/shipbreaker/internal/docker"
)

// Repository wraps the writer DB and exposes write operations.
type Repository struct {
	w *sql.DB
	r *sql.DB
}

func NewRepository(writer, reader *sql.DB) *Repository {
	return &Repository{w: writer, r: reader}
}

// UpsertContainer inserts or updates a container row. Implements docker.Sink.
func (repo *Repository) UpsertContainer(info docker.ContainerInfo) error {
	now := time.Now().UTC().Unix()
	_, err := repo.w.ExecContext(context.Background(), `
		INSERT INTO containers
			(id, name, image, image_repo, image_id, service_key, service_id,
			 status, created_at, first_seen_at, last_seen_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name          = excluded.name,
			image         = excluded.image,
			image_repo    = excluded.image_repo,
			image_id      = excluded.image_id,
			service_key   = excluded.service_key,
			service_id    = excluded.service_id,
			status        = excluded.status,
			last_seen_at  = excluded.last_seen_at
	`,
		info.ID, info.Name, info.Image, info.ImageRepo, info.ImageID,
		info.ServiceKey, info.ServiceID,
		info.Status, info.CreatedAt, now, now,
	)
	return err
}

// InsertRawMetric inserts a raw per-minute metric. Implements docker.Sink.
func (repo *Repository) InsertRawMetric(m docker.RawMetric) error {
	_, err := repo.w.ExecContext(context.Background(), `
		INSERT INTO metrics_raw
			(container_id, timestamp, cpu_percent, memory_ws_bytes,
			 net_rx_bytes, net_tx_bytes, blk_read_bytes, blk_write_bytes)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`,
		m.ContainerID, m.Timestamp, m.CPUPercent, m.MemoryWSBytes,
		m.NetRxBytes, m.NetTxBytes, m.BlkReadBytes, m.BlkWriteBytes,
	)
	return err
}

// MarkRemoved sets status=removed and last_seen_at. Implements docker.Sink.
func (repo *Repository) MarkRemoved(containerID string, at int64) error {
	_, err := repo.w.ExecContext(context.Background(), `
		UPDATE containers SET status = 'removed', last_seen_at = ?
		WHERE id = ?
	`, at, containerID)
	return err
}

// MarkExitedIfNotIn marks as 'exited' any container with status='running' whose
// ID is not in runningIDs. Implements docker.Sink.
func (repo *Repository) MarkExitedIfNotIn(runningIDs []string, at int64) error {
	if len(runningIDs) == 0 {
		_, err := repo.w.ExecContext(context.Background(), `
			UPDATE containers SET status = 'exited', last_seen_at = ?
			WHERE status = 'running'
		`, at)
		return err
	}
	placeholders := make([]string, len(runningIDs))
	args := make([]any, 0, len(runningIDs)+1)
	args = append(args, at)
	for i, id := range runningIDs {
		placeholders[i] = "?"
		args = append(args, id)
	}
	q := fmt.Sprintf(`
		UPDATE containers SET status = 'exited', last_seen_at = ?
		WHERE status = 'running' AND id NOT IN (%s)
	`, strings.Join(placeholders, ","))
	_, err := repo.w.ExecContext(context.Background(), q, args...)
	return err
}
