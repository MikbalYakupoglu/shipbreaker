package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/mikbal/shipbreaker/internal/analyzer"
	"github.com/mikbal/shipbreaker/internal/config"
)

// IntervalSetter can update the watcher's polling interval at runtime.
type IntervalSetter interface {
	SetInterval(d time.Duration)
}

// Server is the HTTP API server.
type Server struct {
	router          *chi.Mux
	db              *sql.DB
	an              *analyzer.Analyzer
	auth            *authState // nil if auth disabled
	rl              *rateLimit
	trustedCIDRs    []*net.IPNet
	cfg             *config.Config
	watcher         IntervalSetter
	defaultInterval time.Duration
	log             *slog.Logger
}

// New builds and wires up the chi router.
func New(reader *sql.DB, an *analyzer.Analyzer, cfg *config.Config, auth *authState, watcher IntervalSetter, log *slog.Logger) *Server {
	s := &Server{
		router:          chi.NewRouter(),
		db:              reader,
		an:              an,
		auth:            auth,
		rl:              newRateLimit(),
		cfg:             cfg,
		watcher:         watcher,
		defaultInterval: time.Duration(cfg.SampleIntervalSec) * time.Second,
		log:             log,
	}
	if cfg.TrustedProxies != "" {
		for _, cidr := range splitTrim(cfg.TrustedProxies) {
			_, ipnet, err := net.ParseCIDR(cidr)
			if err == nil {
				s.trustedCIDRs = append(s.trustedCIDRs, ipnet)
			}
		}
	}
	s.routes()
	return s
}

func (s *Server) Handler() http.Handler { return s.router }

func (s *Server) routes() {
	r := s.router
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)

	// Auth-exempt endpoints (Y3)
	r.Get("/healthz", s.handleHealthz)
	r.Post("/api/login", s.handleLogin)
	r.Get("/api/config", s.handleConfig)

	// SPA static files — auth-exempt (login page must load without auth)
	r.Handle("/*", staticHandler())

	// Protected group
	if s.auth != nil {
		r.Group(func(r chi.Router) {
			r.Use(sessionMiddleware(s.auth))
			s.protectedRoutes(r)
		})
	} else {
		// No-auth mode: mount directly
		r.Group(func(r chi.Router) {
			s.protectedRoutes(r)
		})
	}
}

func (s *Server) protectedRoutes(r chi.Router) {
	r.Get("/api/zombies", s.handleZombies)
	r.Get("/api/snapshot", s.handleSnapshot)
	r.Get("/api/services/{serviceID}/metrics", s.handleServiceMetrics)
	r.Get("/api/containers", s.handleContainers)
	r.Get("/api/containers/{id}", s.handleContainer)
	r.Get("/api/containers/{id}/metrics", s.handleContainerRawMetrics)
	r.Post("/api/live", s.handleLive)
}

// handleLive toggles the backend sampling interval between the default and 5 s.
func (s *Server) handleLive(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	var d time.Duration
	var intervalSec int
	if body.Enabled {
		d = time.Duration(s.cfg.LiveIntervalSec) * time.Second
		intervalSec = s.cfg.LiveIntervalSec
	} else {
		d = s.defaultInterval
		intervalSec = s.cfg.SampleIntervalSec
	}
	s.watcher.SetInterval(d)
	jsonOK(w, map[string]int{"interval_sec": intervalSec})
}

// handleHealthz — minimal, auth-free (Y3)
func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("ok"))
}

// handleConfig — auth-free, non-sensitive settings (Y3)
func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	jsonOK(w, map[string]any{
		"tz":                  s.cfg.TZ,
		"auth_required":       s.auth != nil,
		"sample_interval_sec": s.cfg.SampleIntervalSec,
		"live_interval_sec":   s.cfg.LiveIntervalSec,
	})
}

