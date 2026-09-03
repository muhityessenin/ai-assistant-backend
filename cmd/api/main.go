package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/example/ai-assistants-platform/internal/app"
	"github.com/example/ai-assistants-platform/internal/platform/config"
	"github.com/example/ai-assistants-platform/internal/platform/postgres"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("configuration failed", "error", err)
		os.Exit(1)
	}
	level := slog.LevelInfo
	if cfg.LogLevel == "debug" {
		level = slog.LevelDebug
	} else if cfg.LogLevel == "warn" {
		level = slog.LevelWarn
	} else if cfg.LogLevel == "error" {
		level = slog.LevelError
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(log)
	startup, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	db, err := postgres.Open(startup, cfg.DatabaseURL)
	if err != nil {
		log.Error("database failed", "error", err)
		os.Exit(1)
	}
	defer db.Close()
	if err = postgres.Migrate(startup, db); err != nil {
		log.Error("migration failed", "error", err)
		os.Exit(1)
	}
	application, err := app.New(cfg, db, log)
	if err != nil {
		log.Error("app initialization failed", "error", err)
		os.Exit(1)
	}
	if err = application.Start(startup); err != nil {
		log.Error("app startup failed", "error", err)
		os.Exit(1)
	}
	server := &http.Server{Addr: cfg.Host + ":" + cfg.Port, Handler: application.Handler(), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: cfg.ReadTimeout, WriteTimeout: cfg.WriteTimeout, IdleTimeout: cfg.IdleTimeout, MaxHeaderBytes: 1 << 20}
	errs := make(chan error, 1)
	go func() { log.Info("server started", "address", server.Addr); errs <- server.ListenAndServe() }()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	select {
	case s := <-sig:
		log.Info("shutdown requested", "signal", s.String())
	case err = <-errs:
		if err != nil && err != http.ErrServerClosed {
			log.Error("server failed", "error", err)
		}
	}
	shutdown, cancelShutdown := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelShutdown()
	if err = server.Shutdown(shutdown); err != nil {
		log.Error("http shutdown failed", "error", err)
	}
	application.Stop()
	log.Info("shutdown complete")
}
