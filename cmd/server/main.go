package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/pprof"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"

	"ratelimiter/internal/admin"
	"ratelimiter/internal/auth"
	"ratelimiter/internal/config"
	"ratelimiter/internal/decision"
	"ratelimiter/internal/limiter"
	"ratelimiter/internal/metrics"
	"ratelimiter/internal/server"
	"ratelimiter/internal/store"
	"ratelimiter/internal/tenant"
)

var version = "dev" // overridden with -ldflags "-X main.version=..."

func main() {
	cfg, err := config.Load()
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	if err == nil {
		err = run(log, cfg)
	}
	if err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger, cfg config.Config) error {
	var err error
	var upstream *url.URL
	if cfg.UpstreamURL != "" {
		if upstream, err = url.Parse(cfg.UpstreamURL); err != nil || upstream.Host == "" {
			return fmt.Errorf("config RL_UPSTREAM_URL %q: must be an absolute URL", cfg.UpstreamURL)
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	rdb, err := store.NewRedis(ctx, cfg)
	if err != nil {
		return err
	}
	defer rdb.Close()
	log.Info("redis connected", "addr", cfg.RedisAddr, "pool", cfg.RedisPoolSize)

	var scripter redis.Scripter = rdb
	if cfg.BatchFlushers > 0 {
		b := limiter.NewBatcher(rdb, cfg.BatchFlushers, cfg.BatchMax, cfg.RedisTimeout)
		defer b.Close()
		scripter = b
		log.Info("redis pipelining enabled", "flushers", cfg.BatchFlushers, "max_batch", cfg.BatchMax)
	}
	m := metrics.New(cfg.MetricsPerTenant, version)
	tstore := tenant.NewStore(rdb)
	provider := tenant.NewProvider(rdb, tstore, cfg.DefaultRule, cfg.TenantCacheTTL, log)
	provider.ObserveCache(m.ObserveCache)
	go provider.Watch(ctx) // hot reload: apply config changes made on any instance

	var identifier auth.Identifier = provider
	if cfg.AuthMode == "header" {
		identifier = auth.HeaderIdentifier{}
		log.Warn("RL_AUTH_MODE=header: tenant is taken from X-Tenant-ID without authentication; development only")
	}
	var adminHandler http.Handler
	if cfg.AdminToken != "" {
		adminHandler = admin.New(tstore, cfg.AdminToken, log)
	} else {
		log.Info("admin API disabled (set RL_ADMIN_TOKEN to enable)")
	}

	srv := server.New(server.Deps{
		Addr:       cfg.HTTPAddr,
		Ready:      func(ctx context.Context) error { return rdb.Ping(ctx).Err() },
		Checker:    decision.Checker{Limiter: limiter.NewRouter(scripter), Rules: provider, Observer: m},
		Identifier: identifier,
		Admin:      adminHandler,
		Upstream:   upstream,
		FailOpen:   cfg.FailOpen,
		Log:        log,
		Metrics:    m,
	})

	// /metrics lives on its own listener so it is never exposed on the public/gateway port.
	var msrv *http.Server
	if cfg.MetricsAddr != "" {
		mux := http.NewServeMux()
		mux.Handle("GET /metrics", m.Handler())
		if cfg.PprofEnabled { // same private listener as /metrics; never on the public port
			mux.HandleFunc("/debug/pprof/", pprof.Index)
			mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
			mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
			log.Warn("pprof enabled on metrics listener", "addr", cfg.MetricsAddr)
		}
		msrv = &http.Server{Addr: cfg.MetricsAddr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	}

	errCh := make(chan error, 2)
	if msrv != nil {
		go func() {
			log.Info("metrics listening", "addr", cfg.MetricsAddr)
			if err := msrv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
				errCh <- err
			}
		}()
	}
	go func() {
		log.Info("http listening", "addr", cfg.HTTPAddr, "upstream", cfg.UpstreamURL,
			"default_rule", cfg.DefaultRule.Algorithm, "limit", cfg.DefaultRule.Limit, "window", cfg.DefaultRule.Window)
		if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		log.Info("shutting down")
	}
	sctx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	if msrv != nil {
		_ = msrv.Shutdown(sctx)
	}
	return srv.Shutdown(sctx)
}
