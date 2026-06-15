package docker

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/events"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/client"
)

const workerPoolSize = 10

// ContainerInfo holds resolved identity + current status for a container.
type ContainerInfo struct {
	ID         string
	Name       string
	Image      string
	ImageRepo  string
	ImageID    string
	ServiceKey string
	ServiceID  string
	Status     string // "running" | "exited" | "removed"
	CreatedAt  int64  // Unix epoch seconds (Docker Created field)
}

// RawMetric is one per-minute sample.
type RawMetric struct {
	ContainerID    string
	Timestamp      int64
	CPUPercent     float64
	MemoryWSBytes  int64
	NetRxBytes     int64
	NetTxBytes     int64
	BlkReadBytes   int64
	BlkWriteBytes  int64
	BlkDataPresent bool
}

// Sink receives data from the watcher.
type Sink interface {
	UpsertContainer(info ContainerInfo) error
	InsertRawMetric(m RawMetric) error
	MarkRemoved(containerID string, at int64) error
	// MarkExitedIfNotIn sets status='exited' for any running container whose ID
	// is not in runningIDs. Called after each poll to catch containers that
	// stopped while the watcher was down.
	MarkExitedIfNotIn(runningIDs []string, at int64) error
}

// Watcher polls Docker stats every interval and streams events.
type Watcher struct {
	cli      *client.Client
	sink     Sink
	interval time.Duration
	log      *slog.Logger

	mu       sync.Mutex
	prevRead map[string]*container.StatsResponse // containerID → last stats snapshot
	hostCPUs uint32
}

// New creates a Watcher. cli must have been opened with WithAPIVersionNegotiation.
func New(cli *client.Client, sink Sink, interval time.Duration, log *slog.Logger) *Watcher {
	return &Watcher{
		cli:      cli,
		sink:     sink,
		interval: interval,
		log:      log,
		prevRead: make(map[string]*container.StatsResponse),
	}
}

// Run starts the polling loop and events listener. Blocks until ctx is cancelled.
func (w *Watcher) Run(ctx context.Context) {
	w.cacheHostCPUs(ctx)

	go w.runEvents(ctx)

	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	var loopRunning bool
	var loopMu sync.Mutex

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			loopMu.Lock()
			if loopRunning {
				w.log.Warn("stats poll still running from previous tick — skipping")
				loopMu.Unlock()
				continue
			}
			loopRunning = true
			loopMu.Unlock()

			go func() {
				defer func() {
					loopMu.Lock()
					loopRunning = false
					loopMu.Unlock()
				}()
				// Cancel the poll before the next tick so stalled Docker API
				// calls don't pile up across intervals.
				pollCtx, cancel := context.WithTimeout(ctx, w.interval*9/10)
				defer cancel()
				w.poll(pollCtx)
			}()
		}
	}
}

func (w *Watcher) cacheHostCPUs(ctx context.Context) {
	info, err := w.cli.Info(ctx)
	if err != nil {
		w.log.Warn("could not fetch docker info for CPU count", "err", err)
		w.hostCPUs = 1
		return
	}
	w.hostCPUs = uint32(info.NCPU)
}

// poll collects stats for all running containers using a bounded worker pool.
func (w *Watcher) poll(ctx context.Context) {
	containers, err := w.cli.ContainerList(ctx, container.ListOptions{})
	if err != nil {
		w.log.Error("container list failed", "err", err)
		return
	}

	type job struct {
		c container.Summary
	}
	jobs := make(chan job, len(containers))
	runningIDs := make([]string, 0, len(containers))
	for _, c := range containers {
		jobs <- job{c}
		runningIDs = append(runningIDs, c.ID)
	}
	close(jobs)

	var wg sync.WaitGroup
	for i := 0; i < workerPoolSize; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				if ctx.Err() != nil {
					return
				}
				w.processContainer(ctx, j.c)
			}
		}()
	}
	wg.Wait()

	// Reconcile: mark as exited any DB container that Docker no longer reports
	// as running. This catches containers that stopped while the watcher was down.
	if err := w.sink.MarkExitedIfNotIn(runningIDs, time.Now().UTC().Unix()); err != nil {
		w.log.Warn("reconcile exited containers", "err", err)
	}
}

