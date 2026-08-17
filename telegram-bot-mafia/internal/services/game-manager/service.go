// Package gamemanager contains the rules and in-memory state of Mafia games.
package gamemanager

import (
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"sort"
	"sync"
	"time"

	"tgbot-mafia/internal/domain"
	"tgbot-mafia/internal/lib/logger/sl"
	"tgbot-mafia/internal/storage"
	"tgbot-mafia/internal/storage/memory"
)

var (
	ErrGameNotFound        = errors.New("game manager: game not found")
	ErrGameAlreadyExists   = errors.New("game manager: game already exists")
	ErrPlayerAlreadyExists = errors.New("game manager: player already exists")
	ErrPlayerNotFound      = errors.New("game manager: player not found")
	ErrGameIsNotInLobby    = errors.New("game manager: game is not in lobby")
	ErrNotEnoughPlayers    = errors.New("game manager: not enough players")
	ErrGameFinished        = errors.New("game manager: game is finished")
	ErrInvalidPhase        = errors.New("game manager: action is unavailable in this phase")
	ErrPlayerIsDead        = errors.New("game manager: player is dead")
	ErrInvalidTarget       = errors.New("game manager: invalid target")
	ErrUnauthorized        = errors.New("game manager: player cannot perform this action")
	ErrVoteAlreadyCast     = errors.New("game manager: vote already cast")
	ErrHealRepeatTarget    = errors.New("game manager: doctor cannot heal the same player two nights in a row")
	ErrHealSelfAlreadyUsed = errors.New("game manager: doctor already used self-heal this game")
	ErrAlreadyInvestigated = errors.New("game manager: detective already checked this player")
	ErrAlibiProtected      = errors.New("game manager: player has an alibi")
)

const (
	NightActionCheck = "check"
	NightActionShoot = "shoot"
)

type Service struct {
	log      *slog.Logger
	settings models.GameSettings
	now      func() time.Time

	mu      sync.Mutex
	storage storage.GameRepository
}

type gameState = storage.GameState

// New creates a game manager. If repositories is empty, an in-memory store is used.
func New(log *slog.Logger, settings models.GameSettings, repositories ...storage.GameRepository) (*Service, error) {
	if log == nil {
		log = slog.Default()
	}
	if settings.MinPlayers < 1 || settings.MaxPlayers < settings.MinPlayers {
		return nil, fmt.Errorf("game manager: invalid player limits")
	}
	if settings.LobbyDuration < 0 || settings.NightDuration < 0 || settings.DiscussionDuration < 0 || settings.VotingDuration < 0 {
		return nil, fmt.Errorf("game manager: durations must not be negative")
	}
	if len(settings.MafiaRules) == 0 {
		return nil, fmt.Errorf("game manager: at least one mafia rule is required")
	}
	for _, rule := range settings.MafiaRules {
		if rule.MaxPlayers < settings.MinPlayers || rule.Count < 1 {
			return nil, fmt.Errorf("game manager: invalid mafia rule")
		}
	}
	if settings.Doctor.Count < 0 || settings.Detective.Count < 0 || settings.Beauty.Count < 0 {
		return nil, fmt.Errorf("game manager: role count must not be negative")
	}
	repository := storage.GameRepository(memory.New())
	if len(repositories) > 0 && repositories[0] != nil {
		repository = repositories[0]
	}
	log.Info("game manager initialized",
		"min_players", settings.MinPlayers,
		"max_players", settings.MaxPlayers,
		"lobby", settings.LobbyDuration,
		"night", settings.NightDuration,
		"discussion", settings.DiscussionDuration,
		"voting", settings.VotingDuration,
	)
	return &Service{log: log, settings: cloneSettings(settings), now: time.Now, storage: repository}, nil
}

// CreateGame opens a lobby in chatID with creator as the first player.
func (s *Service) CreateGame(chatID int64, creator models.Player) (models.Game, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if creator.ID == 0 {
		return models.Game{}, fmt.Errorf("game manager: creator ID must not be zero")
	}
	if existing, err := s.storage.GameState(chatID); err == nil {
		if existing.Game.Phase != models.PhaseFinished {
			return models.Game{}, ErrGameAlreadyExists
		}
		if err := s.storage.DeleteGame(chatID); err != nil {
			return models.Game{}, mapStorageError(err)
		}
		s.log.Info("finished game removed before create", "chat_id", chatID)
	} else if !errors.Is(mapStorageError(err), ErrGameNotFound) {
		return models.Game{}, mapStorageError(err)
	}
	creator.Role, creator.Alive = "", true
	settings := cloneSettings(s.settings)
	state := gameState{Game: models.Game{ChatID: chatID, CreatorID: creator.ID, Players: []models.Player{creator}, Phase: models.PhaseLobby, Settings: settings, EndsAt: s.now().Add(settings.LobbyDuration)}}
	if err := s.storage.CreateGame(state); err != nil {
		return models.Game{}, mapStorageError(err)
	}
	s.log.Info("game created", "chat_id", chatID, "creator_id", creator.ID, "lobby_duration", settings.LobbyDuration)
	return cloneGame(state.Game), nil
}

