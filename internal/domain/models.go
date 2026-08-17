package models

import "time"

// Game is the public state of one game. Slices are copied by the game manager
// before being returned, so callers cannot alter the manager's state.
type Game struct {
	ChatID       int64
	CreatorID    int64
	Players      []Player
	Phase        Phase
	Settings     GameSettings
	EndsAt       time.Time
	StartedAt    time.Time
	Results       *GameResults
	LastKilledIDs     []int64 // players who died during the last night; empty means nobody died
	LastLynchedID     int64   // player hanged by the last vote; 0 means nobody was lynched
	MafiaFirstVoterID int64   // living mafia who votes first tonight; 0 if none
	AlibiPlayerID     int64   // living player with a beauty alibi today; 0 if none
}

type Player struct {
	ID        int64
	Username  string
	FirstName string
	Role      Role
	Alive     bool
}

type Role string

const (
	RoleCivilian  Role = "civilian"
	RoleMafia     Role = "mafia"
	RoleDoctor    Role = "doctor"
	RoleDetective Role = "detective"
	RoleBeauty    Role = "beauty"
)

type Phase int

const (
	PhaseLobby Phase = iota
	PhaseNight
	PhaseDiscussion
	PhaseVoting
	PhaseFinished
)

type GameSettings struct {
	MinPlayers         int
	MaxPlayers         int
	MafiaRules         []MafiaRule
	Doctor             RoleRule
	Detective          RoleRule
	Beauty             RoleRule
	LobbyDuration      time.Duration
	NightDuration      time.Duration
	DiscussionDuration time.Duration
	VotingDuration     time.Duration
}

// MafiaRule chooses the number of mafia for games up to MaxPlayers.
type MafiaRule struct {
	MaxPlayers int
	Count      int
}

type RoleRule struct {
	MinPlayers int
	Count      int
}

type GameResults struct {
	Players  []Player
	Winner   Team
	Duration time.Duration
}

type Team string

const (
	TeamMafia     Team = "mafia"
	TeamCivilians Team = "civilians"
)