func (w *Watcher) processContainer(ctx context.Context, c container.Summary) {
	key := resolveServiceKey(c)
	id := serviceID(key)

	info := ContainerInfo{
		ID:         c.ID,
		Name:       summaryName(c),
		Image:      c.Image,
		ImageRepo:  imageRepo(c.Image),
		ImageID:    c.ImageID,
		ServiceKey: key,
		ServiceID:  id,
		Status:     normalizeStatus(c.State),
		CreatedAt:  c.Created,
	}
	if err := w.sink.UpsertContainer(info); err != nil {
		w.log.Error("upsert container", "id", c.ID, "err", err)
		return
	}

	if c.State != "running" {
		return
	}

	stats, err := fetchStats(ctx, w.cli, c.ID)
	if err != nil {
		w.log.Warn("fetch stats failed", "id", shortID(c.ID), "err", err)
		return
	}

	w.mu.Lock()
	prev := w.prevRead[c.ID]
	w.prevRead[c.ID] = stats
	w.mu.Unlock()

	onlineCPUs := onlineCPUsFromStats(stats, w.hostCPUs)
	cpu, skip := cpuPercent(stats, prev, onlineCPUs)
	if skip {
		return
	}

	rx, tx := networkTotals(stats)
	blkR, blkW, blkOK := blkioTotals(stats)

	m := RawMetric{
		ContainerID:    c.ID,
		Timestamp:      time.Now().UTC().Unix(),
		CPUPercent:     cpu,
		MemoryWSBytes:  memoryWorkingSet(stats),
		NetRxBytes:     rx,
		NetTxBytes:     tx,
		BlkReadBytes:   blkR,
		BlkWriteBytes:  blkW,
		BlkDataPresent: blkOK,
	}
	if err := w.sink.InsertRawMetric(m); err != nil {
		w.log.Error("insert raw metric", "id", shortID(c.ID), "err", err)
	}
}

// runEvents listens to Docker events and updates container status reactively.
// Reconnects with exponential backoff on disconnect.
func (w *Watcher) runEvents(ctx context.Context) {
	backoff := time.Second
	var since string

	for ctx.Err() == nil {
		f := filters.NewArgs()
		f.Add("type", "container")

		opts := events.ListOptions{Filters: f}
		if since != "" {
			opts.Since = since
		}

		msgCh, errCh := w.cli.Events(ctx, opts)
		w.log.Info("docker events stream connected")

		for loop := true; loop; {
			select {
			case <-ctx.Done():
				return
			case err := <-errCh:
				if ctx.Err() != nil {
					return
				}
				w.log.Warn("docker events stream error", "err", err, "reconnect_in", backoff)
				loop = false
			case msg := <-msgCh:
				since = fmt.Sprintf("%d", msg.TimeNano)
				backoff = time.Second // reset on success
				w.handleEvent(ctx, msg)
			}
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
			if backoff < 60*time.Second {
				backoff *= 2
			}
		}

		// Resync after reconnect
		w.resync(ctx)
	}
}

func (w *Watcher) handleEvent(ctx context.Context, msg events.Message) {
	id := msg.Actor.ID
	now := time.Now().UTC().Unix()

	switch msg.Action {
	case "start":
		w.updateStatusFromEvent(ctx, id, "running", now)
	case "stop", "die", "pause":
		w.updateStatusFromEvent(ctx, id, "exited", now)
	case "destroy":
		if err := w.sink.MarkRemoved(id, now); err != nil {
			w.log.Error("mark removed", "id", shortID(id), "err", err)
		}
		w.mu.Lock()
		delete(w.prevRead, id)
		w.mu.Unlock()
	}
}

func (w *Watcher) updateStatusFromEvent(ctx context.Context, id, status string, _ int64) {
	data, err := w.cli.ContainerInspect(ctx, id)
	if err != nil {
		w.log.Warn("inspect on event failed", "id", shortID(id), "err", err)
		return
	}
	info := containerInfoFromInspect(data, status)
	if err := w.sink.UpsertContainer(info); err != nil {
		w.log.Error("upsert on event", "id", shortID(id), "err", err)
	}
}

func (w *Watcher) resync(ctx context.Context) {
	containers, err := w.cli.ContainerList(ctx, container.ListOptions{})
	if err != nil {
		w.log.Warn("resync: container list failed", "err", err)
		return
	}
	for _, c := range containers {
		key := resolveServiceKey(c)
		info := ContainerInfo{
			ID:         c.ID,
			Name:       summaryName(c),
			Image:      c.Image,
			ImageRepo:  imageRepo(c.Image),
			ImageID:    c.ImageID,
			ServiceKey: key,
			ServiceID:  serviceID(key),
			Status:     normalizeStatus(c.State),
			CreatedAt:  c.Created,
		}
		if err := w.sink.UpsertContainer(info); err != nil {
			w.log.Warn("resync: upsert failed", "id", shortID(c.ID), "err", err)
		}
	}
}

func summaryName(c container.Summary) string {
	if len(c.Names) == 0 {
		return ""
	}
	name := c.Names[0]
	if len(name) > 0 && name[0] == '/' {
		return name[1:]
	}
	return name
}

func normalizeStatus(state string) string {
	switch state {
	case "running":
		return "running"
	default:
		return "exited"
	}
}

func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}
