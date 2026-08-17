package gamemanager

import (
	"errors"
	"io"
	"log/slog"
	"strconv"
	"testing"
	"time"

	models "tgbot-mafia/internal/domain"
	"tgbot-mafia/internal/storage"
)

func testSettings() models.GameSettings {
	return models.GameSettings{
		MinPlayers: 2,
		MaxPlayers: 10,
		MafiaRules: []models.MafiaRule{
			{MaxPlayers: 6, Count: 1},
			{MaxPlayers: 10, Count: 2},
		},
		Doctor:             models.RoleRule{MinPlayers: 6, Count: 1},
		Detective:          models.RoleRule{MinPlayers: 3, Count: 1},
		Beauty:             models.RoleRule{MinPlayers: 4, Count: 1},
		LobbyDuration:      time.Minute,
		NightDuration:      time.Minute,
		DiscussionDuration: time.Minute,
		VotingDuration:     time.Minute,
	}
}

func TestRolesForIncludesBeauty(t *testing.T) {
	t.Parallel()

	tests := []struct {
		players int
		want    map[models.Role]int
	}{
		{players: 3, want: map[models.Role]int{models.RoleMafia: 1, models.RoleDetective: 1, models.RoleCivilian: 1}},
		{players: 4, want: map[models.Role]int{models.RoleMafia: 1, models.RoleDetective: 1, models.RoleBeauty: 1, models.RoleCivilian: 1}},
		{players: 6, want: map[models.Role]int{models.RoleMafia: 1, models.RoleDoctor: 1, models.RoleDetective: 1, models.RoleBeauty: 1, models.RoleCivilian: 2}},
	}

	for _, test := range tests {
		t.Run(strconv.Itoa(test.players), func(t *testing.T) {
			t.Parallel()
			roles, err := rolesFor(testSettings(), test.players)
			if err != nil {
				t.Fatalf("rolesFor() error = %v", err)
			}
			got := make(map[models.Role]int)
			for _, role := range roles {
				got[role]++
			}
			if len(roles) != test.players {
				t.Fatalf("got %d roles, want %d", len(roles), test.players)
			}
			for role, count := range test.want {
				if got[role] != count {
					t.Errorf("role %s = %d, want %d (got %v)", role, got[role], count, got)
				}
			}
		})
	}
}

func TestRolesForAllowsTownSpecialsWithoutCivilian(t *testing.T) {
	t.Parallel()

	settings := testSettings()
	settings.Doctor.MinPlayers = 3
	settings.Beauty.MinPlayers = 3
	settings.Detective.MinPlayers = 6

	roles, err := rolesFor(settings, 3)
	if err != nil {
		t.Fatalf("rolesFor() error = %v", err)
	}
	got := make(map[models.Role]int)
	for _, role := range roles {
		got[role]++
	}
	want := map[models.Role]int{models.RoleMafia: 1, models.RoleDoctor: 1, models.RoleBeauty: 1}
	if len(roles) != 3 {
		t.Fatalf("got %d roles, want 3", len(roles))
	}
	for role, count := range want {
		if got[role] != count {
			t.Errorf("role %s = %d, want %d (got %v)", role, got[role], count, got)
		}
	}
	if got[models.RoleCivilian] != 0 {
		t.Errorf("unexpected civilians: %d", got[models.RoleCivilian])
	}
}

func TestResolveNightSetsAlibi(t *testing.T) {
	t.Parallel()

	service := &Service{log: slog.New(slog.NewTextHandler(io.Discard, nil)), now: time.Now}
	state := &gameState{
		Game: models.Game{
			Players: []models.Player{
				{ID: 1, Role: models.RoleBeauty, Alive: true},
				{ID: 2, Role: models.RoleCivilian, Alive: true},
				{ID: 3, Role: models.RoleMafia, Alive: true},
			},
			Settings: testSettings(),
		},
		NightActions: map[int64]int64{1: 2},
	}

	service.resolveNight(state)
	if state.Game.AlibiPlayerID != 2 {
		t.Fatalf("AlibiPlayerID = %d, want 2", state.Game.AlibiPlayerID)
	}
	if state.Game.Phase != models.PhaseDiscussion {
		t.Fatalf("Phase = %v, want discussion", state.Game.Phase)
	}
}

func TestResolveNightClearsAlibiIfTargetDied(t *testing.T) {
	t.Parallel()

	service := &Service{log: slog.New(slog.NewTextHandler(io.Discard, nil)), now: time.Now}
	state := &gameState{
		Game: models.Game{
			Players: []models.Player{
				{ID: 1, Role: models.RoleBeauty, Alive: true},
				{ID: 2, Role: models.RoleCivilian, Alive: true},
				{ID: 3, Role: models.RoleMafia, Alive: true},
				{ID: 4, Role: models.RoleCivilian, Alive: true},
			},
			Settings: testSettings(),
		},
		NightActions: map[int64]int64{1: 2, 3: 2},
	}

	service.resolveNight(state)
	if state.Game.AlibiPlayerID != 0 {
		t.Fatalf("AlibiPlayerID = %d, want 0 after target died", state.Game.AlibiPlayerID)
	}
}

func TestVoteRejectsAlibiPlayer(t *testing.T) {
	t.Parallel()

	service, err := New(slog.New(slog.NewTextHandler(io.Discard, nil)), testSettings())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if _, err := service.CreateGame(1, models.Player{ID: 1, FirstName: "A"}); err != nil {
		t.Fatalf("CreateGame() error = %v", err)
	}

	_, err = service.storage.UpdateGame(1, func(state *storage.GameState) error {
		state.Game.Phase = models.PhaseVoting
		state.Game.AlibiPlayerID = 2
		state.Game.Players = []models.Player{
			{ID: 1, FirstName: "A", Role: models.RoleCivilian, Alive: true},
			{ID: 2, FirstName: "B", Role: models.RoleCivilian, Alive: true},
			{ID: 3, FirstName: "C", Role: models.RoleMafia, Alive: true},
		}
		state.Votes = map[int64]int64{}
		return nil
	})
	if err != nil {
		t.Fatalf("UpdateGame() error = %v", err)
	}

	if _, err := service.Vote(1, 1, 2); !errors.Is(err, ErrAlibiProtected) {
		t.Fatalf("Vote() error = %v, want ErrAlibiProtected", err)
	}
	if _, err := service.Vote(1, 1, 3); err != nil {
		t.Fatalf("Vote() for non-alibi target error = %v", err)
	}
}

func TestNightActionTargetsBeautyIncludesSelf(t *testing.T) {
	t.Parallel()

	service, err := New(slog.New(slog.NewTextHandler(io.Discard, nil)), testSettings())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if _, err := service.CreateGame(1, models.Player{ID: 1}); err != nil {
		t.Fatalf("CreateGame() error = %v", err)
	}
	_, err = service.storage.UpdateGame(1, func(state *storage.GameState) error {
		state.Game.Phase = models.PhaseNight
		state.Game.Players = []models.Player{
			{ID: 1, Role: models.RoleBeauty, Alive: true},
			{ID: 2, Role: models.RoleCivilian, Alive: true},
			{ID: 3, Role: models.RoleMafia, Alive: true},
		}
		return nil
	})
	if err != nil {
		t.Fatalf("UpdateGame() error = %v", err)
	}

	targets, err := service.NightActionTargets(1, 1, "")
	if err != nil {
		t.Fatalf("NightActionTargets() error = %v", err)
	}
	if len(targets) != 3 {
		t.Fatalf("got %d targets, want 3", len(targets))
	}
}
