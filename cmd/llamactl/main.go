package main

import (
	"context"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/tmlabonte/llamactl/internal/api"
	"github.com/tmlabonte/llamactl/internal/config"
)

func main() {
	configPath := flag.String("config", "/data/config/llamactl.yaml", "config file path")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		slog.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	srv := api.NewServer(cfg)

	httpSrv := &http.Server{
		Addr:    cfg.ListenAddr,
		Handler: srv.Router(),
	}

	// Graceful shutdown
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		slog.Info("listening", "addr", cfg.ListenAddr)
		if err := httpSrv.ListenAndServe(); err != http.ErrServerClosed {
			slog.Error("server error", "error", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	slog.Info("shutting down")
	httpSrv.Shutdown(context.Background())
}
