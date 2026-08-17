package storage

import (
	"errors"

	"tgbot-mafia/internal/domain"
)

var (
	ErrGameNotFound      = errors.New("storage: game not found")
	ErrGameAlreadyExists = errors.New("storage: game already exists")

	ErrPlayerNotFound      = errors.New("storage: player not found")
	ErrPlayerAlreadyExists = errors.New("storage: player already exists")
)

// GameState contains all mutable data needed to resume a game. It is kept in
// storage rather than in a transport/service, so a game manager has one source
// of truth for games, player roles, night actions and votes.
type GameState struct {
	Game             models.Game
	NightActions     map[int64]int64
	NightActionTypes map[int64]string // detective: "check" or "shoot"
	Votes            map[int64]int64
	LastDoctorTarget int64          // healed last night; cannot heal again tonight
	DoctorSelfHealed bool           // doctor may self-heal only once per game
	DetectiveChecked map[int64]bool // targets already investigated this game
	FlavorRole         models.Role // only this role gets joke phrases this game
	FlavorPhrases      []string    // remaining shuffled phrases (mafia/doctor/detective shoot)
	FlavorCheckPhrases []string    // remaining shuffled detective check phrases
}

// GameRepository offers atomic access to game state. Implementations must not
// expose their internal maps or slices to callers.
type GameRepository interface {
	CreateGame(GameState) error
	GameState(chatID int64) (GameState, error)
	UpdateGame(chatID int64, update func(*GameState) error) (GameState, error)
	DeleteGame(chatID int64) error
}
