package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	dockerclient "github.com/docker/docker/client"
	"github.com/mikbal/shipbreaker/internal/analyzer"
	"github.com/mikbal/shipbreaker/internal/api"
	"github.com/mikbal/shipbreaker/internal/config"
	"github.com/mikbal/shipbreaker/internal/docker"
	"github.com/mikbal/shipbreaker/internal/logger"
	"github.com/mikbal/shipbreaker/internal/storage"
	"github.com/spf13/cobra"
)

var (
	cfgFile  string
	bindAddr string
	port     int
	dbPath   string
	tz       string
)

func main() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

var rootCmd = &cobra.Command{
	Use:   "breaker",
	Short: "Shipbreaker — zombie Docker container detector",
	Long: `Shipbreaker monitors Docker containers and identifies zombie services:
containers that are running but consuming negligible CPU, network, and disk I/O
over a configurable observation window.

Configuration precedence (high → low):
  CLI flag  >  environment variable  >  YAML config file  >  built-in defaults

Key environment variables:
  SHIPBREAKER_USER              HTTP basic-auth username
  SHIPBREAKER_PASSWORD          HTTP basic-auth password
  SHIPBREAKER_SESSION_SECRET    session cookie signing secret
  SHIPBREAKER_BIND              bind address   (default: 0.0.0.0)
  SHIPBREAKER_PORT              listen port    (default: 7777)
  SHIPBREAKER_DB_PATH           SQLite path    (default: /data/shipbreaker.db)
  SHIPBREAKER_TZ                display tz     (default: UTC)`,
}

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start the shipbreaker daemon (watcher + API + UI)",
	Long: `Start the long-running shipbreaker daemon, which runs three background jobs:

  Watcher   — samples every container's CPU, network, and disk I/O once per
              SAMPLE_INTERVAL_SEC (default 60 s) and writes raw rows to SQLite.

  Aggregator — rolls raw rows into hourly buckets every hour to keep the
               database small while preserving per-service history.

  Retention  — prunes rows older than RAW_RETENTION_DAYS (raw) and
               HOURLY_RETENTION_DAYS (hourly) once per hour.

The HTTP API and embedded web UI are served on BIND:PORT.

Auth:
  SHIPBREAKER_USER + SHIPBREAKER_PASSWORD must be set when binding to a
  non-loopback address (e.g. 0.0.0.0). On 127.0.0.1 the server starts
  without auth but logs a warning.`,
	Example: `  # Quickstart (loopback, no auth required)
  breaker serve --bind 127.0.0.1 --port 7777

  # Production (set credentials via env, use a config file)
  SHIPBREAKER_USER=admin SHIPBREAKER_PASSWORD=s3cr3t \
    breaker serve --config /etc/shipbreaker/config.yaml

  # Override just the port
  breaker serve --port 8080`,
	RunE: runServe,
}

var scanCmd = &cobra.Command{
	Use:   "scan",
	Short: "Run a one-shot zombie scan and print results (read-only)",
	Long: `Run the zombie heuristic against the existing data in the SQLite database
and print each service's verdict to stdout. No Docker daemon connection is
made and no new samples are written — the database is opened read-only.

A service is classified as:
  ZOMBIE   — below ALL configured thresholds for the full observation window
  ACTIVE   — above at least one threshold
  UNKNOWN  — not enough hourly samples yet (< MIN_SAMPLES)

Thresholds (configurable via YAML or defaults):
  cpu_threshold_pct       per-core CPU %     (default 5 %)
  net_threshold_per_day   network bytes/day  (default 1.5 MB)
  disk_threshold_per_day  disk I/O bytes/day (default 7 MB)`,
	Example: `  # Scan with default settings
  breaker scan

  # Point at a non-default database
  breaker scan --db /var/lib/shipbreaker/ship.db

  # Use a config file for thresholds
  breaker scan --config /etc/shipbreaker/config.yaml`,
	RunE: runScan,
}

func init() {
	serveCmd.Flags().StringVar(&cfgFile, "config", "", "path to YAML config file")
	serveCmd.Flags().StringVar(&bindAddr, "bind", "", "bind address")
	serveCmd.Flags().IntVar(&port, "port", 0, "listen port")
	serveCmd.Flags().StringVar(&dbPath, "db", "", "SQLite database path")
	serveCmd.Flags().StringVar(&tz, "tz", "", "display timezone")

	scanCmd.Flags().StringVar(&cfgFile, "config", "", "path to YAML config file")
	scanCmd.Flags().StringVar(&dbPath, "db", "", "SQLite database path")
	scanCmd.Flags().StringVar(&tz, "tz", "", "display timezone")

	rootCmd.AddCommand(serveCmd)
	rootCmd.AddCommand(scanCmd)
}

func loadConfig() (*config.Config, *slog.Logger, error) {
	log := logger.New()
	cfg, err := config.Load(cfgFile)
	if err != nil {
		return nil, log, err
	}
	if bindAddr != "" {
		cfg.Bind = bindAddr
	}
	if port != 0 {
		cfg.Port = port
	}
	if dbPath != "" {
		cfg.DBPath = dbPath
	}
	if tz != "" {
		cfg.TZ = tz
	}
	if err := cfg.Validate(); err != nil {
		return nil, log, err
	}
	return cfg, log, nil
}

