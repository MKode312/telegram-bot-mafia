package common

import (
	"context"
	"log/slog"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// BotJoinedGroup is true when the bot itself was added to a group or supergroup.
func BotJoinedGroup(update *models.Update) bool {
	event := update.MyChatMember
	if event == nil {
		return false
	}
	chat := event.Chat
	if chat.Type != models.ChatTypeGroup && chat.Type != models.ChatTypeSupergroup {
		return false
	}
	return !memberInChat(event.OldChatMember) && memberInChat(event.NewChatMember)
}

func memberInChat(member models.ChatMember) bool {
	switch member.Type {
	case models.ChatMemberTypeOwner, models.ChatMemberTypeAdministrator, models.ChatMemberTypeMember:
		return true
	case models.ChatMemberTypeRestricted:
		return member.Restricted != nil && member.Restricted.IsMember
	default:
		return false
	}
}

// OnBotJoinedGroup clears a leftover reply keyboard from a previous bot in that group.
func OnBotJoinedGroup(log *slog.Logger) bot.HandlerFunc {
	return func(ctx context.Context, b *bot.Bot, update *models.Update) {
		chat := update.MyChatMember.Chat
		log.Info("bot added to group, removing leftover reply keyboard", "chat_id", chat.ID, "title", chat.Title)
		RemoveReplyKeyboard(ctx, b, log, chat.ID, 0)
	}
}
