package handlers

import (
	"context"
	"log/slog"

	gamemanager "tgbot-mafia/internal/services/game-manager"
	"tgbot-mafia/internal/telegram-bot/handlers/common"
	gamehandler "tgbot-mafia/internal/telegram-bot/handlers/game"
	"tgbot-mafia/internal/telegram-bot/handlers/help"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// Register attaches membership, help and game handlers to b.
func Register(b *bot.Bot, log *slog.Logger, manager *gamemanager.Service) {
	b.RegisterHandlerMatchFunc(common.BotJoinedGroup, common.OnBotJoinedGroup(log))
	help.Register(b, log)
	gamehandler.New(log, manager).Register(b)
}

// SetBotCommands publishes the bot command menu for private chats and groups.
func SetBotCommands(ctx context.Context, b *bot.Bot) error {
	commands := common.BotCommands()
	scopes := []models.BotCommandScope{
		&models.BotCommandScopeDefault{},
		&models.BotCommandScopeAllPrivateChats{},
		&models.BotCommandScopeAllGroupChats{},
	}
	for _, lang := range []string{"ru", "en"} {
		for _, scope := range scopes {
			if _, err := b.DeleteMyCommands(ctx, &bot.DeleteMyCommandsParams{Scope: scope, LanguageCode: lang}); err != nil {
				return err
			}
		}
	}
	for _, scope := range scopes {
		if _, err := b.SetMyCommands(ctx, &bot.SetMyCommandsParams{Commands: commands, Scope: scope}); err != nil {
			return err
		}
	}
	return nil
}

// Default handles unmatched private messages and unknown slash commands.
func Default(log *slog.Logger) bot.HandlerFunc {
	return common.CleanupGroupCommand(log, func(ctx context.Context, b *bot.Bot, update *models.Update) {
		if update.Message == nil || update.Message.Text == "" {
			return
		}
		chat := update.Message.Chat
		text := update.Message.Text

		// Groups: ignore plain text; only clean up unknown slash-commands.
		if chat.Type != models.ChatTypePrivate {
			return
		}

		if text[0] == '/' {
			log.Debug("unsupported Telegram command", "chat_id", chat.ID, "message_id", update.Message.ID, "text", text)
			common.Reply(ctx, b, log, update, "❓ Неизвестная команда. Используйте /help.")
			return
		}
		log.Debug("private plain text received", "chat_id", chat.ID, "user_id", update.Message.From.ID)
		common.Reply(ctx, b, log, update, "ℹ️ Используйте /help, чтобы посмотреть список команд.")
	})
}
