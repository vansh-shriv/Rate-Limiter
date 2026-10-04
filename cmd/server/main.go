package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"syscall"

	"ratelimiter/internal/config"
	"ratelimiter/internal/decision"
	"ratelimiter/internal/limiter"
	"ratelimiter/internal/rules"
	"ratelimiter/internal/server"
	"ratelimiter/internal/store"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(log); err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
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

	srv := server.New(server.Deps{
		Addr:     cfg.HTTPAddr,
		Ready:    func(ctx context.Context) error { return rdb.Ping(ctx).Err() },
		Checker:  decision.Checker{Limiter: limiter.NewRouter(rdb), Rules: rules.NewStatic(cfg.DefaultRule)},
		Upstream: upstream,
		FailOpen: cfg.FailOpen,
		Log:      log,
	})

	errCh := make(chan error, 1)
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
	return srv.Shutdown(sctx)
}
