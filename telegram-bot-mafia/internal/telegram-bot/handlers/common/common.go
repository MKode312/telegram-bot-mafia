package common

import (
	"context"
	"fmt"
	"log/slog"

	"tgbot-mafia/internal/lib/logger/sl"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// Reply sends text as a reply in the same chat as update and hides a leftover reply keyboard.
func Reply(ctx context.Context, b *bot.Bot, log *slog.Logger, update *models.Update, text string) {
	if update.Message == nil {
		return
	}
	params := &bot.SendMessageParams{
		ChatID:          update.Message.Chat.ID,
		MessageThreadID: update.Message.MessageThreadID,
		Text:            text,
		ParseMode:       models.ParseModeHTML,
		ReplyMarkup:     &models.ReplyKeyboardRemove{RemoveKeyboard: true},
	}
	if _, err := b.SendMessage(ctx, params); err != nil {
		log.Error("failed to send Telegram message", "chat_id", update.Message.Chat.ID, sl.Err(err))
	}
}

// Send delivers text to chatID and logs the error if delivery fails.
func Send(ctx context.Context, b *bot.Bot, log *slog.Logger, chatID int64, text string) {
	if err := TrySend(ctx, b, chatID, text); err != nil {
		log.Error("failed to send Telegram message", "chat_id", chatID, sl.Err(err))
	}
}

// TrySend sends a text message and returns the API error, if any.
// Also removes a leftover reply keyboard (including one left by a deleted bot).
func TrySend(ctx context.Context, b *bot.Bot, chatID int64, text string) error {
	_, err := b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:      chatID,
		Text:        text,
		ParseMode:   models.ParseModeHTML,
		ReplyMarkup: &models.ReplyKeyboardRemove{RemoveKeyboard: true},
	})
	return err
}

// SendWithKeyboard sends text with an inline keyboard. It returns the message or nil on error.
func SendWithKeyboard(ctx context.Context, b *bot.Bot, log *slog.Logger, chatID int64, text string, keyboard *models.InlineKeyboardMarkup) *models.Message {
	message, err := b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:      chatID,
		Text:        text,
		ParseMode:   models.ParseModeHTML,
		ReplyMarkup: keyboard,
	})
	if err != nil {
		log.Error("failed to send Telegram message with keyboard", "chat_id", chatID, sl.Err(err))
		return nil
	}
	return message
}

// ClearInlineKeyboard removes inline buttons from a message. Falls back to delete on failure.
func ClearInlineKeyboard(ctx context.Context, b *bot.Bot, log *slog.Logger, chatID int64, messageID int) {
	if messageID == 0 {
		return
	}
	_, err := b.EditMessageReplyMarkup(ctx, &bot.EditMessageReplyMarkupParams{
		ChatID:    chatID,
		MessageID: messageID,
		ReplyMarkup: &models.InlineKeyboardMarkup{
			InlineKeyboard: [][]models.InlineKeyboardButton{},
		},
	})
	if err == nil {
		return
	}
	log.Error("failed to clear inline keyboard, deleting message", "chat_id", chatID, "message_id", messageID, sl.Err(err))
	DeleteMessage(ctx, b, log, chatID, messageID)
}

// CanMessageUser reports whether the bot can deliver private messages to the user.
// Telegram allows this only after the user has opened a private chat (usually /start).
func CanMessageUser(ctx context.Context, b *bot.Bot, userID int64) bool {
	_, err := b.SendChatAction(ctx, &bot.SendChatActionParams{
		ChatID: userID,
		Action: models.ChatActionTyping,
	})
	return err == nil
}

// BotPrivateChatLink returns https://t.me/<bot> to open an existing private chat
// without sending /start.
func BotPrivateChatLink(ctx context.Context, b *bot.Bot) (string, error) {
	me, err := b.GetMe(ctx)
	if err != nil {
		return "", err
	}
	if me.Username == "" {
		return "", fmt.Errorf("bot has no username")
	}
	return "https://t.me/" + me.Username, nil
}

// BotDeepLink returns https://t.me/<bot>?start=1 so Telegram opens a private chat
// and sends /start. Use this only when the player may not have started the bot yet.
func BotDeepLink(ctx context.Context, b *bot.Bot) (string, error) {
	link, err := BotPrivateChatLink(ctx, b)
	if err != nil {
		return "", err
	}
	return link + "?start=1", nil
}

// ChatAndUser extracts chat ID, user ID and a display name from a message update.
func ChatAndUser(update *models.Update) (chatID, userID int64, username string, ok bool) {
	if update.Message == nil || update.Message.From == nil {
		return 0, 0, "", false
	}
	user := update.Message.From
	username = user.Username
	if username == "" {
		username = user.FirstName
	}
	return update.Message.Chat.ID, user.ID, username, true
}

// DeleteMessage removes a chat message. Fails silently in logs if the bot lacks rights.
func DeleteMessage(ctx context.Context, b *bot.Bot, log *slog.Logger, chatID int64, messageID int) {
	if messageID == 0 {
		return
	}
	if _, err := b.DeleteMessage(ctx, &bot.DeleteMessageParams{ChatID: chatID, MessageID: messageID}); err != nil {
		log.Error("failed to delete Telegram message", "chat_id", chatID, "message_id", messageID, sl.Err(err))
	}
}

func groupSlashCommand(update *models.Update) (chatID int64, threadID int, ok bool) {
	if update.Message == nil {
		return 0, 0, false
	}
	chat := update.Message.Chat
	if chat.Type != models.ChatTypeGroup && chat.Type != models.ChatTypeSupergroup {
		return 0, 0, false
	}
	text := update.Message.Text
	if text == "" || text[0] != '/' {
		return 0, 0, false
	}
	return chat.ID, update.Message.MessageThreadID, true
}

// RemoveReplyKeyboard overwrites another bot's reply keyboard with ours, then hides it.
// Clients often ignore ReplyKeyboardRemove for a keyboard they still associate with a
// deleted bot; replacing it first is required. The hide-message is kept in history.
func RemoveReplyKeyboard(ctx context.Context, b *bot.Bot, log *slog.Logger, chatID int64, threadID int) {
	overwrite, err := b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:              chatID,
		MessageThreadID:     threadID,
		Text:                "⌨️",
		DisableNotification: true,
		ReplyMarkup: &models.ReplyKeyboardMarkup{
			Keyboard:       [][]models.KeyboardButton{{{Text: "…"}}},
			ResizeKeyboard: true,
		},
	})
	if err != nil {
		log.Error("failed to overwrite reply keyboard", "chat_id", chatID, sl.Err(err))
	} else {
		DeleteMessage(ctx, b, log, chatID, overwrite.ID)
	}

	_, err = b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:              chatID,
		MessageThreadID:     threadID,
		Text:                "📋 Команды: /help",
		DisableNotification: true,
		ReplyMarkup:         &models.ReplyKeyboardRemove{RemoveKeyboard: true},
	})
	if err != nil {
		log.Error("failed to remove reply keyboard", "chat_id", chatID, sl.Err(err))
	}
}

// CleanupGroupCommand runs next, then deletes the triggering command message in groups.
func CleanupGroupCommand(log *slog.Logger, next bot.HandlerFunc) bot.HandlerFunc {
	return func(ctx context.Context, b *bot.Bot, update *models.Update) {
		next(ctx, b, update)
		chatID, _, ok := groupSlashCommand(update)
		if !ok {
			return
		}
		DeleteMessage(ctx, b, log, chatID, update.Message.ID)
	}
}
