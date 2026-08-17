package tgbotapp

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/go-telegram/bot"
	"tgbot-mafia/internal/lib/logger/sl"
	gamemanager "tgbot-mafia/internal/services/game-manager"
	"tgbot-mafia/internal/telegram-bot/handlers"
)

type App struct {
	log   *slog.Logger
	bot   *bot.Bot
	token string

	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

// New creates a Telegram bot that uses the first game manager in managers.
func New(log *slog.Logger, token string, managers ...*gamemanager.Service) (*App, error) {
	const op = "tgbotapp.New"
	if log == nil {
		log = slog.Default()
	}
	if len(managers) == 0 || managers[0] == nil {
		return nil, fmt.Errorf("%s: game manager is required", op)
	}
	b, err := bot.New(token,
		bot.WithDefaultHandler(handlers.Default(log)),
		bot.WithErrorsHandler(func(err error) { log.Error("Telegram bot error", sl.Err(err)) }),
	)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", op, err)
	}
	handlers.Register(b, log, managers[0])

	return &App{
		log:   log,
		bot:   b,
		token: token,
	}, nil
}

// Run begins receiving Telegram updates and blocks until ctx is cancelled.
func (a *App) Run(ctx context.Context) {
	runCtx, cancel := context.WithCancel(ctx)
	a.mu.Lock()
	a.cancel = cancel
	a.done = make(chan struct{})
	done := a.done
	a.mu.Unlock()

	a.log.Info("telegram bot started")
	if err := handlers.SetBotCommands(runCtx, a.bot); err != nil {
		a.log.Error("failed to set bot commands", sl.Err(err))
	}
	a.bot.Start(runCtx)
	a.log.Info("telegram bot stopped")
	close(done)
}

// Start is kept as a compatibility alias for Run.
func (a *App) Start(ctx context.Context) { a.Run(ctx) }

// GracefulShutdown stops receiving updates and waits until Run returns.
func (a *App) GracefulShutdown(ctx context.Context) error {
	a.mu.Lock()
	cancel, done := a.cancel, a.done
	a.mu.Unlock()
	if cancel == nil || done == nil {
		return nil
	}

	cancel()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
