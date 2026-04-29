package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/tmlabonte/llamactl/internal/api"
	"github.com/tmlabonte/llamactl/internal/config"
)

func main() {
	configPath := flag.String("config", "/data/config/llama-toolchest.yaml", "config file path")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		slog.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	if err := initDataDir(cfg.DataDir); err != nil {
		slog.Warn("could not init data dir (expected in local dev)", "error", err)
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

func initDataDir(dataDir string) error {
	dirs := []string{
		filepath.Join(dataDir, "config"),
		filepath.Join(dataDir, "builds"),
		filepath.Join(dataDir, "models"),
	}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("creating %s: %w", dir, err)
		}
	}
	return nil
}