// ClearFinishedGame deletes a finished game so a new lobby can be created.
func (s *Service) ClearFinishedGame(chatID int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.state(chatID)
	if err != nil {
		if errors.Is(err, ErrGameNotFound) {
			return nil
		}
		return err
	}
	if state.Game.Phase != models.PhaseFinished {
		return ErrInvalidPhase
	}
	if err := s.storage.DeleteGame(chatID); err != nil {
		return mapStorageError(err)
	}
	s.log.Info("finished game cleared", "chat_id", chatID)
	return nil
}

// Game returns a copy of the current game in chatID.
func (s *Service) Game(chatID int64) (models.Game, error) {
	state, err := s.storage.GameState(chatID)
	if err != nil {
		return models.Game{}, mapStorageError(err)
	}
	return cloneGame(state.Game), nil
}

// JoinGame adds player to the lobby in chatID.
func (s *Service) JoinGame(chatID int64, player models.Player) (models.Game, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.lobby(chatID)
	if err != nil {
		return models.Game{}, err
	}
	if len(state.Game.Players) >= state.Game.Settings.MaxPlayers {
		return models.Game{}, fmt.Errorf("game manager: player limit reached")
	}
	if _, ok := playerIndex(state.Game.Players, player.ID); ok {
		return models.Game{}, ErrPlayerAlreadyExists
	}
	player.Role, player.Alive = "", true
	state.Game.Players = append(state.Game.Players, player)
	game, err := s.persist(state)
	if err == nil {
		s.log.Info("player joined game", "chat_id", chatID, "player_id", player.ID, "players", len(game.Players))
	}
	return game, err
}

// LeaveGame removes playerID from the lobby. If the creator leaves, rights pass to the next player.
func (s *Service) LeaveGame(chatID, playerID int64) (models.Game, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.lobby(chatID)
	if err != nil {
		return models.Game{}, err
	}
	i, ok := playerIndex(state.Game.Players, playerID)
	if !ok {
		return models.Game{}, ErrPlayerNotFound
	}
	wasCreator := playerID == state.Game.CreatorID
	state.Game.Players = append(state.Game.Players[:i], state.Game.Players[i+1:]...)
	if len(state.Game.Players) == 0 {
		if err := s.storage.DeleteGame(chatID); err != nil {
			return models.Game{}, mapStorageError(err)
		}
		s.log.Info("game deleted after last player left", "chat_id", chatID, "player_id", playerID)
		return cloneGame(state.Game), nil
	}
	if wasCreator {
		// After removal, the next person in the former list sits at index i
		// (or wraps to 0 if the creator was last).
		next := i
		if next >= len(state.Game.Players) {
			next = 0
		}
		state.Game.CreatorID = state.Game.Players[next].ID
		s.log.Info("creator rights transferred", "chat_id", chatID, "from", playerID, "to", state.Game.CreatorID)
	}
	game, err := s.persist(state)
	if err == nil {
		s.log.Info("player left game", "chat_id", chatID, "player_id", playerID, "players", len(game.Players), "creator_id", game.CreatorID)
	}
	return game, err
}

// CancelGame deletes a lobby game. Only the creator may cancel before start.
func (s *Service) CancelGame(chatID, actorID int64) (models.Game, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.lobby(chatID)
	if err != nil {
		return models.Game{}, err
	}
	if actorID != state.Game.CreatorID {
		return models.Game{}, ErrUnauthorized
	}
	game := cloneGame(state.Game)
	if err := s.storage.DeleteGame(chatID); err != nil {
		return models.Game{}, mapStorageError(err)
	}
	s.log.Info("game cancelled", "chat_id", chatID, "actor_id", actorID)
	return game, nil
}

// StartGame assigns roles and begins the first night. Only the creator may start.
func (s *Service) StartGame(chatID, actorID int64) (models.Game, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.lobby(chatID)
	if err != nil {
		return models.Game{}, err
	}
	if actorID != state.Game.CreatorID {
		return models.Game{}, ErrUnauthorized
	}
	if len(state.Game.Players) < state.Game.Settings.MinPlayers {
		return models.Game{}, ErrNotEnoughPlayers
	}
	if err := s.beginGame(state); err != nil {
		return models.Game{}, err
	}
	game, err := s.persist(state)
	if err == nil {
		s.log.Info("game started", "chat_id", chatID, "players", len(game.Players))
	}
	return game, err
}

