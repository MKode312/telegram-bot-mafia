package game

import (
	"fmt"
	"strings"
	"testing"
	"time"

	models "tgbot-mafia/internal/domain"
)

func TestFormatResults(t *testing.T) {
	results := models.GameResults{
		Winner:   models.TeamCivilians,
		Duration: 7*time.Minute + 23*time.Second,
		Players: []models.Player{
			{ID: 1, FirstName: "Дашулька", Username: "dashulka", Role: models.RoleDoctor, Alive: true},
			{ID: 2, FirstName: "Николай", Username: "nikolay", Role: models.RoleCivilian, Alive: true},
			{ID: 3, FirstName: "Кирюша", Username: "kiryusha", Role: models.RoleCivilian, Alive: true},
			{ID: 4, FirstName: "Чумабой", Username: "chumaboy", Role: models.RoleDetective, Alive: true},
			{ID: 5, FirstName: "Олеся", Username: "olesya", Role: models.RoleCivilian, Alive: true},
			{ID: 6, FirstName: "Anastasia", Username: "anastasia", Role: models.RoleCivilian, Alive: false},
			{ID: 7, FirstName: "Данила", Username: "glukhov", Role: models.RoleMafia, Alive: false},
		},
	}

	text := formatResults(results)
	want := strings.Join([]string{
		"Игра завершена!",
		"Победили: Мирные жители",
		"",
		"Победители:",
		"    " + profileLink(1, "Дашулька") + " - " + resultRoleName(models.RoleDoctor),
		"    " + profileLink(2, "Николай") + " - " + resultRoleName(models.RoleCivilian),
		"    " + profileLink(3, "Кирюша") + " - " + resultRoleName(models.RoleCivilian),
		"    " + profileLink(4, "Чумабой") + " - " + resultRoleName(models.RoleDetective),
		"    " + profileLink(5, "Олеся") + " - " + resultRoleName(models.RoleCivilian),
		"",
		"Остальные участники:",
		"    " + profileLink(6, "Anastasia") + " - " + resultRoleName(models.RoleCivilian),
		"    " + profileLink(7, "Данила") + " - " + resultRoleName(models.RoleMafia),
		"",
		"Игра длилась: 7 мин. 23 сек.",
	}, "\n")

	if text != want {
		t.Fatalf("formatResults() mismatch\nwant:\n%s\n\ngot:\n%s", want, text)
	}
}

func profileLink(id int64, name string) string {
	return fmt.Sprintf(`<a href="tg://user?id=%d">%s</a>`, id, name)
}
