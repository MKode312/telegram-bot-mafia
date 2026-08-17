package game

import (
	"strings"
	"testing"

	models "tgbot-mafia/internal/domain"
)

func TestPlayerLabel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		player models.Player
		want   string
	}{
		{name: "first name", player: models.Player{ID: 1, FirstName: "Николай", Username: "nikolay"}, want: "Николай"},
		{name: "username fallback", player: models.Player{ID: 2, Username: "darya"}, want: "darya"},
		{name: "id fallback", player: models.Player{ID: 3}, want: "Игрок 3"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := playerLabel(test.player); got != test.want {
				t.Fatalf("playerLabel() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestDisplayName(t *testing.T) {
	t.Parallel()

	got := displayName(models.Player{ID: 42, FirstName: "Дарья", Username: "darya"})
	want := `<a href="tg://user?id=42">Дарья</a>`
	if got != want {
		t.Fatalf("displayName() = %q, want %q", got, want)
	}

	got = displayName(models.Player{ID: 7, FirstName: `A<B>&"C`})
	want = `<a href="tg://user?id=7">A&lt;B&gt;&amp;&#34;C</a>`
	if got != want {
		t.Fatalf("displayName() escaped = %q, want %q", got, want)
	}
}

func TestLobbyTextUsesFirstNameLinks(t *testing.T) {
	t.Parallel()

	text := lobbyText(models.Game{
		CreatorID: 1,
		Players: []models.Player{
			{ID: 1, FirstName: "Николай", Username: "nikolay"},
			{ID: 2, FirstName: "Дарья", Username: "darya"},
		},
		Settings: models.GameSettings{MaxPlayers: 10},
	})
	if !strings.Contains(text, `<a href="tg://user?id=1">Николай</a>`) {
		t.Fatalf("lobbyText missing Николай link\n%s", text)
	}
	if !strings.Contains(text, `<a href="tg://user?id=2">Дарья</a>`) {
		t.Fatalf("lobbyText missing Дарья link\n%s", text)
	}
	if strings.Contains(text, "@nikolay") || strings.Contains(text, "@darya") {
		t.Fatalf("lobbyText still shows usernames:\n%s", text)
	}
}

func TestVoteKeyboardSkipsAlibiPlayer(t *testing.T) {
	t.Parallel()

	players := []models.Player{
		{ID: 1, FirstName: "Николай", Alive: true},
		{ID: 2, FirstName: "Дарья", Alive: true},
		{ID: 3, FirstName: "Кирилл", Alive: true},
	}
	keyboard := voteKeyboard(10, 1, 2, players)
	if len(keyboard.InlineKeyboard) != 1 {
		t.Fatalf("got %d buttons, want 1", len(keyboard.InlineKeyboard))
	}
	if got := keyboard.InlineKeyboard[0][0].Text; got != "Кирилл" {
		t.Fatalf("button text = %q, want Кирилл", got)
	}
}

func TestAlibiText(t *testing.T) {
	t.Parallel()

	game := models.Game{
		AlibiPlayerID: 2,
		Players: []models.Player{
			{ID: 1, FirstName: "Николай", Alive: true},
			{ID: 2, FirstName: "Дарья", Alive: true},
		},
	}
	got := alibiText(game)
	want := `💋 У <a href="tg://user?id=2">Дарья</a> алиби. Сегодня за этого игрока нельзя голосовать.`
	if got != want {
		t.Fatalf("alibiText() = %q, want %q", got, want)
	}
	if alibiText(models.Game{}) != "" {
		t.Fatal("alibiText() for empty game should be empty")
	}
}
