package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"tgbot-mafia/internal/app"
	"tgbot-mafia/internal/config"
	"tgbot-mafia/internal/lib/logger/handlers/slogpretty"
	"tgbot-mafia/internal/lib/logger/sl"
)

const (
	envLocal = "local"
	envProd  = "prod"

	shutdownTimeout = 10 * time.Second
)

func main() {
	cfg := config.MustLoad()

	log := setupLogger(cfg.Env)
	log.Info("application configuration loaded", "environment", cfg.Env)

	application, err := app.New(log, cfg.Game, cfg.Token)
	if err != nil {
		log.Error("failed to initialize application", sl.Err(err))
		os.Exit(1)
	}

	runDone := make(chan struct{})
	go func() {
		application.Run()
		close(runDone)
	}()

	stopContext, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	select {
	case <-stopContext.Done():
		log.Info("shutdown signal received")
	case <-runDone:
		log.Warn("application stopped before a shutdown signal")
	}

	shutdownContext, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := application.GracefulShutdown(shutdownContext); err != nil && !errors.Is(err, context.Canceled) {
		log.Error("graceful shutdown failed", sl.Err(err))
		os.Exit(1)
	}
}

func setupLogger(env string) *slog.Logger {
	var log *slog.Logger
	switch env {
	case envLocal:
		log = slog.New(slogpretty.PrettyHandlerOptions{
			SlogOpts: &slog.HandlerOptions{Level: slog.LevelDebug},
		}.NewPrettyHandler(os.Stdout))
	case envProd:
		log = slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	default:
		log = slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))
	}
	slog.SetDefault(log)
	return log
}
