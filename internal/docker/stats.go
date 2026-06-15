package docker

import (
	"context"
	"encoding/json"
	"math"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
)

// cpuPercent computes per-core CPU% between two consecutive stats readings.
// Returns (percent, false) on success; (0, true) if the sample must be skipped.
func cpuPercent(cur, prev *container.StatsResponse, onlineCPUs uint32) (float64, bool) {
	if prev == nil {
		return 0, true // first reading
	}

	cpuDelta := float64(cur.CPUStats.CPUUsage.TotalUsage) -
		float64(prev.CPUStats.CPUUsage.TotalUsage)
	if cpuDelta < 0 {
		return 0, true // counter reset (container restart)
	}

	sysDelta := float64(cur.CPUStats.SystemUsage) - float64(prev.CPUStats.SystemUsage)
	if sysDelta <= 0 {
		return 0, true // guard against NaN/Inf
	}

	pct := (cpuDelta / sysDelta) * float64(onlineCPUs) * 100.0
	if math.IsNaN(pct) || math.IsInf(pct, 0) {
		return 0, true
	}
	return pct, false
}

// memoryWorkingSet returns RSS-equivalent bytes, excluding page cache.
func memoryWorkingSet(s *container.StatsResponse) int64 {
	usage := int64(s.MemoryStats.Usage)

	// cgroup v2: inactive_file is in Stats map
	if v, ok := s.MemoryStats.Stats["inactive_file"]; ok {
		return usage - int64(v)
	}
	// cgroup v1
	if v, ok := s.MemoryStats.Stats["total_inactive_file"]; ok {
		return usage - int64(v)
	}
	if v, ok := s.MemoryStats.Stats["cache"]; ok {
		return usage - int64(v)
	}
	return usage
}

// networkTotals sums rx/tx bytes across all network interfaces.
func networkTotals(s *container.StatsResponse) (rx, tx int64) {
	for _, n := range s.Networks {
		rx += int64(n.RxBytes)
		tx += int64(n.TxBytes)
	}
	return
}

// blkioTotals sums read/write bytes from blkio stats (cgroup v1; v2 maps here too).
func blkioTotals(s *container.StatsResponse) (read, write int64, ok bool) {
	entries := s.BlkioStats.IoServiceBytesRecursive
	if len(entries) == 0 {
		return 0, 0, false
	}
	ok = true
	for _, e := range entries {
		switch e.Op {
		case "Read":
			read += int64(e.Value)
		case "Write":
			write += int64(e.Value)
		}
	}
	return
}

// fetchStats fetches a single non-streaming stats snapshot for containerID.
func fetchStats(ctx context.Context, cli *client.Client, containerID string) (*container.StatsResponse, error) {
	rc, err := cli.ContainerStats(ctx, containerID, false)
	if err != nil {
		return nil, err
	}
	defer rc.Body.Close()

	var s container.StatsResponse
	if err := json.NewDecoder(rc.Body).Decode(&s); err != nil {
		return nil, err
	}
	return &s, nil
}

// onlineCPUsFromStats resolves the online CPU count from a stats response,
// falling back to a cached host value.
func onlineCPUsFromStats(s *container.StatsResponse, hostCPUs uint32) uint32 {
	if s.CPUStats.OnlineCPUs > 0 {
		return s.CPUStats.OnlineCPUs
	}
	if n := len(s.CPUStats.CPUUsage.PercpuUsage); n > 0 {
		return uint32(n)
	}
	return hostCPUs
}
