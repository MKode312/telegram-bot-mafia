package help

import (
	"context"
	"log/slog"

	"tgbot-mafia/internal/lib/logger/sl"
	"tgbot-mafia/internal/telegram-bot/handlers/common"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

const welcomeText = "👋 Привет! Я бот для игры в мафию от MKode312.\n\nДобавьте меня в групповой чат, создайте игру командой /create и зовите друзей.\n\n💬 Важно: перед стартом каждый игрок должен написать мне в личные сообщения (/start), иначе я не смогу прислать роль.\n\n📋 Список команд — /help."

const helpText = "🏠 Лобби:\n\n/create — создать игру\n/leave — выйти\n/game — состояние\n/startgame — начать (создатель)\n/cancel — отменить (создатель, до старта)\n\nВход в лобби — кнопкой «Присоединиться».\n\nПеред стартом каждый игрок должен написать боту в ЛС (/start).\n\n🎮 Во время игры:\n\nголосование начинается само после обсуждения и завершается по таймеру.\nГолос и ночные действия — кнопками."

// Register attaches /start, /help and /jopa handlers to b.
func Register(b *bot.Bot, log *slog.Logger) {
	b.RegisterHandlerMatchFunc(common.MatchCommand("start"), common.CleanupGroupCommand(log, start(log)))
	b.RegisterHandlerMatchFunc(common.MatchCommand("help"), common.CleanupGroupCommand(log, reply(log, "help", helpText)))
	b.RegisterHandlerMatchFunc(common.MatchCommand("jopa"), common.CleanupGroupCommand(log, jopa(log)))
}

func start(log *slog.Logger) bot.HandlerFunc {
	return func(ctx context.Context, b *bot.Bot, update *models.Update) {
		if update.Message == nil || update.Message.From == nil {
			return
		}
		chat := update.Message.Chat
		userID := update.Message.From.ID
		log.Info("command requested", "command", "start", "chat_id", chat.ID, "user_id", userID, "chat_type", chat.Type)

		if chat.Type == models.ChatTypePrivate {
			common.Send(ctx, b, log, chat.ID, welcomeText)
			return
		}

		if err := common.TrySend(ctx, b, userID, welcomeText); err != nil {
			log.Info("private start blocked, group /start ignored", "user_id", userID, "from_chat_id", chat.ID, sl.Err(err))
			return
		}
		log.Info("welcome sent in private chat", "user_id", userID, "from_chat_id", chat.ID)
	}
}

func reply(log *slog.Logger, command, text string) bot.HandlerFunc {
	return func(ctx context.Context, b *bot.Bot, update *models.Update) {
		if update.Message == nil || update.Message.From == nil {
			return
		}
		chat := update.Message.Chat
		log.Info("command requested", "command", command, "chat_id", chat.ID, "user_id", update.Message.From.ID)
		common.Reply(ctx, b, log, update, text)
	}
}
