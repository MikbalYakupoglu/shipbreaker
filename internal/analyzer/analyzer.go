package analyzer

import (
	"context"
	"database/sql"
	"math"
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

type containerGen struct {
	id        string
	imageID   string
	createdAt int64
}

type hourlyRow struct {
	bucket      int64
	cpuAvg      float64
	sampleCount int64
	netRx       int64
	netTx       int64
	blkR        int64
	blkW        int64
}

type rawRow struct {
	timestamp int64
	cpuPct    float64
	netRx     int64
	netTx     int64
	blkR      int64
	blkW      int64
}

// Run evaluates ALL non-removed services using 4 batch queries instead of 4N.
func (a *Analyzer) Run(ctx context.Context) ([]ServiceResult, error) {
	now := time.Now().UTC().Unix()
	W := int64(a.cfg.WindowDays) * 86400
	windowStart := now - W
	currentBucket := now - (now % 3600)

	// ── 1. All non-removed services ──────────────────────────────────────────
	svcRows, err := a.r.QueryContext(ctx, `
		SELECT DISTINCT c.service_id, c.service_key, c.name, c.image
		FROM containers c
		WHERE c.status != 'removed'
	`)
	if err != nil {
		return nil, err
	}
	type svcKey struct{ id, key, name, image string }
	var services []svcKey
	seen := map[string]bool{}
	for svcRows.Next() {
		var s svcKey
		if err := svcRows.Scan(&s.id, &s.key, &s.name, &s.image); err != nil {
			svcRows.Close()
			return nil, err
		}
		if !seen[s.id] {
			seen[s.id] = true
			services = append(services, s)
		}
	}
	svcRows.Close()
	if err := svcRows.Err(); err != nil {
		return nil, err
	}

	// ── 2. All containers (newest first per service, for image-change clamping) ─
	containerMap := map[string][]containerGen{}
	cRows, err := a.r.QueryContext(ctx, `
		SELECT service_id, id, COALESCE(image_id, ''), created_at
		FROM containers
		WHERE status != 'removed'
		ORDER BY service_id, created_at DESC
	`)
	if err != nil {
		return nil, err
	}
	for cRows.Next() {
		var svcID string
		var g containerGen
		if err := cRows.Scan(&svcID, &g.id, &g.imageID, &g.createdAt); err != nil {
			cRows.Close()
			return nil, err
		}
		containerMap[svcID] = append(containerMap[svcID], g)
	}
	cRows.Close()
	if err := cRows.Err(); err != nil {
		return nil, err
	}

	// ── 3. All hourly data within the full observation window ─────────────────
	hourlyMap := map[string][]hourlyRow{}
	hRows, err := a.r.QueryContext(ctx, `
		SELECT c.service_id,
		       h.bucket, h.cpu_percent_avg, h.sample_count,
		       h.net_rx_delta, h.net_tx_delta, h.blk_read_delta, h.blk_write_delta
		FROM metrics_hourly h
		JOIN containers c ON c.id = h.container_id
		WHERE h.bucket >= ? AND h.bucket < ?
	`, windowStart, currentBucket)
	if err != nil {
		return nil, err
	}
	for hRows.Next() {
		var svcID string
		var r hourlyRow
		if err := hRows.Scan(&svcID, &r.bucket, &r.cpuAvg, &r.sampleCount,
			&r.netRx, &r.netTx, &r.blkR, &r.blkW); err != nil {
			hRows.Close()
			return nil, err
		}
		hourlyMap[svcID] = append(hourlyMap[svcID], r)
	}
	hRows.Close()
	if err := hRows.Err(); err != nil {
		return nil, err
	}

	// ── 4. Raw data for the current (incomplete) hourly bucket ───────────────
	rawMap := map[string][]rawRow{}
	rRows, err := a.r.QueryContext(ctx, `
		SELECT c.service_id,
		       r.timestamp, r.cpu_percent,
		       r.net_rx_bytes, r.net_tx_bytes, r.blk_read_bytes, r.blk_write_bytes
		FROM metrics_raw r
		JOIN containers c ON c.id = r.container_id
		WHERE r.timestamp >= ?
	`, currentBucket)
	if err != nil {
		return nil, err
	}
	for rRows.Next() {
		var svcID string
		var r rawRow
		if err := rRows.Scan(&svcID, &r.timestamp, &r.cpuPct,
			&r.netRx, &r.netTx, &r.blkR, &r.blkW); err != nil {
			rRows.Close()
			return nil, err
		}
		rawMap[svcID] = append(rawMap[svcID], r)
	}
	rRows.Close()
	if err := rRows.Err(); err != nil {
		return nil, err
	}

	// ── 5. Evaluate each service in memory (no more DB calls) ─────────────────
	results := make([]ServiceResult, 0, len(services))
	for _, svc := range services {
		results = append(results, a.evaluateInMemory(
			svc.id, svc.key, svc.name, svc.image,
			now, W, currentBucket,
			containerMap[svc.id],
			hourlyMap[svc.id],
			rawMap[svc.id],
		))
	}
	return results, nil
}

func (a *Analyzer) evaluateInMemory(
	serviceID, serviceKey, name, image string,
	now, W, currentBucket int64,
	gens []containerGen,
	hourly []hourlyRow,
	raw []rawRow,
) ServiceResult {
	insufficient := func(count int) ServiceResult {
		return ServiceResult{
			ServiceID:   serviceID,
			ServiceKey:  serviceKey,
			Name:        name,
			Image:       image,
			Status:      StatusInsufficient,
			SampleCount: count,
		}
	}

	if len(gens) == 0 {
		return insufficient(0)
	}

	// Determine clamped window start based on current image's continuous run.
	clampedStart := now - W
	currentImageID := gens[0].imageID
	if currentImageID != "" {
		lastImageChange := int64(0)
		for _, g := range gens {
			if g.imageID != currentImageID {
				break
			}
			lastImageChange = g.createdAt
		}
		if lastImageChange > now-W {
			clampedStart = lastImageChange
		}
	}

	// Count distinct hourly buckets within clamped window.
	seenBuckets := map[int64]struct{}{}
	for _, h := range hourly {
		if h.bucket >= clampedStart {
			seenBuckets[h.bucket] = struct{}{}
		}
	}
	sampleCount := len(seenBuckets)

	// Count distinct raw buckets in current hour (within clamped window).
	seenRawBuckets := map[int64]struct{}{}
	for _, r := range raw {
		if r.timestamp >= clampedStart {
			b := r.timestamp - (r.timestamp % 3600)
			seenRawBuckets[b] = struct{}{}
		}
	}
	sampleCount += len(seenRawBuckets)

	if sampleCount < a.cfg.MinSamples {
		return insufficient(sampleCount)
	}

	// Aggregate hourly metrics.
	var cpuWeighted, totalSamples float64
	var netBytes, diskBytes float64
	hasDisk := false
	for _, h := range hourly {
		if h.bucket < clampedStart {
			continue
		}
		sc := float64(h.sampleCount)
		cpuWeighted += h.cpuAvg * sc
		totalSamples += sc
		netBytes += float64(h.netRx + h.netTx)
		diskBytes += float64(h.blkR + h.blkW)
		hasDisk = true
	}

	// Merge current-bucket raw metrics (delta = max - min per counter).
	var rawCPUSum float64
	var rawCount int64
	minNetRx, maxNetRx := int64(math.MaxInt64), int64(math.MinInt64)
	minNetTx, maxNetTx := int64(math.MaxInt64), int64(math.MinInt64)
	minBlkR, maxBlkR := int64(math.MaxInt64), int64(math.MinInt64)
	minBlkW, maxBlkW := int64(math.MaxInt64), int64(math.MinInt64)
	for _, r := range raw {
		if r.timestamp < clampedStart {
			continue
		}
		rawCPUSum += r.cpuPct
		rawCount++
		if r.netRx < minNetRx {
			minNetRx = r.netRx
		}
		if r.netRx > maxNetRx {
			maxNetRx = r.netRx
		}
		if r.netTx < minNetTx {
			minNetTx = r.netTx
		}
		if r.netTx > maxNetTx {
			maxNetTx = r.netTx
		}
		if r.blkR < minBlkR {
			minBlkR = r.blkR
		}
		if r.blkR > maxBlkR {
			maxBlkR = r.blkR
		}
		if r.blkW < minBlkW {
			minBlkW = r.blkW
		}
		if r.blkW > maxBlkW {
			maxBlkW = r.blkW
		}
	}
	if rawCount > 0 {
		rawCPUAvg := rawCPUSum / float64(rawCount)
		cpuWeighted += rawCPUAvg * float64(rawCount)
		totalSamples += float64(rawCount)
		if minNetRx != math.MaxInt64 {
			netBytes += float64((maxNetRx - minNetRx) + (maxNetTx - minNetTx))
		}
		if minBlkR != math.MaxInt64 {
			diskBytes += float64((maxBlkR - minBlkR) + (maxBlkW - minBlkW))
			hasDisk = true
		}
	}

	var cpuAvg float64
	if totalSamples > 0 {
		cpuAvg = cpuWeighted / totalSamples
	}

	windowDays := float64(now-clampedStart) / 86400.0
	if windowDays < 0.001 {
		windowDays = 0.001
	}

	netPerDay := netBytes / windowDays
	diskPerDay := diskBytes / windowDays

	isZombie := cpuAvg < a.cfg.CPUThresholdPct &&
		netPerDay < a.cfg.NetThresholdPerDay &&
		(!hasDisk || diskPerDay < a.cfg.DiskThresholdPerDay)

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
	}
}
