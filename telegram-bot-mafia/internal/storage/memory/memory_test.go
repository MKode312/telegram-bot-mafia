package memory

import (
	"sync"
	"testing"

	"tgbot-mafia/internal/domain"
	"tgbot-mafia/internal/storage"
)

func TestStorageUpdateGameIsAtomic(t *testing.T) {
	t.Parallel()

	store := New()
	if err := store.CreateGame(storage.GameState{Game: models.Game{ChatID: 1}}); err != nil {
		t.Fatalf("CreateGame() error = %v", err)
	}

	const updates = 100
	var group sync.WaitGroup
	group.Add(updates)
	for range updates {
		go func() {
			defer group.Done()
			_, err := store.UpdateGame(1, func(state *storage.GameState) error {
				state.Game.CreatorID++
				return nil
			})
			if err != nil {
				t.Errorf("UpdateGame() error = %v", err)
			}
		}()
	}
	group.Wait()

	state, err := store.GameState(1)
	if err != nil {
		t.Fatalf("GameState() error = %v", err)
	}
	if state.Game.CreatorID != updates {
		t.Errorf("CreatorID = %d, want %d", state.Game.CreatorID, updates)
	}
}

func TestStorageDoesNotExposeMutableState(t *testing.T) {
	store := New()
	input := storage.GameState{Game: models.Game{ChatID: 1, Players: []models.Player{{ID: 1}}}}
	if err := store.CreateGame(input); err != nil {
		t.Fatalf("CreateGame() error = %v", err)
	}
	input.Game.Players[0].Username = "changed outside"

	state, err := store.GameState(1)
	if err != nil {
		t.Fatalf("GameState() error = %v", err)
	}
	state.Game.Players[0].Username = "changed copy"
	stored, err := store.GameState(1)
	if err != nil {
		t.Fatalf("GameState() error = %v", err)
	}
	if stored.Game.Players[0].Username != "" {
		t.Errorf("storage contains %q, want empty username", stored.Game.Players[0].Username)
	}
}
