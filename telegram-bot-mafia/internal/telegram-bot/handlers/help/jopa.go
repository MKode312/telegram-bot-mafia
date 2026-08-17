package help

import (
	"bytes"
	"context"
	"log/slog"
	"sync"
	"time"

	"tgbot-mafia/assets"
	"tgbot-mafia/internal/lib/logger/sl"
	"tgbot-mafia/internal/telegram-bot/handlers/common"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

const jopaTTL = 30 * time.Second

var (
	jopaFileMu sync.Mutex
	jopaFileID string
)

func jopa(log *slog.Logger) bot.HandlerFunc {
	return func(ctx context.Context, b *bot.Bot, update *models.Update) {
		if update.Message == nil || update.Message.From == nil {
			return
		}
		chat := update.Message.Chat
		log.Info("command requested", "command", "jopa", "chat_id", chat.ID, "user_id", update.Message.From.ID)

		message, err := b.SendPhoto(ctx, &bot.SendPhotoParams{
			ChatID:          chat.ID,
			MessageThreadID: update.Message.MessageThreadID,
			Photo:           jopaPhoto(),
		})
		if err != nil {
			log.Error("failed to send jopa photo", "chat_id", chat.ID, sl.Err(err))
			return
		}
		rememberJopaFileID(message)
		scheduleJopaDelete(log, b, chat.ID, message.ID)
	}
}

func jopaPhoto() models.InputFile {
	jopaFileMu.Lock()
	defer jopaFileMu.Unlock()
	if jopaFileID != "" {
		return &models.InputFileString{Data: jopaFileID}
	}
	return &models.InputFileUpload{Filename: "jopa.jpg", Data: bytes.NewReader(assets.JopaJPG)}
}

func rememberJopaFileID(message *models.Message) {
	if message == nil || len(message.Photo) == 0 {
		return
	}
	id := message.Photo[len(message.Photo)-1].FileID
	if id == "" {
		return
	}
	jopaFileMu.Lock()
	jopaFileID = id
	jopaFileMu.Unlock()
}

func scheduleJopaDelete(log *slog.Logger, b *bot.Bot, chatID int64, messageID int) {
	time.AfterFunc(jopaTTL, func() {
		common.DeleteMessage(context.Background(), b, log, chatID, messageID)
	})
}
