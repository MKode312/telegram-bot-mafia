package app

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	gamemanagerapp "tgbot-mafia/internal/app/game-manager"
	tgbotapp "tgbot-mafia/internal/app/telegram-bot"
	"tgbot-mafia/internal/config"
	"tgbot-mafia/internal/storage/memory"
)

type App struct {
	GameManagerApp *gamemanagerapp.App
	TelegramBotApp *tgbotapp.App

	storage *memory.Storage
	log     *slog.Logger
	ctx     context.Context
	cancel  context.CancelFunc

	runOnce      sync.Once
	shutdownOnce sync.Once
	started      chan struct{}
	done         chan struct{}
}

// New creates application components that use one shared in-memory storage.
func New(log *slog.Logger, game config.GameConfig, token string) (*App, error) {
	const op = "app.New"
	if log == nil {
		log = slog.Default()
	}

	store := memory.New()

	gameManager, err := gamemanagerapp.New(log, game, store)
	if err != nil {
		return nil, fmt.Errorf("%s: initialize game manager: %w", op, err)
	}

	telegramBot, err := tgbotapp.New(log, token, gameManager.Service())
	if err != nil {
		return nil, fmt.Errorf("%s: initialize telegram bot: %w", op, err)
	}
	ctx, cancel := context.WithCancel(context.Background())

	return &App{
		GameManagerApp: gameManager,
		TelegramBotApp: telegramBot,
		storage:        store,
		log:            log,
		ctx:            ctx,
		cancel:         cancel,
		started:        make(chan struct{}),
		done:           make(chan struct{}),
	}, nil
}

// Run starts application components and blocks until they stop.
func (a *App) Run() {
	a.runOnce.Do(func() {
		close(a.started)
		a.log.Info("application started")
		a.TelegramBotApp.Run(a.ctx)
		close(a.done)
		a.log.Info("application stopped")
	})
}

// GracefulShutdown asks running components to stop and waits no longer than ctx permits.
func (a *App) GracefulShutdown(ctx context.Context) error {
	a.shutdownOnce.Do(func() {
		a.log.Info("application shutdown started")
		a.cancel()
	})

	select {
	case <-a.started:
	default:
		return nil
	}

	if err := a.TelegramBotApp.GracefulShutdown(ctx); err != nil {
		return fmt.Errorf("application shutdown: telegram bot: %w", err)
	}
	select {
	case <-a.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
