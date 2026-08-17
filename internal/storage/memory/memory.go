// Package memory provides a concurrent in-memory implementation of game storage.
package memory

import (
	"sync"

	"tgbot-mafia/internal/domain"
	"tgbot-mafia/internal/storage"
)

type Storage struct {
	mu    sync.RWMutex
	games map[int64]storage.GameState
}

// New returns an empty in-memory game store.
func New() *Storage {
	return &Storage{games: make(map[int64]storage.GameState)}
}

// CreateGame stores a new game. It returns storage.ErrGameAlreadyExists if chatID is taken.
func (s *Storage) CreateGame(state storage.GameState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.games[state.Game.ChatID]; exists {
		return storage.ErrGameAlreadyExists
	}
	s.games[state.Game.ChatID] = cloneState(state)
	return nil
}

// GameState returns a copy of the game for chatID.
func (s *Storage) GameState(chatID int64) (storage.GameState, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	state, exists := s.games[chatID]
	if !exists {
		return storage.GameState{}, storage.ErrGameNotFound
	}
	return cloneState(state), nil
}

// UpdateGame executes update while the state is exclusively locked. The update
// is atomic with respect to every other Storage operation.
func (s *Storage) UpdateGame(chatID int64, update func(*storage.GameState) error) (storage.GameState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	state, exists := s.games[chatID]
	if !exists {
		return storage.GameState{}, storage.ErrGameNotFound
	}
	state = cloneState(state)
	if err := update(&state); err != nil {
		return storage.GameState{}, err
	}
	state = cloneState(state)
	s.games[chatID] = state
	return cloneState(state), nil
}

// DeleteGame removes the game for chatID.
func (s *Storage) DeleteGame(chatID int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.games[chatID]; !exists {
		return storage.ErrGameNotFound
	}
	delete(s.games, chatID)
	return nil
}

// AddPlayer appends a player to the lobby for chatID.
func (s *Storage) AddPlayer(chatID int64, player models.Player) error {
	_, err := s.UpdateGame(chatID, func(state *storage.GameState) error {
		for _, existing := range state.Game.Players {
			if existing.ID == player.ID {
				return storage.ErrPlayerAlreadyExists
			}
		}
		state.Game.Players = append(state.Game.Players, player)
		return nil
	})
	return err
}

// Player returns the player with playerID in the game for chatID.
func (s *Storage) Player(chatID, playerID int64) (models.Player, error) {
	state, err := s.GameState(chatID)
	if err != nil {
		return models.Player{}, err
	}
	for _, player := range state.Game.Players {
		if player.ID == playerID {
			return player, nil
		}
	}
	return models.Player{}, storage.ErrPlayerNotFound
}

// UpdatePlayer replaces the stored player that matches player.ID.
func (s *Storage) UpdatePlayer(chatID int64, player models.Player) error {
	_, err := s.UpdateGame(chatID, func(state *storage.GameState) error {
		for i, existing := range state.Game.Players {
			if existing.ID == player.ID {
				state.Game.Players[i] = player
				return nil
			}
		}
		return storage.ErrPlayerNotFound
	})
	return err
}

// DeletePlayer removes playerID from the game for chatID.
func (s *Storage) DeletePlayer(chatID, playerID int64) error {
	_, err := s.UpdateGame(chatID, func(state *storage.GameState) error {
		for i, player := range state.Game.Players {
			if player.ID == playerID {
				state.Game.Players = append(state.Game.Players[:i], state.Game.Players[i+1:]...)
				return nil
			}
		}
		return storage.ErrPlayerNotFound
	})
	return err
}

func cloneState(state storage.GameState) storage.GameState {
	state.Game.Players = append([]models.Player(nil), state.Game.Players...)
	state.Game.Settings.MafiaRules = append([]models.MafiaRule(nil), state.Game.Settings.MafiaRules...)
	state.Game.LastKilledIDs = append([]int64(nil), state.Game.LastKilledIDs...)
	if state.Game.Results != nil {
		results := *state.Game.Results
		results.Players = append([]models.Player(nil), results.Players...)
		state.Game.Results = &results
	}
	state.NightActions = cloneMoves(state.NightActions)
	state.NightActionTypes = cloneStringMap(state.NightActionTypes)
	state.Votes = cloneMoves(state.Votes)
	state.DetectiveChecked = cloneBoolMap(state.DetectiveChecked)
	state.FlavorPhrases = append([]string(nil), state.FlavorPhrases...)
	state.FlavorCheckPhrases = append([]string(nil), state.FlavorCheckPhrases...)
	return state
}

func cloneMoves(moves map[int64]int64) map[int64]int64 {
	if moves == nil {
		return nil
	}
	copy := make(map[int64]int64, len(moves))
	for key, value := range moves {
		copy[key] = value
	}
	return copy
}

func cloneBoolMap(values map[int64]bool) map[int64]bool {
	if values == nil {
		return nil
	}
	copy := make(map[int64]bool, len(values))
	for key, value := range values {
		copy[key] = value
	}
	return copy
}

func cloneStringMap(values map[int64]string) map[int64]string {
	if values == nil {
		return nil
	}
	copy := make(map[int64]string, len(values))
	for key, value := range values {
		copy[key] = value
	}
	return copy
}