// SubmitNightAction records an action by a living mafia, doctor, detective, or beauty.
// Detective actionType must be NightActionCheck or NightActionShoot.
// The night resolves automatically after every active special role has acted.
func (s *Service) SubmitNightAction(chatID, actorID, targetID int64, actionType string) (models.Game, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.state(chatID)
	if err != nil {
		return models.Game{}, err
	}
	if state.Game.Phase != models.PhaseNight {
		return models.Game{}, ErrInvalidPhase
	}
	actor, ok := findPlayer(state.Game.Players, actorID)
	if !ok {
		return models.Game{}, ErrPlayerNotFound
	}
	target, targetOK := findPlayer(state.Game.Players, targetID)
	if !actor.Alive {
		return models.Game{}, ErrPlayerIsDead
	}
	if !targetOK || !target.Alive {
		return models.Game{}, ErrInvalidTarget
	}
	if !canActAtNight(actor.Role) {
		return models.Game{}, ErrUnauthorized
	}
	if actorID == targetID && actor.Role != models.RoleDoctor && actor.Role != models.RoleBeauty {
		return models.Game{}, ErrInvalidTarget
	}
	if actor.Role == models.RoleMafia && target.Role == models.RoleMafia {
		return models.Game{}, ErrInvalidTarget
	}
	if actor.Role == models.RoleDoctor && targetID == state.LastDoctorTarget {
		return models.Game{}, ErrHealRepeatTarget
	}
	if actor.Role == models.RoleDoctor && actorID == targetID && state.DoctorSelfHealed {
		return models.Game{}, ErrHealSelfAlreadyUsed
	}
	if actor.Role == models.RoleDetective {
		if actionType != NightActionCheck && actionType != NightActionShoot {
			return models.Game{}, ErrInvalidTarget
		}
		if actionType == NightActionCheck && state.DetectiveChecked[targetID] {
			return models.Game{}, ErrAlreadyInvestigated
		}
	}
	if state.NightActions == nil {
		state.NightActions = make(map[int64]int64)
	}
	if state.NightActionTypes == nil {
		state.NightActionTypes = make(map[int64]string)
	}
	state.NightActions[actorID] = targetID
	if actor.Role == models.RoleDetective {
		state.NightActionTypes[actorID] = actionType
		if actionType == NightActionCheck {
			if state.DetectiveChecked == nil {
				state.DetectiveChecked = make(map[int64]bool)
			}
			state.DetectiveChecked[targetID] = true
		}
	}
	s.log.Debug("night action submitted", "chat_id", chatID, "actor_id", actorID, "action_type", actionType)
	if s.allNightActionsSubmitted(state) {
		s.resolveNight(state)
	}
	return s.persist(state)
}

// NightActionTargets returns living players the actor may target tonight.
// For detectives pass NightActionCheck or NightActionShoot.
func (s *Service) NightActionTargets(chatID, actorID int64, actionType string) ([]models.Player, error) {
	state, err := s.state(chatID)
	if err != nil {
		return nil, err
	}
	actor, ok := findPlayer(state.Game.Players, actorID)
	if !ok {
		return nil, ErrPlayerNotFound
	}
	targets := make([]models.Player, 0, len(state.Game.Players))
	for _, player := range state.Game.Players {
		if !player.Alive {
			continue
		}
		switch actor.Role {
		case models.RoleDoctor:
			if player.ID == state.LastDoctorTarget {
				continue
			}
			if player.ID == actorID && state.DoctorSelfHealed {
				continue
			}
		case models.RoleDetective:
			if player.ID == actorID {
				continue
			}
			if actionType == NightActionCheck && state.DetectiveChecked[player.ID] {
				continue
			}
		case models.RoleMafia:
			if player.ID == actorID || player.Role == models.RoleMafia {
				continue
			}
		case models.RoleBeauty:
			// Any living player, including the beauty herself.
		default:
			continue
		}
		targets = append(targets, player)
	}
	return clonePlayers(targets), nil
}

// Vote records voterID's day vote for targetID. The vote resolves when every living player has voted.
func (s *Service) Vote(chatID, voterID, targetID int64) (models.Game, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.state(chatID)
	if err != nil {
		return models.Game{}, err
	}
	if state.Game.Phase != models.PhaseVoting {
		return models.Game{}, ErrInvalidPhase
	}
	voter, ok := findPlayer(state.Game.Players, voterID)
	if !ok {
		return models.Game{}, ErrPlayerNotFound
	}
	target, targetOK := findPlayer(state.Game.Players, targetID)
	if !voter.Alive {
		return models.Game{}, ErrPlayerIsDead
	}
	if !targetOK || !target.Alive || voterID == targetID {
		return models.Game{}, ErrInvalidTarget
	}
	if targetID == state.Game.AlibiPlayerID {
		return models.Game{}, ErrAlibiProtected
	}
	if _, voted := state.Votes[voterID]; voted {
		return models.Game{}, ErrVoteAlreadyCast
	}
	state.Votes[voterID] = targetID
	s.log.Debug("vote submitted", "chat_id", chatID, "voter_id", voterID)
	if len(state.Votes) == aliveCount(state.Game.Players) {
		s.resolveVote(state)
	}
	return s.persist(state)
}

