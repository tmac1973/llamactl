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

// Build info, populated via -ldflags by goreleaser.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	configPath := flag.String("config", config.DefaultConfigPath(), "config file path")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Printf("llama-toolchest %s (commit %s, built %s)\n", version, commit, date)
		return
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		slog.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	if err := initDataDir(cfg); err != nil {
		slog.Warn("could not init data dir (expected in local dev)", "error", err)
	}

	srv := api.NewServer(cfg, *configPath)
	srv.SetVersion(version)

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

func initDataDir(cfg *config.Config) error {
	dirs := []string{
		filepath.Join(cfg.DataDir, "config"),
		filepath.Join(cfg.DataDir, "builds"),
		cfg.ModelsPath(),
	}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("creating %s: %w", dir, err)
		}
	}
	return nil
}