func runServe(cmd *cobra.Command, args []string) error {
	cfg, log, err := loadConfig()
	if err != nil {
		return err
	}

	// Fail-closed auth check (Y2)
	hasCredentials := cfg.User != "" && cfg.Password != ""
	isLoopback := config.IsLoopback(cfg.Bind)

	if !hasCredentials {
		if isLoopback {
			log.Warn("no credentials set — running without auth (loopback only)",
				"bind", cfg.Bind)
		} else {
			return fmt.Errorf(
				"fatal: SHIPBREAKER_USER and SHIPBREAKER_PASSWORD must be set "+
					"when binding to %s. Set credentials or use bind=127.0.0.1", cfg.Bind)
		}
	}

	log.Info("shipbreaker starting",
		"bind", cfg.Bind, "port", cfg.Port,
		"db", cfg.DBPath, "tz", cfg.TZ, "auth", hasCredentials)

	// Open database
	writer, reader, err := storage.Open(cfg.DBPath)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer writer.Close()
	defer reader.Close()

	repo := storage.NewRepository(writer, reader)

	// Open Docker client
	cli, err := dockerclient.NewClientWithOpts(
		dockerclient.FromEnv,
		dockerclient.WithAPIVersionNegotiation(),
	)
	if err != nil {
		return fmt.Errorf("docker client: %w", err)
	}
	defer cli.Close()

	// Build components
	interval := time.Duration(cfg.SampleIntervalSec) * time.Second
	watcher := docker.New(cli, repo, interval, log)

	an := analyzer.New(reader, analyzer.Config{
		WindowDays:          cfg.WindowDays,
		MinSamples:          cfg.MinSamples,
		CPUThresholdPct:     cfg.CPUThresholdPct,
		NetThresholdPerDay:  cfg.NetThresholdPerDay,
		DiskThresholdPerDay: cfg.DiskThresholdPerDay,
	})

	agg := storage.NewAggregator(writer, reader, log)
	ret := storage.NewRetention(writer, cfg.RawRetentionDays, cfg.HourlyRetentionDays, log)

	// Resolve session secret: env var takes priority; otherwise persist in DB
	// so the secret survives container restarts (sessions stay valid).
	sessionSecret := cfg.SessionSecret
	if sessionSecret == "" {
		sessionSecret, err = storage.GetOrCreateSecret(writer, "session_secret")
		if err != nil {
			return fmt.Errorf("session secret: %w", err)
		}
	}

	// Auth
	var authState *api.AuthState
	if hasCredentials {
		authState, err = api.NewAuthState(cfg.User, cfg.Password, sessionSecret)
		if err != nil {
			return fmt.Errorf("auth init: %w", err)
		}
	}

	srv := api.New(reader, an, cfg, authState, watcher, log)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Background goroutines
	go watcher.Run(ctx)

	go func() {
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		// Initial run
		if err := agg.Run(ctx); err != nil {
			log.Error("initial aggregate", "err", err)
		}
		if err := ret.Run(ctx); err != nil {
			log.Error("initial retention", "err", err)
		}
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := agg.Run(ctx); err != nil {
					log.Error("aggregate", "err", err)
				}
				if err := ret.Run(ctx); err != nil {
					log.Error("retention", "err", err)
				}
			}
		}
	}()

	addr := fmt.Sprintf("%s:%d", cfg.Bind, cfg.Port)
	httpSrv := &http.Server{
		Addr:        addr,
		Handler:     srv.Handler(),
		BaseContext: func(l net.Listener) context.Context { return ctx },
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", addr)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	select {
	case <-ctx.Done():
		log.Info("shutting down")
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		httpSrv.Shutdown(shutCtx)
	case err := <-errCh:
		return err
	}
	return nil
}

func runScan(cmd *cobra.Command, args []string) error {
	cfg, log, err := loadConfig()
	if err != nil {
		return err
	}
	_, reader, err := storage.Open(cfg.DBPath)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer reader.Close()

	an := analyzer.New(reader, analyzer.Config{
		WindowDays:          cfg.WindowDays,
		MinSamples:          cfg.MinSamples,
		CPUThresholdPct:     cfg.CPUThresholdPct,
		NetThresholdPerDay:  cfg.NetThresholdPerDay,
		DiskThresholdPerDay: cfg.DiskThresholdPerDay,
	})

	results, err := an.Run(context.Background())
	if err != nil {
		return err
	}

	for _, r := range results {
		log.Info("service",
			"status", r.Status,
			"service_key", r.ServiceKey,
			"cpu_avg", fmt.Sprintf("%.2f%%", r.CPUAvg),
			"net_mb_day", fmt.Sprintf("%.2f", r.NetBytesDay/1024/1024),
			"window_days", fmt.Sprintf("%.1f", r.WindowDays),
			"samples", r.SampleCount,
		)
	}
	if len(results) == 0 {
		fmt.Println("no services with sufficient data yet")
	}
	return nil
}