// LobbyExpireAction describes what ExpireLobby did after the join deadline.
type LobbyExpireAction int

const (
	LobbyExpireNone LobbyExpireAction = iota
	LobbyExpireCancelled
	LobbyExpireStarted
)

// ExpireLobby handles the lobby join deadline: starts the game when there are
// enough players, otherwise cancels the lobby.
func (s *Service) ExpireLobby(chatID int64) (models.Game, LobbyExpireAction, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	state, err := s.lobby(chatID)
	if err != nil {
		if errors.Is(err, ErrGameNotFound) || errors.Is(err, ErrGameIsNotInLobby) {
			return models.Game{}, LobbyExpireNone, nil
		}
		return models.Game{}, LobbyExpireNone, err
	}
	if !state.Game.EndsAt.IsZero() && s.now().Before(state.Game.EndsAt) {
		return cloneGame(state.Game), LobbyExpireNone, nil
	}
	if len(state.Game.Players) >= state.Game.Settings.MinPlayers {
		if err := s.beginGame(state); err != nil {
			return models.Game{}, LobbyExpireNone, err
		}
		game, err := s.persist(state)
		if err != nil {
			return models.Game{}, LobbyExpireNone, err
		}
		s.log.Info("lobby expired, game started", "chat_id", chatID, "players", len(game.Players))
		return game, LobbyExpireStarted, nil
	}
	game := cloneGame(state.Game)
	if err := s.storage.DeleteGame(chatID); err != nil {
		return models.Game{}, LobbyExpireNone, mapStorageError(err)
	}
	s.log.Info("lobby expired", "chat_id", chatID, "players", len(game.Players))
	return game, LobbyExpireCancelled, nil
}

func (s *Service) beginGame(state *gameState) error {
	roles, err := rolesFor(state.Game.Settings, len(state.Game.Players))
	if err != nil {
		return err
	}
	if err = shuffle(roles); err != nil {
		return err
	}
	for i := range state.Game.Players {
		state.Game.Players[i].Role, state.Game.Players[i].Alive = roles[i], true
	}
	state.LastDoctorTarget = 0
	state.DoctorSelfHealed = false
	state.DetectiveChecked = make(map[int64]bool)
	state.Game.LastLynchedID = 0
	if err := s.assignNightFlavor(state); err != nil {
		return err
	}
	if err := s.assignMafiaFirstVoter(state, false); err != nil {
		return err
	}
	state.Game.StartedAt = s.now()
	s.startNight(state)
	return nil
}

// ExpireNight resolves the night after the night deadline, even if some roles
// have not acted. Returns true when the night was resolved by this call.
func (s *Service) ExpireNight(chatID int64) (models.Game, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	state, err := s.state(chatID)
	if err != nil {
		if errors.Is(err, ErrGameNotFound) {
			return models.Game{}, false, nil
		}
		return models.Game{}, false, err
	}
	if state.Game.Phase != models.PhaseNight {
		return models.Game{}, false, nil
	}
	if !state.Game.EndsAt.IsZero() && s.now().Before(state.Game.EndsAt) {
		return cloneGame(state.Game), false, nil
	}
	s.resolveNight(state)
	game, err := s.persist(state)
	if err != nil {
		return models.Game{}, false, err
	}
	s.log.Info("night expired", "chat_id", chatID, "phase", game.Phase)
	return game, true, nil
}

// ExpireDiscussion starts voting after the discussion deadline. Returns true when
// the phase actually advanced.
func (s *Service) ExpireDiscussion(chatID int64) (models.Game, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	state, err := s.state(chatID)
	if err != nil {
		if errors.Is(err, ErrGameNotFound) {
			return models.Game{}, false, nil
		}
		return models.Game{}, false, err
	}
	if state.Game.Phase != models.PhaseDiscussion {
		return models.Game{}, false, nil
	}
	if !state.Game.EndsAt.IsZero() && s.now().Before(state.Game.EndsAt) {
		return cloneGame(state.Game), false, nil
	}
	s.startVoting(state)
	game, err := s.persist(state)
	if err != nil {
		return models.Game{}, false, err
	}
	s.log.Info("discussion expired, voting started", "chat_id", chatID)
	return game, true, nil
}

