package config

import (
	"errors"
	"flag"
	"os"
	"time"

	"github.com/ilyakaznacheev/cleanenv"
	"github.com/joho/godotenv"
)

type Config struct {
	Env  string     `yaml:"env" env-default:"local"`
	Token string
	Game GameConfig `yaml:"game"`
}

type GameConfig struct {
	MinPlayers int          `yaml:"min_players" env-required:"true"`
	MaxPlayers int          `yaml:"max_players" env-default:"10"`
	Mafia      []MafiaRule  `yaml:"mafia"`
	Roles      RolesConfig  `yaml:"roles"`
	Timers     TimersConfig `yaml:"timers"`
}

type MafiaRule struct {
	MaxPlayers int `yaml:"max_players" env-required:"true"`
	Count      int `yaml:"count" env-required:"true"`
}

type RolesConfig struct {
	Doctor    RoleConfig `yaml:"doctor"`
	Detective RoleConfig `yaml:"detective"`
	Beauty    RoleConfig `yaml:"beauty"`
}

type RoleConfig struct {
	MinPlayers int `yaml:"min_players" env-required:"true"`
	Count      int `yaml:"count" env-required:"true"`
}

type TimersConfig struct {
	Lobby      time.Duration `yaml:"lobby" env-required:"true"`
	Night      time.Duration `yaml:"night" env-required:"true"`
	Discussion time.Duration `yaml:"discussion" env-required:"true"`
	Voting     time.Duration `yaml:"voting" env-required:"true"`
}

// MustLoad reads configuration from the path returned by MustFetchConfigPath.
// It panics if the path is empty or the file cannot be loaded.
func MustLoad() *Config {
	configPath := MustFetchConfigPath()
	if configPath == "" {
		panic("config path is empty")
	}

	return MustLoadByPath(configPath)
}

// MustLoadByPath reads YAML from configPath and BOT_TOKEN from the environment.
// It panics if the file is missing, invalid, or BOT_TOKEN is empty.
func MustLoadByPath(configPath string) *Config {
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		panic("config file does not exist: " + configPath)
	}

	var cfg Config

	if err := cleanenv.ReadConfig(configPath, &cfg); err != nil {
		panic("cannot read config: " + err.Error())
	}

	cfg.Token = os.Getenv("BOT_TOKEN")
	if cfg.Token == "" {
		panic("BOT_TOKEN is empty")
	}

	return &cfg
}

// MustFetchConfigPath returns the config file path from -config or CONFIG_PATH.
// A local .env file is loaded when present; a missing file is ignored.
func MustFetchConfigPath() string {
	var res string

	if err := godotenv.Load(); err != nil && !errors.Is(err, os.ErrNotExist) {
		panic(err)
	}

	flag.StringVar(&res, "config", "", "path to config file")
	flag.Parse()

	if res == "" {
		res = os.Getenv("CONFIG_PATH")
	}

	return res
}
