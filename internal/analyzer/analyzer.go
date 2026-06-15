package analyzer

import (
	"context"
	"database/sql"
	"time"
)

type Status string

const (
	StatusZombie       Status = "zombie"
	StatusActive       Status = "active"
	StatusInsufficient Status = "insufficient_data"
)

type ServiceResult struct {
	ServiceID    string
	ServiceKey   string
	Name         string
	Image        string
	Status       Status
	CPUAvg       float64
	NetBytesDay  float64
	DiskBytesDay float64
	WindowDays   float64
	SampleCount  int
}

type Config struct {
	WindowDays          int
	MinSamples          int
	CPUThresholdPct     float64
	NetThresholdPerDay  float64
	DiskThresholdPerDay float64
}

type Analyzer struct {
	r   *sql.DB
	cfg Config
}

func New(reader *sql.DB, cfg Config) *Analyzer {
	return &Analyzer{r: reader, cfg: cfg}
}

// Run evaluates ALL non-removed services, returning results even for those with insufficient data.
func (a *Analyzer) Run(ctx context.Context) ([]ServiceResult, error) {
	// Include all known (non-removed) services — metrics existence not required.
	rows, err := a.r.QueryContext(ctx, `
		SELECT DISTINCT c.service_id, c.service_key, c.name, c.image
		FROM containers c
		WHERE c.status != 'removed'
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type svcKey struct{ id, key, name, image string }
	var services []svcKey
	seen := map[string]bool{}
	for rows.Next() {
		var s svcKey
		if err := rows.Scan(&s.id, &s.key, &s.name, &s.image); err != nil {
			return nil, err
		}
		if !seen[s.id] {
			seen[s.id] = true
			services = append(services, s)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var results []ServiceResult
	for _, svc := range services {
		res, err := a.evaluateService(ctx, svc.id, svc.key, svc.name, svc.image, time.Now().UTC().Unix())
		if err != nil {
			continue
		}
		results = append(results, res)
	}
	return results, nil
}

func (a *Analyzer) evaluateService(
	ctx context.Context,
	serviceID, serviceKey, name, image string,
	now int64,
) (ServiceResult, error) {
	W := int64(a.cfg.WindowDays) * 86400

	// Fetch ALL containers for this service ordered newest first.
	// No created_at cutoff — we need the full history to detect last image change.
	containerRows, err := a.r.QueryContext(ctx, `
		SELECT id, image_id, created_at FROM containers
		WHERE service_id = ?
		ORDER BY created_at DESC
	`, serviceID)
	if err != nil {
		return ServiceResult{}, err
	}
	type gen struct {
		id        string
		imageID   string
		createdAt int64
	}
	var gens []gen
	for containerRows.Next() {
		var g gen
		var imgID sql.NullString
		if err := containerRows.Scan(&g.id, &imgID, &g.createdAt); err != nil {
			containerRows.Close()
			return ServiceResult{}, err
		}
		g.imageID = imgID.String
		gens = append(gens, g)
	}
	containerRows.Close()

	if len(gens) == 0 {
		return ServiceResult{ServiceID: serviceID, ServiceKey: serviceKey,
			Name: name, Image: image, Status: StatusInsufficient}, nil
	}

	// Determine clamped window start (Y1).
	// Find the start of the current image's continuous unbroken run.
	currentImageID := gens[0].imageID
	clampedStart := now - W // default: full window

	if currentImageID != "" {
		// Walk newest→oldest; find where the current imageID starts continuously.
		lastImageChange := int64(0)
		for _, g := range gens {
			if g.imageID != currentImageID {
				break
			}
			lastImageChange = g.createdAt
		}
		// Only clamp if the image change happened within the window.
		if lastImageChange > now-W {
			clampedStart = lastImageChange
		}
	}

	// Count distinct hourly buckets in the clamped window for this service.
	var sampleCount int
	err = a.r.QueryRowContext(ctx, `
		SELECT COUNT(DISTINCT h.bucket)
		FROM metrics_hourly h
		JOIN containers c ON c.id = h.container_id
		WHERE c.service_id = ? AND h.bucket >= ? AND h.bucket < ?
	`, serviceID, clampedStart, now).Scan(&sampleCount)
	if err != nil {
		return ServiceResult{}, err
	}

	// Count buckets from the current (incomplete) hour in metrics_raw.
	currentBucket := now - (now % 3600)
	var rawBuckets int
	_ = a.r.QueryRowContext(ctx, `
		SELECT COUNT(DISTINCT (r.timestamp - (r.timestamp % 3600)))
		FROM metrics_raw r
		JOIN containers c ON c.id = r.container_id
		WHERE c.service_id = ? AND r.timestamp >= ? AND r.timestamp >= ?
	`, serviceID, clampedStart, currentBucket).Scan(&rawBuckets)
	if rawBuckets > 0 {
		sampleCount += rawBuckets
	}

	// minSamples is measured against full W (plan 5.1).
	if sampleCount < a.cfg.MinSamples {
		return ServiceResult{
			ServiceID:   serviceID,
			ServiceKey:  serviceKey,
			Name:        name,
			Image:       image,
			Status:      StatusInsufficient,
			SampleCount: sampleCount,
		}, nil
	}

	cpuAvg, netBytes, diskBytes, err := a.computeMetrics(ctx, serviceID, clampedStart, now)
	if err != nil {
		return ServiceResult{}, err
	}

	windowDays := float64(now-clampedStart) / 86400.0
	if windowDays < 0.001 {
		windowDays = 0.001
	}

	netPerDay := netBytes / windowDays
	diskPerDay := diskBytes / windowDays

	isZombie := cpuAvg < a.cfg.CPUThresholdPct &&
		netPerDay < a.cfg.NetThresholdPerDay &&
		(diskBytes < 0 || diskPerDay < a.cfg.DiskThresholdPerDay)

	status := StatusActive
	if isZombie {
		status = StatusZombie
	}

	return ServiceResult{
		ServiceID:    serviceID,
		ServiceKey:   serviceKey,
		Name:         name,
		Image:        image,
		Status:       status,
		CPUAvg:       cpuAvg,
		NetBytesDay:  netPerDay,
		DiskBytesDay: diskPerDay,
		WindowDays:   windowDays,
		SampleCount:  sampleCount,
	}, nil
}

func (a *Analyzer) computeMetrics(
	ctx context.Context,
	serviceID string,
	clampedStart, now int64,
) (cpuAvg float64, netBytes, diskBytes float64, err error) {
	currentBucket := now - (now % 3600)

	row := a.r.QueryRowContext(ctx, `
		SELECT
			SUM(h.cpu_percent_avg * h.sample_count),
			SUM(h.sample_count),
			SUM(h.net_rx_delta + h.net_tx_delta),
			SUM(h.blk_read_delta + h.blk_write_delta)
		FROM metrics_hourly h
		JOIN containers c ON c.id = h.container_id
		WHERE c.service_id = ? AND h.bucket >= ? AND h.bucket < ?
	`, serviceID, clampedStart, currentBucket)

	var cpuWeighted, totalSamples, netSum, diskSum sql.NullFloat64
	if err = row.Scan(&cpuWeighted, &totalSamples, &netSum, &diskSum); err != nil {
		return
	}

	rawRow := a.r.QueryRowContext(ctx, `
		SELECT
			AVG(r.cpu_percent),
			COUNT(*),
			MAX(r.net_rx_bytes) - MIN(r.net_rx_bytes) + MAX(r.net_tx_bytes) - MIN(r.net_tx_bytes),
			MAX(r.blk_read_bytes) - MIN(r.blk_read_bytes) + MAX(r.blk_write_bytes) - MIN(r.blk_write_bytes)
		FROM metrics_raw r
		JOIN containers c ON c.id = r.container_id
		WHERE c.service_id = ? AND r.timestamp >= ? AND r.timestamp >= ?
	`, serviceID, clampedStart, currentBucket)

	var rawCPU, rawNet, rawDisk sql.NullFloat64
	var rawCount sql.NullInt64
	_ = rawRow.Scan(&rawCPU, &rawCount, &rawNet, &rawDisk)

	totalW := totalSamples.Float64
	cpuW := cpuWeighted.Float64
	if rawCPU.Valid && rawCount.Valid && rawCount.Int64 > 0 {
		cpuW += rawCPU.Float64 * float64(rawCount.Int64)
		totalW += float64(rawCount.Int64)
	}
	if totalW > 0 {
		cpuAvg = cpuW / totalW
	}

	netBytes = netSum.Float64
	if rawNet.Valid {
		netBytes += rawNet.Float64
	}

	if !diskSum.Valid {
		diskBytes = -1
	} else {
		diskBytes = diskSum.Float64
		if rawDisk.Valid {
			diskBytes += rawDisk.Float64
		}
	}

	err = nil
	return
}