// ExpireVoting resolves the vote after the voting deadline. Returns true when
// the vote was resolved by this call.
func (s *Service) ExpireVoting(chatID int64) (models.Game, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	state, err := s.state(chatID)
	if err != nil {
		if errors.Is(err, ErrGameNotFound) {
			return models.Game{}, false, nil
		}
		return models.Game{}, false, err
	}
	if state.Game.Phase != models.PhaseVoting {
		return models.Game{}, false, nil
	}
	if !state.Game.EndsAt.IsZero() && s.now().Before(state.Game.EndsAt) {
		return cloneGame(state.Game), false, nil
	}
	s.resolveVote(state)
	game, err := s.persist(state)
	if err != nil {
		return models.Game{}, false, err
	}
	s.log.Info("voting expired", "chat_id", chatID, "phase", game.Phase)
	return game, true, nil
}

// PlayerRole returns the role of playerID in the game for chatID.
func (s *Service) PlayerRole(chatID, playerID int64) (models.Role, error) {
	state, err := s.state(chatID)
	if err != nil {
		return "", err
	}
	p, ok := findPlayer(state.Game.Players, playerID)
	if !ok {
		return "", ErrPlayerNotFound
	}
	return p.Role, nil
}

// LivingMafiaAllies returns other living mafia players (excluding playerID).
func (s *Service) LivingMafiaAllies(chatID, playerID int64) ([]models.Player, error) {
	state, err := s.state(chatID)
	if err != nil {
		return nil, err
	}
	allies := make([]models.Player, 0)
	for _, player := range state.Game.Players {
		if player.Alive && player.Role == models.RoleMafia && player.ID != playerID {
			allies = append(allies, player)
		}
	}
	return clonePlayers(allies), nil
}

// TakeNightFlavor returns the next joke phrase for a night announce, if this
// game's flavor role matches and phrases remain. Detective check and shoot use
// separate phrase decks.
func (s *Service) TakeNightFlavor(chatID int64, role models.Role, actionType string) string {
	s.mu.Lock()
	defer s.mu.Unlock()

	state, err := s.state(chatID)
	if err != nil {
		return ""
	}
	if role != state.FlavorRole {
		return ""
	}

	var phrase string
	switch {
	case role == models.RoleDetective && actionType == NightActionCheck:
		if len(state.FlavorCheckPhrases) == 0 {
			return ""
		}
		phrase = state.FlavorCheckPhrases[0]
		state.FlavorCheckPhrases = append([]string(nil), state.FlavorCheckPhrases[1:]...)
	case role == models.RoleDetective && actionType != NightActionShoot:
		return ""
	default:
		if len(state.FlavorPhrases) == 0 {
			return ""
		}
		phrase = state.FlavorPhrases[0]
		state.FlavorPhrases = append([]string(nil), state.FlavorPhrases[1:]...)
	}

	if _, err := s.persist(state); err != nil {
		s.log.Error("failed to persist night flavor phrase", "chat_id", chatID, sl.Err(err))
		return ""
	}
	return phrase
}

func (s *Service) lobby(chatID int64) (*gameState, error) {
	state, err := s.state(chatID)
	if err != nil {
		return nil, err
	}
	if state.Game.Phase != models.PhaseLobby {
		return nil, ErrGameIsNotInLobby
	}
	return state, nil
}
func (s *Service) state(chatID int64) (*gameState, error) {
	state, err := s.storage.GameState(chatID)
	if err != nil {
		return nil, mapStorageError(err)
	}
	return &state, nil
}

func (s *Service) persist(state *gameState) (models.Game, error) {
	updated, err := s.storage.UpdateGame(state.Game.ChatID, func(current *storage.GameState) error {
		*current = *state
		return nil
	})
	if err != nil {
		s.log.Error("failed to persist game state", "chat_id", state.Game.ChatID, sl.Err(err))
		return models.Game{}, mapStorageError(err)
	}
	return cloneGame(updated.Game), nil
}

