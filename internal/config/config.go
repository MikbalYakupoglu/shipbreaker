package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Config holds all shipbreaker settings. Precedence (high→low): CLI flag > env var > YAML > defaults.
type Config struct {
	// Server
	Bind string `yaml:"bind"`
	Port int    `yaml:"port"`

	// Auth (fail-closed — see Apply)
	User            string `yaml:"-"` // from env only
	Password        string `yaml:"-"` // from env only
	SessionSecret   string `yaml:"-"` // from env only
	TrustedProxies  string `yaml:"trusted_proxies"`

	// Database
	DBPath string `yaml:"db_path"`

	// Retention
	RawRetentionDays    int `yaml:"raw_retention_days"`
	HourlyRetentionDays int `yaml:"hourly_retention_days"`

	// Sampling
	SampleIntervalSec int `yaml:"sample_interval_sec"`

	// Heuristic window
	WindowDays int `yaml:"window_days"`
	MinSamples int `yaml:"min_samples"` // minimum hourly buckets in full W

	// Thresholds
	CPUThresholdPct      float64 `yaml:"cpu_threshold_pct"`       // per-core %
	NetThresholdPerDay   float64 `yaml:"net_threshold_per_day"`   // bytes/day
	DiskThresholdPerDay  float64 `yaml:"disk_threshold_per_day"`  // bytes/day

	// Display timezone
	TZ string `yaml:"tz"`
}

func defaults() Config {
	return Config{
		Bind:                "0.0.0.0",
		Port:                7777,
		DBPath:              "/data/shipbreaker.db",
		RawRetentionDays:    3,
		HourlyRetentionDays: 35,
		SampleIntervalSec:   60,
		WindowDays:          7,
		MinSamples:          84, // 50% of 168 hourly buckets in 7 days
		CPUThresholdPct:     5.0,
		NetThresholdPerDay:  1.5 * 1024 * 1024, // 1.5 MB/day
		DiskThresholdPerDay: 7 * 1024 * 1024,   // 7 MB/day
		TZ:                  "UTC",
	}
}

// Load builds a Config by layering: defaults → YAML file (if path non-empty) → env vars.
// CLI flag overrides are applied afterwards by the caller via the returned *Config.
func Load(yamlPath string) (*Config, error) {
	cfg := defaults()

	if yamlPath != "" {
		data, err := os.ReadFile(yamlPath)
		if err != nil {
			return nil, fmt.Errorf("config: read yaml: %w", err)
		}
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			return nil, fmt.Errorf("config: parse yaml: %w", err)
		}
	}

	// Env vars (override YAML)
	if v := os.Getenv("SHIPBREAKER_BIND"); v != "" {
		cfg.Bind = v
	}
	if v := os.Getenv("SHIPBREAKER_PORT"); v != "" {
		var p int
		if _, err := fmt.Sscanf(v, "%d", &p); err == nil {
			cfg.Port = p
		}
	}
	if v := os.Getenv("SHIPBREAKER_DB_PATH"); v != "" {
		cfg.DBPath = v
	}
	if v := os.Getenv("SHIPBREAKER_TZ"); v == "" {
		if v2 := os.Getenv("TZ"); v2 != "" {
			cfg.TZ = v2
		}
	} else {
		cfg.TZ = v
	}
	if v := os.Getenv("SHIPBREAKER_SAMPLE_INTERVAL_SEC"); v != "" {
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil && n > 0 {
			cfg.SampleIntervalSec = n
		}
	}
	cfg.User = os.Getenv("SHIPBREAKER_USER")
	cfg.Password = os.Getenv("SHIPBREAKER_PASSWORD")
	cfg.SessionSecret = os.Getenv("SHIPBREAKER_SESSION_SECRET")
	if v := os.Getenv("SHIPBREAKER_TRUSTED_PROXIES"); v != "" {
		cfg.TrustedProxies = v
	}

	return &cfg, nil
}

// Validate checks semantic correctness (called after all overrides are applied).
func (c *Config) Validate() error {
	if c.Port < 1 || c.Port > 65535 {
		return fmt.Errorf("config: invalid port %d", c.Port)
	}
	if c.WindowDays < 1 {
		return fmt.Errorf("config: window_days must be >= 1")
	}
	if c.MinSamples < 1 {
		return fmt.Errorf("config: min_samples must be >= 1")
	}
	if c.CPUThresholdPct <= 0 {
		return fmt.Errorf("config: cpu_threshold_pct must be > 0")
	}
	if _, err := time.LoadLocation(c.TZ); err != nil {
		c.TZ = "UTC"
		// non-fatal: caller logs warning
	}
	return nil
}

// IsLoopback returns true if the bind address is a loopback interface.
func IsLoopback(bind string) bool {
	return bind == "127.0.0.1" || bind == "::1" || bind == "localhost"
}
