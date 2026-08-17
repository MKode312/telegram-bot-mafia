package common

import (
	"testing"

	"github.com/go-telegram/bot/models"
)

func TestBotJoinedGroup(t *testing.T) {
	t.Parallel()

	group := models.Chat{ID: -100, Type: models.ChatTypeSupergroup, Title: "mafia"}
	tests := []struct {
		name string
		upd  *models.Update
		want bool
	}{
		{
			name: "added as member",
			upd: &models.Update{MyChatMember: &models.ChatMemberUpdated{
				Chat:          group,
				OldChatMember: models.ChatMember{Type: models.ChatMemberTypeLeft},
				NewChatMember: models.ChatMember{Type: models.ChatMemberTypeMember},
			}},
			want: true,
		},
		{
			name: "added as admin",
			upd: &models.Update{MyChatMember: &models.ChatMemberUpdated{
				Chat:          group,
				OldChatMember: models.ChatMember{Type: models.ChatMemberTypeLeft},
				NewChatMember: models.ChatMember{Type: models.ChatMemberTypeAdministrator},
			}},
			want: true,
		},
		{
			name: "already in chat",
			upd: &models.Update{MyChatMember: &models.ChatMemberUpdated{
				Chat:          group,
				OldChatMember: models.ChatMember{Type: models.ChatMemberTypeMember},
				NewChatMember: models.ChatMember{Type: models.ChatMemberTypeAdministrator},
			}},
			want: false,
		},
		{
			name: "kicked",
			upd: &models.Update{MyChatMember: &models.ChatMemberUpdated{
				Chat:          group,
				OldChatMember: models.ChatMember{Type: models.ChatMemberTypeMember},
				NewChatMember: models.ChatMember{Type: models.ChatMemberTypeBanned},
			}},
			want: false,
		},
		{
			name: "private chat",
			upd: &models.Update{MyChatMember: &models.ChatMemberUpdated{
				Chat:          models.Chat{ID: 1, Type: models.ChatTypePrivate},
				OldChatMember: models.ChatMember{Type: models.ChatMemberTypeLeft},
				NewChatMember: models.ChatMember{Type: models.ChatMemberTypeMember},
			}},
			want: false,
		},
		{name: "nil update", upd: &models.Update{}, want: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := BotJoinedGroup(test.upd); got != test.want {
				t.Fatalf("BotJoinedGroup() = %v, want %v", got, test.want)
			}
		})
	}
}
