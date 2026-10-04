// Package config loads service configuration from environment variables.
package config

import (
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"time"

	"ratelimiter/internal/limiter"
)

type Config struct {
	HTTPAddr        string
	RedisAddr       string
	RedisPassword   string
	RedisDB         int
	RedisPoolSize   int
	RedisTimeout    time.Duration // dial/read/write timeout
	ShutdownTimeout time.Duration

	BatchFlushers int // >0 (default 2) pipelines concurrent decisions to Redis (limiter.Batcher); 0 = one round trip per decision
	BatchMax      int // max decisions per pipeline

	MetricsAddr      string // separate listener for /metrics ("" disables)
	PprofEnabled     bool   // expose /debug/pprof on the metrics listener (profiling; off by default)
	MetricsPerTenant bool   // add a tenant label to decision metrics
	LogLevel         slog.Level

	AdminToken     string        // enables the admin API when non-empty
	AuthMode       string        // "apikey" (default, secure) or "header" (dev: trust X-Tenant-ID)
	TenantCacheTTL time.Duration // staleness bound for cached tenant config

	UpstreamURL string       // if set, the service acts as a rate-limiting reverse proxy for it
	FailOpen    bool         // allow traffic when the limiter backend errors
	DefaultRule limiter.Rule // used until per-tenant config exists (Phase 6) and as the fallback after
}

// Load reads configuration from the environment, applying defaults.
func Load() (Config, error) {
	var err error
	c := Config{
		HTTPAddr:      getStr("RL_HTTP_ADDR", ":8080"),
		RedisAddr:     getStr("RL_REDIS_ADDR", "localhost:6379"),
		RedisPassword: getStr("RL_REDIS_PASSWORD", ""),
	}
	if c.RedisDB, err = getInt("RL_REDIS_DB", 0); err != nil {
		return c, err
	}
	if c.RedisPoolSize, err = getInt("RL_REDIS_POOL_SIZE", 100); err != nil {
		return c, err
	}
	if c.RedisTimeout, err = getDur("RL_REDIS_TIMEOUT", 200*time.Millisecond); err != nil {
		return c, err
	}
	if c.ShutdownTimeout, err = getDur("RL_SHUTDOWN_TIMEOUT", 10*time.Second); err != nil {
		return c, err
	}
	if c.FailOpen, err = getBool("RL_FAIL_OPEN", true); err != nil {
		return c, err
	}
	if c.BatchFlushers, err = getInt("RL_BATCH_FLUSHERS", 2); err != nil {
		return c, err
	}
	if c.BatchMax, err = getInt("RL_BATCH_MAX", 128); err != nil {
		return c, err
	}
	if c.BatchFlushers < 0 || c.BatchMax < 1 {
		return c, fmt.Errorf("config RL_BATCH_FLUSHERS must be >= 0 and RL_BATCH_MAX >= 1")
	}
	c.MetricsAddr = getStr("RL_METRICS_ADDR", ":9090")
	if v, ok := os.LookupEnv("RL_METRICS_ADDR"); ok && v == "" {
		c.MetricsAddr = "" // explicitly empty disables
	}
	if c.PprofEnabled, err = getBool("RL_PPROF_ENABLED", false); err != nil {
		return c, err
	}
	if c.MetricsPerTenant, err = getBool("RL_METRICS_PER_TENANT", true); err != nil {
		return c, err
	}
	if err = c.LogLevel.UnmarshalText([]byte(getStr("RL_LOG_LEVEL", "info"))); err != nil {
		return c, fmt.Errorf("config RL_LOG_LEVEL: %w", err)
	}
	c.AdminToken = getStr("RL_ADMIN_TOKEN", "")
	c.AuthMode = getStr("RL_AUTH_MODE", "apikey")
	if c.AuthMode != "apikey" && c.AuthMode != "header" {
		return c, fmt.Errorf("config RL_AUTH_MODE: must be apikey or header, got %q", c.AuthMode)
	}
	if c.TenantCacheTTL, err = getDur("RL_TENANT_CACHE_TTL", 30*time.Second); err != nil {
		return c, err
	}
	c.UpstreamURL = getStr("RL_UPSTREAM_URL", "")
	c.DefaultRule.Algorithm = limiter.Algorithm(getStr("RL_DEFAULT_ALGORITHM", string(limiter.TokenBucketAlgo)))
	if c.DefaultRule.Limit, err = getInt64("RL_DEFAULT_LIMIT", 100); err != nil {
		return c, err
	}
	if c.DefaultRule.Window, err = getDur("RL_DEFAULT_WINDOW", time.Minute); err != nil {
		return c, err
	}
	if c.DefaultRule.Burst, err = getInt64("RL_DEFAULT_BURST", 0); err != nil {
		return c, err
	}
	if err = c.DefaultRule.Validate(); err != nil {
		return c, fmt.Errorf("config default rule: %w", err)
	}
	return c, nil
}

func getStr(k, def string) string {
	if v, ok := os.LookupEnv(k); ok && v != "" {
		return v
	}
	return def
}

func getInt(k string, def int) (int, error) {
	v, ok := os.LookupEnv(k)
	if !ok || v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("config %s: %w", k, err)
	}
	return n, nil
}

func getDur(k string, def time.Duration) (time.Duration, error) {
	v, ok := os.LookupEnv(k)
	if !ok || v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("config %s: %w", k, err)
	}
	return d, nil
}

func getInt64(k string, def int64) (int64, error) {
	n, err := getInt(k, int(def))
	return int64(n), err
}

func getBool(k string, def bool) (bool, error) {
	v, ok := os.LookupEnv(k)
	if !ok || v == "" {
		return def, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, fmt.Errorf("config %s: %w", k, err)
	}
	return b, nil
}
