package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/events"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/client"
)

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

// RawMetric is one per-interval sample.
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
	MarkExitedIfNotIn(runningIDs []string, at int64) error
}

// Watcher maintains a persistent stats stream per container and writes
// sampled metrics to the Sink on each interval tick.
type Watcher struct {
	cli  *client.Client
	sink Sink
	log  *slog.Logger

	mu           sync.Mutex
	interval     time.Duration
	hostCPUs     uint32
	prevRead     map[string]*container.StatsResponse // last frame used for CPU delta
	statsCache   map[string]*container.StatsResponse // latest frame from stream
	streamCancel map[string]context.CancelFunc        // per-container stream lifecycle
	intervalCh   chan time.Duration
}

func New(cli *client.Client, sink Sink, interval time.Duration, log *slog.Logger) *Watcher {
	return &Watcher{
		cli:          cli,
		sink:         sink,
		interval:     interval,
		log:          log,
		prevRead:     make(map[string]*container.StatsResponse),
		statsCache:   make(map[string]*container.StatsResponse),
		streamCancel: make(map[string]context.CancelFunc),
		intervalCh:   make(chan time.Duration, 1),
	}
}

// SetInterval changes the polling interval at runtime. Safe to call from any goroutine.
func (w *Watcher) SetInterval(d time.Duration) {
	select {
	case <-w.intervalCh:
	default:
	}
	w.intervalCh <- d
}

// Run starts the polling loop and the events listener. Blocks until ctx is cancelled.
func (w *Watcher) Run(ctx context.Context) {
	w.cacheHostCPUs(ctx)

	// Open a stats stream for every container already running at startup.
	if cs, err := w.cli.ContainerList(ctx, container.ListOptions{}); err == nil {
		for _, c := range cs {
			w.startStatsStream(ctx, c.ID)
		}
	}

	go w.runEvents(ctx)

	w.mu.Lock()
	ticker := time.NewTicker(w.interval)
	w.mu.Unlock()
	defer ticker.Stop()

	var pollRunning bool
	var pollMu sync.Mutex

	for {
		select {
		case <-ctx.Done():
			return
		case d := <-w.intervalCh:
			ticker.Reset(d)
			w.mu.Lock()
			w.interval = d
			w.mu.Unlock()
		case <-ticker.C:
			pollMu.Lock()
			if pollRunning {
				w.log.Warn("poll still running from previous tick — skipping")
				pollMu.Unlock()
				continue
			}
			pollRunning = true
			pollMu.Unlock()

			go func() {
				defer func() {
					pollMu.Lock()
					pollRunning = false
					pollMu.Unlock()
				}()
				// Poll is now just DB writes from cache — very fast.
				// Give it a generous timeout in case ContainerList stalls.
				w.mu.Lock()
				cur := w.interval
				w.mu.Unlock()
				pollCtx, cancel := context.WithTimeout(ctx, cur*9/10)
				defer cancel()
				w.poll(pollCtx)
			}()
		}
	}
}

// startStatsStream opens a persistent streaming stats connection for containerID.
// Duplicate calls for the same ID are ignored.
func (w *Watcher) startStatsStream(ctx context.Context, containerID string) {
	w.mu.Lock()
	if _, exists := w.streamCancel[containerID]; exists {
		w.mu.Unlock()
		return
	}
	streamCtx, cancel := context.WithCancel(ctx)
	w.streamCancel[containerID] = cancel
	w.mu.Unlock()

	go w.runStatsStream(streamCtx, containerID)
}

// stopStatsStream cancels the streaming goroutine and clears cached state for containerID.
func (w *Watcher) stopStatsStream(containerID string) {
	w.mu.Lock()
	cancel, ok := w.streamCancel[containerID]
	if ok {
		delete(w.streamCancel, containerID)
		delete(w.statsCache, containerID)
		delete(w.prevRead, containerID)
	}
	w.mu.Unlock()
	if ok {
		cancel() // called outside the lock — goroutine defer also holds w.mu
	}
}

