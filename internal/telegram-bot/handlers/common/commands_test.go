package common

import (
	"testing"

	"github.com/go-telegram/bot/models"
)

func TestMatchCommand(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		text    string
		length  int
		command string
		want    bool
	}{
		{name: "plain", text: "/create", length: 7, command: "create", want: true},
		{name: "with bot username", text: "/create@mafia_bot", length: 17, command: "create", want: true},
		{name: "other command", text: "/create", length: 7, command: "leave", want: false},
		{name: "startgame is not start", text: "/startgame", length: 10, command: "start", want: false},
		{name: "startgame with bot", text: "/startgame@mafia_bot", length: 20, command: "startgame", want: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			update := &models.Update{Message: &models.Message{
				Text: test.text,
				Entities: []models.MessageEntity{{
					Type:   models.MessageEntityTypeBotCommand,
					Offset: 0,
					Length: test.length,
				}},
			}}
			if got := MatchCommand(test.command)(update); got != test.want {
				t.Fatalf("MatchCommand(%q)(%q) = %v, want %v", test.command, test.text, got, test.want)
			}
		})
	}
}