// handleLogin authenticates a user and sets a session cookie.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if s.auth == nil {
		jsonOK(w, map[string]string{"status": "no_auth"})
		return
	}

	ip := clientIP(r, s.trustedCIDRs)
	if !s.rl.allowed(ip) {
		http.Error(w, "too many attempts", http.StatusTooManyRequests)
		return
	}

	var creds struct {
		User     string `json:"user"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&creds); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	if !s.auth.verify(creds.User, creds.Password) {
		s.rl.record(ip, false)
		http.Error(w, "invalid credentials", http.StatusUnauthorized)
		return
	}
	s.rl.record(ip, true)

	token := s.auth.createSession()
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   86400 * 7,
	})
	jsonOK(w, map[string]string{"status": "ok"})
}

// handleZombies returns the analyzer's current verdict for all services,
// enriched with the latest raw metric snapshot per service.
func (s *Server) handleZombies(w http.ResponseWriter, r *http.Request) {
	results, err := s.an.Run(r.Context())
	if err != nil {
		s.log.Error("handleZombies: analyzer run failed", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Fetch latest raw metric snapshot for all services in one query.
	latestMetrics := s.latestMetricsPerService(r.Context())

	type row struct {
		ServiceID    string   `json:"service_id"`
		ServiceKey   string   `json:"service_key"`
		Name         string   `json:"name"`
		Image        string   `json:"image"`
		Status       string   `json:"status"`
		CPUAvg       float64  `json:"cpu_avg_pct"`
		NetBytesDay  float64  `json:"net_bytes_per_day"`
		DiskBytesDay float64  `json:"disk_bytes_per_day"`
		WindowDays   float64  `json:"window_days"`
		SampleCount  int      `json:"sample_count"`
		Latest       svcSnap  `json:"latest"`
	}
	out := make([]row, 0, len(results))
	for _, res := range results {
		r2 := row{
			ServiceID:    res.ServiceID,
			ServiceKey:   res.ServiceKey,
			Name:         res.Name,
			Image:        res.Image,
			Status:       string(res.Status),
			CPUAvg:       res.CPUAvg,
			NetBytesDay:  res.NetBytesDay,
			DiskBytesDay: res.DiskBytesDay,
			WindowDays:   res.WindowDays,
			SampleCount:  res.SampleCount,
			Latest:       latestMetrics[res.ServiceID],
		}
		out = append(out, r2)
	}
	jsonOK(w, out)
}

// svcSnap holds the latest raw metric snapshot for a service.
type svcSnap struct {
	CPU   *float64 `json:"latest_cpu_pct"`
	Mem   *int64   `json:"latest_mem_bytes"`
	NetRx *int64   `json:"latest_net_rx_bytes"`
	NetTx *int64   `json:"latest_net_tx_bytes"`
	BlkR  *int64   `json:"latest_blk_read_bytes"`
	BlkW  *int64   `json:"latest_blk_write_bytes"`
}

// latestMetricsPerService returns the most recent raw metric for each service_id.
func (s *Server) latestMetricsPerService(ctx context.Context) map[string]svcSnap {
	result := map[string]svcSnap{}

	rows, err := s.db.QueryContext(ctx, `
		SELECT c.service_id,
		       r.cpu_percent, r.memory_ws_bytes,
		       r.net_rx_bytes, r.net_tx_bytes,
		       r.blk_read_bytes, r.blk_write_bytes
		FROM metrics_raw r
		JOIN containers c ON c.id = r.container_id
		WHERE r.timestamp = (
		    SELECT MAX(r2.timestamp)
		    FROM metrics_raw r2
		    JOIN containers c2 ON c2.id = r2.container_id
		    WHERE c2.service_id = c.service_id
		)
		GROUP BY c.service_id
	`)
	if err != nil {
		return result
	}
	defer rows.Close()

	for rows.Next() {
		var svcID string
		var cpu float64
		var mem, netRx, netTx, blkR, blkW int64
		if err := rows.Scan(&svcID, &cpu, &mem, &netRx, &netTx, &blkR, &blkW); err != nil {
			continue
		}
		cpuP := cpu
		result[svcID] = svcSnap{
			CPU:   &cpuP,
			Mem:   &mem,
			NetRx: &netRx,
			NetTx: &netTx,
			BlkR:  &blkR,
			BlkW:  &blkW,
		}
	}
	return result
}

// handleSnapshot returns the latest raw metric snapshot per service.
// Lightweight: no analyzer run, just one DB query — safe to poll every few seconds.
func (s *Server) handleSnapshot(w http.ResponseWriter, r *http.Request) {
	snaps := s.latestMetricsPerService(r.Context())
	jsonOK(w, snaps)
}

// handleServiceMetrics returns hourly metric history for a service.
func (s *Server) handleServiceMetrics(w http.ResponseWriter, r *http.Request) {
	svcID := chi.URLParam(r, "serviceID")

	rows, err := s.db.QueryContext(r.Context(), `
		SELECT h.bucket, h.cpu_percent_avg, h.cpu_percent_max,
		       h.memory_ws_avg, h.net_rx_delta, h.net_tx_delta,
		       h.blk_read_delta, h.blk_write_delta, h.sample_count
		FROM metrics_hourly h
		JOIN containers c ON c.id = h.container_id
		WHERE c.service_id = ?
		ORDER BY h.bucket ASC
	`, svcID)
	if err != nil {
		s.log.Error("handleServiceMetrics: query failed", "service_id", svcID, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	type point struct {
		Bucket       string  `json:"bucket"` // UTC ISO-8601
		CPUAvg       float64 `json:"cpu_avg_pct"`
		CPUMax       float64 `json:"cpu_max_pct"`
		MemAvg       int64   `json:"memory_ws_avg_bytes"`
		NetRxDelta   int64   `json:"net_rx_bytes"`
		NetTxDelta   int64   `json:"net_tx_bytes"`
		BlkReadDelta int64   `json:"blk_read_bytes"`
		BlkWriteDelta int64  `json:"blk_write_bytes"`
		SampleCount  int     `json:"sample_count"`
	}
	points := []point{}
	for rows.Next() {
		var p point
		var bucket int64
		if err := rows.Scan(&bucket, &p.CPUAvg, &p.CPUMax, &p.MemAvg,
			&p.NetRxDelta, &p.NetTxDelta, &p.BlkReadDelta, &p.BlkWriteDelta,
			&p.SampleCount); err != nil {
			continue
		}
		p.Bucket = time.Unix(bucket, 0).UTC().Format(time.RFC3339)
		points = append(points, p)
	}
	jsonOK(w, points)
}

// handleContainers returns one row per (name, image) pair — the running container
// if one exists, otherwise the most recently seen. This deduplicates the list when
// a service has been restarted multiple times.
func (s *Server) handleContainers(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.QueryContext(r.Context(), `
		WITH ranked AS (
			SELECT id, name, image, service_key, service_id, status, created_at, last_seen_at,
				ROW_NUMBER() OVER (
					PARTITION BY name, image
					ORDER BY
						CASE WHEN status = 'running' THEN 0 ELSE 1 END,
						last_seen_at DESC
				) AS rn
			FROM containers
		)
		SELECT id, name, image, service_key, service_id, status, created_at, last_seen_at
		FROM ranked WHERE rn = 1
		ORDER BY last_seen_at DESC
	`)
	if err != nil {
		s.log.Error("handleContainers: query failed", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	type cRow struct {
		ID         string `json:"id"`
		Name       string `json:"name"`
		Image      string `json:"image"`
		ServiceKey string `json:"service_key"`
		ServiceID  string `json:"service_id"`
		Status     string `json:"status"`
		CreatedAt  string `json:"created_at"`
		LastSeenAt string `json:"last_seen_at"`
	}
	var out []cRow
	for rows.Next() {
		var c cRow
		var createdAt, lastSeenAt int64
		if err := rows.Scan(&c.ID, &c.Name, &c.Image, &c.ServiceKey, &c.ServiceID,
			&c.Status, &createdAt, &lastSeenAt); err != nil {
			continue
		}
		c.CreatedAt = time.Unix(createdAt, 0).UTC().Format(time.RFC3339)
		c.LastSeenAt = time.Unix(lastSeenAt, 0).UTC().Format(time.RFC3339)
		out = append(out, c)
	}
	jsonOK(w, out)
}

// handleContainer returns a single container with latest metrics snapshot.
func (s *Server) handleContainer(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	var name, image, serviceKey, serviceID, status string
	var createdAt, firstSeenAt, lastSeenAt int64
	err := s.db.QueryRowContext(r.Context(), `
		SELECT name, image, service_key, service_id, status, created_at, first_seen_at, last_seen_at
		FROM containers WHERE id = ?
	`, id).Scan(&name, &image, &serviceKey, &serviceID, &status,
		&createdAt, &firstSeenAt, &lastSeenAt)
	if err == sql.ErrNoRows {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.log.Error("handleContainer: query failed", "container_id", id, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Latest raw metric snapshot
	var latestCPU, latestMem, latestNetRx, latestNetTx, latestBlkR, latestBlkW sql.NullFloat64
	_ = s.db.QueryRowContext(r.Context(), `
		SELECT cpu_percent, memory_ws_bytes, net_rx_bytes, net_tx_bytes,
		       blk_read_bytes, blk_write_bytes
		FROM metrics_raw WHERE container_id = ? ORDER BY timestamp DESC LIMIT 1
	`, id).Scan(&latestCPU, &latestMem, &latestNetRx, &latestNetTx, &latestBlkR, &latestBlkW)

	out := map[string]any{
		"id":            id,
		"name":          name,
		"image":         image,
		"service_key":   serviceKey,
		"service_id":    serviceID,
		"status":        status,
		"created_at":    time.Unix(createdAt, 0).UTC().Format(time.RFC3339),
		"first_seen_at": time.Unix(firstSeenAt, 0).UTC().Format(time.RFC3339),
		"last_seen_at":  time.Unix(lastSeenAt, 0).UTC().Format(time.RFC3339),
	}
	if latestCPU.Valid {
		out["latest_cpu_pct"] = latestCPU.Float64
		out["latest_mem_bytes"] = int64(latestMem.Float64)
		out["latest_net_rx_bytes"] = int64(latestNetRx.Float64)
		out["latest_net_tx_bytes"] = int64(latestNetTx.Float64)
		out["latest_blk_read_bytes"] = int64(latestBlkR.Float64)
		out["latest_blk_write_bytes"] = int64(latestBlkW.Float64)
	}
	jsonOK(w, out)
}

// handleContainerRawMetrics returns the last 60 raw samples (~1 hour) for sparklines.
func (s *Server) handleContainerRawMetrics(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	rows, err := s.db.QueryContext(r.Context(), `
		SELECT timestamp, cpu_percent, memory_ws_bytes,
		       net_rx_bytes, net_tx_bytes, blk_read_bytes, blk_write_bytes
		FROM metrics_raw WHERE container_id = ?
		ORDER BY timestamp DESC LIMIT 60
	`, id)
	if err != nil {
		s.log.Error("handleContainerRawMetrics: query failed", "container_id", id, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	type pt struct {
		T      string  `json:"t"`
		CPU    float64 `json:"cpu_pct"`
		Mem    int64   `json:"mem_bytes"`
		NetRx  int64   `json:"net_rx_bytes"`
		NetTx  int64   `json:"net_tx_bytes"`
		BlkR   int64   `json:"blk_read_bytes"`
		BlkW   int64   `json:"blk_write_bytes"`
	}
	var pts []pt
	for rows.Next() {
		var p pt
		var ts int64
		var mem, netRx, netTx, blkR, blkW int64
		if err := rows.Scan(&ts, &p.CPU, &mem, &netRx, &netTx, &blkR, &blkW); err != nil {
			continue
		}
		p.T = time.Unix(ts, 0).UTC().Format(time.RFC3339)
		p.Mem = mem
		p.NetRx = netRx
		p.NetTx = netTx
		p.BlkR = blkR
		p.BlkW = blkW
		pts = append(pts, p)
	}
	// Reverse to chronological order
	for i, j := 0, len(pts)-1; i < j; i, j = i+1, j-1 {
		pts[i], pts[j] = pts[j], pts[i]
	}
	jsonOK(w, pts)
}

func jsonOK(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(v)
}

func splitTrim(s string) []string {
	var out []string
	for _, p := range splitComma(s) {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