func mapStorageError(err error) error {
	switch {
	case errors.Is(err, storage.ErrGameNotFound):
		return ErrGameNotFound
	case errors.Is(err, storage.ErrGameAlreadyExists):
		return ErrGameAlreadyExists
	case errors.Is(err, storage.ErrPlayerNotFound):
		return ErrPlayerNotFound
	case errors.Is(err, storage.ErrPlayerAlreadyExists):
		return ErrPlayerAlreadyExists
	default:
		return err
	}
}
func (s *Service) startNight(state *gameState) {
	state.Game.Phase = models.PhaseNight
	state.Game.EndsAt = s.now().Add(state.Game.Settings.NightDuration)
	state.Game.LastKilledIDs = nil
	state.Game.AlibiPlayerID = 0
	state.NightActions = make(map[int64]int64)
	state.NightActionTypes = make(map[int64]string)
	state.Votes = nil
	s.log.Info("night started", "chat_id", state.Game.ChatID, "ends_at", state.Game.EndsAt)
}
func (s *Service) startDiscussion(state *gameState) {
	state.Game.Phase = models.PhaseDiscussion
	state.Game.EndsAt = s.now().Add(state.Game.Settings.DiscussionDuration)
	state.NightActions = nil
	state.NightActionTypes = nil
	s.log.Info("discussion started", "chat_id", state.Game.ChatID, "ends_at", state.Game.EndsAt)
}
func (s *Service) startVoting(state *gameState) {
	state.Game.Phase = models.PhaseVoting
	state.Game.EndsAt = s.now().Add(state.Game.Settings.VotingDuration)
	state.Votes = make(map[int64]int64)
	state.Game.LastLynchedID = 0
	s.log.Info("voting phase started", "chat_id", state.Game.ChatID, "ends_at", state.Game.EndsAt)
}

func (s *Service) allNightActionsSubmitted(state *gameState) bool {
	for _, p := range state.Game.Players {
		if p.Alive && canActAtNight(p.Role) {
			if _, ok := state.NightActions[p.ID]; !ok {
				return false
			}
		}
	}
	return true
}
func (s *Service) resolveNight(state *gameState) {
	mafiaTargets := make(map[int64]int)
	var protected int64
	var hasProtect bool
	var detectiveShot int64
	var alibiTarget int64
	for _, p := range state.Game.Players {
		target, acted := state.NightActions[p.ID]
		if !acted {
			continue
		}
		switch p.Role {
		case models.RoleMafia:
			mafiaTargets[target]++
		case models.RoleDoctor:
			protected = target
			hasProtect = true
			state.LastDoctorTarget = target
			if p.ID == target {
				state.DoctorSelfHealed = true
			}
		case models.RoleDetective:
			if state.NightActionTypes[p.ID] == NightActionShoot {
				detectiveShot = target
			}
		case models.RoleBeauty:
			alibiTarget = target
		}
	}
	killed := make([]int64, 0, 2)
	if victim, ok := pickMafiaVictim(mafiaTargets); ok && (!hasProtect || victim != protected) {
		kill(&state.Game, victim)
		killed = append(killed, victim)
	}
	if detectiveShot != 0 && (!hasProtect || detectiveShot != protected) {
		if shot, ok := findPlayer(state.Game.Players, detectiveShot); ok && shot.Alive {
			kill(&state.Game, detectiveShot)
			killed = append(killed, detectiveShot)
		}
	}
	state.Game.LastKilledIDs = killed
	state.Game.AlibiPlayerID = 0
	if alibiTarget != 0 {
		if player, ok := findPlayer(state.Game.Players, alibiTarget); ok && player.Alive {
			state.Game.AlibiPlayerID = alibiTarget
		}
	}
	if !s.finishIfWon(state) {
		s.startDiscussion(state)
	}
}
func (s *Service) resolveVote(state *gameState) {
	counts := make(map[int64]int)
	for _, target := range state.Votes {
		counts[target]++
	}
	state.Game.LastLynchedID = 0
	target, count, tied := uniqueHighestTarget(counts)
	if !tied && count > aliveCount(state.Game.Players)/2 {
		kill(&state.Game, target)
		state.Game.LastLynchedID = target
	}
	if !s.finishIfWon(state) {
		if err := s.assignMafiaFirstVoter(state, true); err != nil {
			s.log.Error("failed to rotate mafia first voter", "chat_id", state.Game.ChatID, sl.Err(err))
		}
		s.startNight(state)
	}
}
func (s *Service) finishIfWon(state *gameState) bool {
	mafia, civilians := 0, 0
	for _, p := range state.Game.Players {
		if p.Alive {
			if p.Role == models.RoleMafia {
				mafia++
			} else {
				civilians++
			}
		}
	}
	if mafia == 0 || mafia >= civilians {
		winner := models.TeamCivilians
		if mafia >= civilians {
			winner = models.TeamMafia
		}
		result := models.GameResults{
			Players:  clonePlayers(state.Game.Players),
			Winner:   winner,
			Duration: gameDuration(state.Game.StartedAt, s.now()),
		}
		state.Game.Results = &result
		state.Game.Phase, state.Game.EndsAt = models.PhaseFinished, time.Time{}
		s.log.Info("game finished", "chat_id", state.Game.ChatID, "winner", winner)
		return true
	}
	return false
}

