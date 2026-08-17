package common

import (
	"strings"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

type Command struct {
	Name        string
	Description string
}

// MatchCommand matches /name and /name@bot in groups (Telegram menu sends the @bot suffix).
func MatchCommand(name string) bot.MatchFunc {
	return func(update *models.Update) bool {
		if update.Message == nil {
			return false
		}
		text := update.Message.Text
		for _, entity := range update.Message.Entities {
			if entity.Type != models.MessageEntityTypeBotCommand {
				continue
			}
			end := entity.Offset + entity.Length
			if entity.Offset < 0 || end > len(text) {
				continue
			}
			cmd := text[entity.Offset+1 : end]
			if at := strings.IndexByte(cmd, '@'); at >= 0 {
				cmd = cmd[:at]
			}
			if cmd == name {
				return true
			}
		}
		return false
	}
}

var Commands = []Command{
	{Name: "start", Description: "Начать работу с ботом"},
	{Name: "help", Description: "Список команд"},
	{Name: "create", Description: "Создать игру"},
	{Name: "leave", Description: "Выйти из лобби"},
	{Name: "game", Description: "Состояние игры"},
	{Name: "startgame", Description: "Начать игру (создатель)"},
	{Name: "cancel", Description: "Отменить игру (создатель)"},
}

// BotCommands converts Commands into Telegram BotCommand values for SetMyCommands.
func BotCommands() []models.BotCommand {
	commands := make([]models.BotCommand, 0, len(Commands))
	for _, command := range Commands {
		commands = append(commands, models.BotCommand{Command: command.Name, Description: command.Description})
	}
	return commands
}