func (w *Watcher) runStatsStream(ctx context.Context, containerID string) {
	defer func() {
		w.mu.Lock()
		delete(w.statsCache, containerID)
		w.mu.Unlock()
	}()

	backoff := time.Second
	for ctx.Err() == nil {
		rc, err := w.cli.ContainerStats(ctx, containerID, true)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			w.log.Warn("stats stream open failed", "id", shortID(containerID), "err", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			if backoff < 30*time.Second {
				backoff *= 2
			}
			continue
		}
		backoff = time.Second // reset on success

		dec := json.NewDecoder(rc.Body)
		for ctx.Err() == nil {
			var s container.StatsResponse
			if err := dec.Decode(&s); err != nil {
				rc.Body.Close()
				if ctx.Err() != nil {
					return
				}
				// Transient decode error — reconnect silently.
				w.log.Debug("stats stream reconnecting", "id", shortID(containerID), "err", err)
				break
			}
			w.mu.Lock()
			w.statsCache[containerID] = &s
			w.mu.Unlock()
		}
		rc.Body.Close()
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

// poll reads the latest stats from each container's in-memory cache and
// writes a metric row to the DB. No HTTP requests are made here.
func (w *Watcher) poll(ctx context.Context) {
	containers, err := w.cli.ContainerList(ctx, container.ListOptions{})
	if err != nil {
		w.log.Error("container list failed", "err", err)
		return
	}

	runningIDs := make([]string, 0, len(containers))
	for _, c := range containers {
		runningIDs = append(runningIDs, c.ID)
		w.processContainer(c)
	}

	if err := w.sink.MarkExitedIfNotIn(runningIDs, time.Now().UTC().Unix()); err != nil {
		w.log.Warn("reconcile exited containers", "err", err)
	}
}

func (w *Watcher) processContainer(c container.Summary) {
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
		w.log.Error("upsert container", "id", c.ID, "err", err)
		return
	}

	if c.State != "running" {
		return
	}

	w.mu.Lock()
	cur := w.statsCache[c.ID]
	prev := w.prevRead[c.ID]
	if cur != nil {
		w.prevRead[c.ID] = cur
	}
	w.mu.Unlock()

	if cur == nil {
		return // stream not yet populated
	}

	onlineCPUs := onlineCPUsFromStats(cur, w.hostCPUs)
	cpu, skip := cpuPercent(cur, prev, onlineCPUs)
	if skip {
		return
	}

	rx, tx := networkTotals(cur)
	blkR, blkW, blkOK := blkioTotals(cur)

	m := RawMetric{
		ContainerID:    c.ID,
		Timestamp:      time.Now().UTC().Unix(),
		CPUPercent:     cpu,
		MemoryWSBytes:  memoryWorkingSet(cur),
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
				backoff = time.Second
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

		w.resync(ctx)
	}
}

func (w *Watcher) handleEvent(ctx context.Context, msg events.Message) {
	id := msg.Actor.ID
	now := time.Now().UTC().Unix()

	switch msg.Action {
	case "start":
		w.updateStatusFromEvent(ctx, id, "running", now)
		w.startStatsStream(ctx, id)
	case "stop", "die", "pause":
		w.updateStatusFromEvent(ctx, id, "exited", now)
		w.stopStatsStream(id)
	case "destroy":
		if err := w.sink.MarkRemoved(id, now); err != nil {
			w.log.Error("mark removed", "id", shortID(id), "err", err)
		}
		w.stopStatsStream(id)
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
	cs, err := w.cli.ContainerList(ctx, container.ListOptions{})
	if err != nil {
		w.log.Warn("resync: container list failed", "err", err)
		return
	}

	runningSet := make(map[string]bool, len(cs))
	for _, c := range cs {
		runningSet[c.ID] = true
	}

	// Stop streams for containers no longer in Docker's running list.
	w.mu.Lock()
	var toCancel []context.CancelFunc
	for id, cancel := range w.streamCancel {
		if !runningSet[id] {
			toCancel = append(toCancel, cancel)
			delete(w.streamCancel, id)
			delete(w.statsCache, id)
		}
	}
	w.mu.Unlock()
	for _, cancel := range toCancel {
		cancel()
	}

	for _, c := range cs {
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
		w.startStatsStream(ctx, c.ID)
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