func gameDuration(startedAt, finishedAt time.Time) time.Duration {
	if startedAt.IsZero() {
		return 0
	}
	d := finishedAt.Sub(startedAt)
	if d < 0 {
		return 0
	}
	return d
}

func rolesFor(settings models.GameSettings, players int) ([]models.Role, error) {
	mafia, limit := 0, 0
	for _, rule := range settings.MafiaRules {
		if players <= rule.MaxPlayers && (limit == 0 || rule.MaxPlayers < limit) {
			mafia, limit = rule.Count, rule.MaxPlayers
		}
	}
	if mafia == 0 {
		return nil, fmt.Errorf("game manager: no mafia rule for %d players: %w", players, ErrNotEnoughPlayers)
	}
	// Need at least one civilian; otherwise the role setup is invalid for this size.
	if mafia >= players {
		return nil, ErrNotEnoughPlayers
	}
	roles := make([]models.Role, 0, players)
	for range mafia {
		roles = append(roles, models.RoleMafia)
	}
	for range settings.Doctor.Count {
		if players >= settings.Doctor.MinPlayers && len(roles) < players {
			roles = append(roles, models.RoleDoctor)
		}
	}
	for range settings.Detective.Count {
		if players >= settings.Detective.MinPlayers && len(roles) < players {
			roles = append(roles, models.RoleDetective)
		}
	}
	for range settings.Beauty.Count {
		if players >= settings.Beauty.MinPlayers && len(roles) < players {
			roles = append(roles, models.RoleBeauty)
		}
	}
	for len(roles) < players {
		roles = append(roles, models.RoleCivilian)
	}
	return roles, nil
}
func shuffle(roles []models.Role) error {
	for i := len(roles) - 1; i > 0; i-- {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(i+1)))
		if err != nil {
			return fmt.Errorf("game manager: shuffle roles: %w", err)
		}
		roles[i], roles[n.Int64()] = roles[n.Int64()], roles[i]
	}
	return nil
}
func findPlayer(players []models.Player, id int64) (models.Player, bool) {
	i, ok := playerIndex(players, id)
	if !ok {
		return models.Player{}, false
	}
	return players[i], true
}
func playerIndex(players []models.Player, id int64) (int, bool) {
	for i, p := range players {
		if p.ID == id {
			return i, true
		}
	}
	return 0, false
}
func kill(game *models.Game, id int64) {
	if i, ok := playerIndex(game.Players, id); ok {
		game.Players[i].Alive = false
	}
}
func canActAtNight(role models.Role) bool {
	switch role {
	case models.RoleMafia, models.RoleDoctor, models.RoleDetective, models.RoleBeauty:
		return true
	default:
		return false
	}
}
func aliveCount(players []models.Player) int {
	total := 0
	for _, p := range players {
		if p.Alive {
			total++
		}
	}
	return total
}
func highestTarget(targets map[int64]int) (int64, int) {
	winner, count, _ := uniqueHighestTarget(targets)
	return winner, count
}

