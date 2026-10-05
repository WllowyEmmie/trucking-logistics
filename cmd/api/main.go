package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"

	"github.com/WllowyEmmie/trucking-logistics/internal/api"
	"github.com/WllowyEmmie/trucking-logistics/internal/config"
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

	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)

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

	addr := ":" + cfg.Port

	logger.Info("listening", "addr", addr)
	if err := http.ListenAndServe(addr, srv.Routes()); err != nil {
		logger.Error("server stopped", "err", err)
		os.Exit(1)
	}
}
