package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/WllowyEmmie/trucking-logistics/internal/api"
	"github.com/WllowyEmmie/trucking-logistics/internal/config"
	"github.com/WllowyEmmie/trucking-logistics/internal/metrics"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
)

func main() {
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	if err := godotenv.Load(); err != nil {
		logger.Info("no .env file found, using environment variables")
	}

	cfg, err := config.Load(logger)
	if err != nil {
		logger.Error("load config", "err", err)
		os.Exit(1)
	}

	poolCfg, err := pgxpool.ParseConfig(cfg.DatabaseURL)
	if err != nil {
		logger.Error("parse DATABASE_URL", "err", err)
		os.Exit(1)
	}
	// Every query is timed for /metrics; slow ones are logged (SLOW_QUERY_MS).
	poolCfg.ConnConfig.Tracer = &metrics.QueryTracer{Reg: metrics.Default, Logger: logger, Slow: cfg.SlowQuery}
	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		logger.Error("create pool", "err", err)
		os.Exit(1)
	}
	defer pool.Close()

	if err := pool.Ping(ctx); err != nil {
		logger.Error("ping db", "err", err)
		os.Exit(1)
	}
	logger.Info("connected to database")

	srv := api.NewServer(pool, logger, cfg)

	httpSrv := &http.Server{
		Addr:    ":" + cfg.Port,
		Handler: srv.Routes(),
		// Without these a client that sends headers slowly holds a
		// connection open forever. No WriteTimeout: /ws stays open, and the
		// other routes have their own 15 s timeout in Routes().
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	// Ctrl-C or a container stop: let in-flight requests finish (a commit
	// half-done is worse than a slow exit), then close the pool.
	stop, cancel := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer cancel()
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		<-stop.Done()
		logger.Info("shutting down")
		shutdownCtx, done := context.WithTimeout(context.Background(), 20*time.Second)
		defer done()
		if err := httpSrv.Shutdown(shutdownCtx); err != nil {
			logger.Error("shutdown", "err", err)
		}
	}()

	logger.Info("listening", "addr", httpSrv.Addr)
	if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("server stopped", "err", err)
		os.Exit(1)
	}
	// ListenAndServe returns as soon as Shutdown starts; wait for the
	// in-flight requests before the deferred pool.Close runs under them.
	<-drained
	logger.Info("stopped")
}