// pickMafiaVictim chooses the night kill target. Matching votes keep that target;
// if mafias split across different victims, one of those victims is chosen at random.
func pickMafiaVictim(targets map[int64]int) (int64, bool) {
	ids := make([]int64, 0, len(targets))
	for id, votes := range targets {
		if votes > 0 {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return 0, false
	}
	if len(ids) == 1 {
		return ids[0], true
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	n, err := rand.Int(rand.Reader, big.NewInt(int64(len(ids))))
	if err != nil {
		return ids[0], true
	}
	return ids[n.Int64()], true
}

// uniqueHighestTarget returns the top target. tied is true when two or more
// targets share the highest vote count (or there were no votes).
func uniqueHighestTarget(targets map[int64]int) (winner int64, count int, tied bool) {
	if len(targets) == 0 {
		return 0, 0, true
	}
	var ids []int64
	for id := range targets {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		votes := targets[id]
		if votes > count {
			winner, count, tied = id, votes, false
			continue
		}
		if votes == count {
			tied = true
		}
	}
	return winner, count, tied
}

func livingMafiaIDs(state *gameState) []int64 {
	ids := make([]int64, 0)
	for _, player := range state.Game.Players {
		if player.Alive && player.Role == models.RoleMafia {
			ids = append(ids, player.ID)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

// assignMafiaFirstVoter picks who votes first among living mafia.
// On the first night (rotate=false) the choice is random; later nights it rotates.
func (s *Service) assignMafiaFirstVoter(state *gameState, rotate bool) error {
	alive := livingMafiaIDs(state)
	if len(alive) == 0 {
		state.Game.MafiaFirstVoterID = 0
		return nil
	}
	if len(alive) == 1 {
		state.Game.MafiaFirstVoterID = alive[0]
		return nil
	}
	if !rotate {
		id, err := pickRandomID(alive)
		if err != nil {
			return err
		}
		state.Game.MafiaFirstVoterID = id
		s.log.Info("mafia first voter assigned", "chat_id", state.Game.ChatID, "player_id", id)
		return nil
	}
	idx := -1
	for i, id := range alive {
		if id == state.Game.MafiaFirstVoterID {
			idx = i
			break
		}
	}
	if idx < 0 {
		id, err := pickRandomID(alive)
		if err != nil {
			return err
		}
		state.Game.MafiaFirstVoterID = id
	} else {
		state.Game.MafiaFirstVoterID = alive[(idx+1)%len(alive)]
	}
	s.log.Info("mafia first voter rotated", "chat_id", state.Game.ChatID, "player_id", state.Game.MafiaFirstVoterID)
	return nil
}

func pickRandomID(ids []int64) (int64, error) {
	if len(ids) == 0 {
		return 0, fmt.Errorf("game manager: empty id list")
	}
	n, err := rand.Int(rand.Reader, big.NewInt(int64(len(ids))))
	if err != nil {
		return 0, fmt.Errorf("game manager: pick random id: %w", err)
	}
	return ids[n.Int64()], nil
}

var nightFlavorPhrases = map[models.Role][]string{
	models.RoleDetective: {
		"(Марк сейчас игру закончит)",
		"(Марк стреляет, держитесь...)",
		"(Кто-то не поиграет...)",
	},
	models.RoleDoctor: {
		"(Первое правило не забыл(а)?)",
		"(У Олеси курс покупал(а)?)",
		"(Блин, опять Коля мешает выиграть...)",
	},
	models.RoleMafia: {
		"(Марк, ну всё равно ведь проиграешь...)",
		"(Мб Лизу некст?)",
		"(Шанс точно не подкручен, уверяю вас)",
		"(Не волнуйся, никакого щита здесь нет)",
	},
	models.RoleBeauty: {
		"(Опять кого-то прикроет...)",
		"(Алиби сегодня не купишь — только красотка даст)",
		"(Марк, тебя точно прикроют)",
	},
}

var detectiveCheckFlavorPhrases = []string{
	"(Срочно Марка проверить, он точно мафия!)",
	"(Опять Настя всех до последнего проверять будет...)",
	"(А фальшивых документов-то здесь нет)",
}

func (s *Service) assignNightFlavor(state *gameState) error {
	state.FlavorRole = ""
	state.FlavorPhrases = nil
	state.FlavorCheckPhrases = nil

	present := make(map[models.Role]bool)
	for _, player := range state.Game.Players {
		if _, ok := nightFlavorPhrases[player.Role]; ok {
			present[player.Role] = true
		}
	}
	candidates := make([]models.Role, 0, len(present))
	for role := range present {
		candidates = append(candidates, role)
	}
	if len(candidates) == 0 {
		return nil
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i] < candidates[j] })
	n, err := rand.Int(rand.Reader, big.NewInt(int64(len(candidates))))
	if err != nil {
		return fmt.Errorf("game manager: pick flavor role: %w", err)
	}
	role := candidates[n.Int64()]
	phrases := append([]string(nil), nightFlavorPhrases[role]...)
	if err := shuffleStrings(phrases); err != nil {
		return err
	}
	state.FlavorRole = role
	state.FlavorPhrases = phrases
	if role == models.RoleDetective {
		checkPhrases := append([]string(nil), detectiveCheckFlavorPhrases...)
		if err := shuffleStrings(checkPhrases); err != nil {
			return err
		}
		state.FlavorCheckPhrases = checkPhrases
	}
	s.log.Info("night flavor role assigned", "chat_id", state.Game.ChatID, "role", role)
	return nil
}

func shuffleStrings(values []string) error {
	for i := len(values) - 1; i > 0; i-- {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(i+1)))
		if err != nil {
			return fmt.Errorf("game manager: shuffle phrases: %w", err)
		}
		values[i], values[n.Int64()] = values[n.Int64()], values[i]
	}
	return nil
}
func clonePlayers(players []models.Player) []models.Player {
	return append([]models.Player(nil), players...)
}
func cloneSettings(s models.GameSettings) models.GameSettings {
	s.MafiaRules = append([]models.MafiaRule(nil), s.MafiaRules...)
	return s
}
func cloneGame(game models.Game) models.Game {
	game.Players = clonePlayers(game.Players)
	game.Settings = cloneSettings(game.Settings)
	game.LastKilledIDs = append([]int64(nil), game.LastKilledIDs...)
	if game.Results != nil {
		result := *game.Results
		result.Players = clonePlayers(result.Players)
		game.Results = &result
	}
	return game
}
