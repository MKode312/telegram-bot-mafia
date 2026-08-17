// Package gamemanagerapp wires game-manager services into the application.
package gamemanagerapp

import (
	"fmt"
	"log/slog"

	"tgbot-mafia/internal/config"
	"tgbot-mafia/internal/domain"
	gamemanager "tgbot-mafia/internal/services/game-manager"
	"tgbot-mafia/internal/storage"
)

type App struct {
	log     *slog.Logger
	service *gamemanager.Service
}

// New creates a game-manager app from YAML game settings and an optional repository.
func New(log *slog.Logger, cfg config.GameConfig, repositories ...storage.GameRepository) (*App, error) {
	const op = "gamemanagerapp.New"
	service, err := gamemanager.New(log, settingsFromConfig(cfg), repositories...)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", op, err)
	}
	return &App{log: log, service: service}, nil
}

// Service exposes the game API to transports such as the Telegram bot.
func (a *App) Service() *gamemanager.Service { return a.service }

func settingsFromConfig(cfg config.GameConfig) models.GameSettings {
	settings := models.GameSettings{
		MinPlayers:         cfg.MinPlayers,
		MaxPlayers:         cfg.MaxPlayers,
		LobbyDuration:      cfg.Timers.Lobby,
		NightDuration:      cfg.Timers.Night,
		DiscussionDuration: cfg.Timers.Discussion,
		VotingDuration:     cfg.Timers.Voting,
		Doctor:             models.RoleRule{MinPlayers: cfg.Roles.Doctor.MinPlayers, Count: cfg.Roles.Doctor.Count},
		Detective:          models.RoleRule{MinPlayers: cfg.Roles.Detective.MinPlayers, Count: cfg.Roles.Detective.Count},
		Beauty:             models.RoleRule{MinPlayers: cfg.Roles.Beauty.MinPlayers, Count: cfg.Roles.Beauty.Count},
	}
	for _, rule := range cfg.Mafia {
		settings.MafiaRules = append(settings.MafiaRules, models.MafiaRule{MaxPlayers: rule.MaxPlayers, Count: rule.Count})
	}
	return settings
}
