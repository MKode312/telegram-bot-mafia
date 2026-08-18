package game

import (
	"context"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	models "tgbot-mafia/internal/domain"
	"tgbot-mafia/internal/lib/logger/sl"
	gamemanager "tgbot-mafia/internal/services/game-manager"
	"tgbot-mafia/internal/telegram-bot/handlers/common"

	"github.com/go-telegram/bot"
	telegrammodels "github.com/go-telegram/bot/models"
)

type trackedMessage struct {
	ChatID    int64
	MessageID int
}

type Handler struct {
	log     *slog.Logger
	manager *gamemanager.Service

	mu             sync.RWMutex
	lobbyMessages  map[int64]int
	phaseTimers    map[int64]*time.Timer
	warningTimers  map[int64]*time.Timer
	actionMessages map[int64][]trackedMessage // game chat ID -> keyboard messages
	phaseOps       sync.Mutex
}

// New creates a game command handler bound to manager.
func New(log *slog.Logger, manager *gamemanager.Service) *Handler {
	return &Handler{
		log:            log,
		manager:        manager,
		lobbyMessages:  make(map[int64]int),
		phaseTimers:    make(map[int64]*time.Timer),
		warningTimers:  make(map[int64]*time.Timer),
		actionMessages: make(map[int64][]trackedMessage),
	}
}

// Register attaches lobby, night-action and voting handlers to b.
func (h *Handler) Register(b *bot.Bot) {
	b.RegisterHandlerMatchFunc(common.MatchCommand("create"), common.CleanupGroupCommand(h.log, h.create))
	b.RegisterHandlerMatchFunc(common.MatchCommand("leave"), common.CleanupGroupCommand(h.log, h.leave))
	b.RegisterHandlerMatchFunc(common.MatchCommand("game"), common.CleanupGroupCommand(h.log, h.status))
	b.RegisterHandlerMatchFunc(common.MatchCommand("cancel"), common.CleanupGroupCommand(h.log, h.cancel))
	b.RegisterHandlerMatchFunc(common.MatchCommand("startgame"), common.CleanupGroupCommand(h.log, h.start))
	b.RegisterHandler(bot.HandlerTypeCallbackQueryData, "vote:", bot.MatchTypePrefix, h.voteCallback)
	b.RegisterHandler(bot.HandlerTypeCallbackQueryData, "night:", bot.MatchTypePrefix, h.nightActionCallback)
	b.RegisterHandler(bot.HandlerTypeCallbackQueryData, "detact:", bot.MatchTypePrefix, h.detectiveActionCallback)
	b.RegisterHandler(bot.HandlerTypeCallbackQueryData, "lobbyjoin:", bot.MatchTypePrefix, h.joinCallback)
}

func (h *Handler) create(ctx context.Context, b *bot.Bot, update *telegrammodels.Update) {
	chatID, userID, _, ok := common.ChatAndUser(update)
	if !ok {
		return
	}
	game, err := h.manager.CreateGame(chatID, playerFromTelegram(*update.Message.From))
	if err != nil {
		h.replyError(ctx, b, update, "create game", err)
		return
	}
	h.log.Info("game created by Telegram command", "chat_id", chatID, "user_id", userID, "lobby_duration", game.Settings.LobbyDuration)
	message := common.SendWithKeyboard(ctx, b, h.log, chatID, LobbyText(game), lobbyKeyboard(chatID))
	if message != nil {
		h.setLobbyMessage(chatID, message.ID)
	}
	h.scheduleLobbyTimer(ctx, b, chatID, game.Settings.LobbyDuration)
}

func (h *Handler) leave(ctx context.Context, b *bot.Bot, update *telegrammodels.Update) {
	chatID, userID, _, ok := common.ChatAndUser(update)
	if !ok {
		return
	}
	wasCreator := false
	if current, err := h.manager.Game(chatID); err == nil {
		wasCreator = current.CreatorID == userID
	}
	game, err := h.manager.LeaveGame(chatID, userID)
	if err != nil {
		h.replyError(ctx, b, update, "leave game", err)
		return
	}
	h.log.Info("player left via Telegram command", "chat_id", chatID, "user_id", userID, "creator_id", game.CreatorID)

	if len(game.Players) == 0 {
		h.cancelPhaseTimer(chatID)
		if !h.markLobbyCancelled(ctx, b, chatID, cancelledLobbyText) {
			common.Send(ctx, b, h.log, chatID, cancelledLobbyText)
		}
		return
	}
	h.updateLobby(ctx, b, game)
	if wasCreator {
		common.Send(ctx, b, h.log, chatID, fmt.Sprintf("👑 Создатель покинул лобби. Новый создатель: %s.", DisplayName(creatorOf(game))))
	}
}

func creatorOf(game models.Game) models.Player {
	for _, player := range game.Players {
		if player.ID == game.CreatorID {
			return player
		}
	}
	if len(game.Players) > 0 {
		return game.Players[0]
	}
	return models.Player{}
}

func (h *Handler) status(ctx context.Context, b *bot.Bot, update *telegrammodels.Update) {
	chatID, userID, _, ok := common.ChatAndUser(update)
	if !ok {
		return
	}
	game, err := h.manager.Game(chatID)
	if err != nil {
		h.replyError(ctx, b, update, "get game", err)
		return
	}
	h.log.Debug("game status requested", "chat_id", chatID, "user_id", userID)
	common.Reply(ctx, b, h.log, update, formatStatus(game))
}

func (h *Handler) cancel(ctx context.Context, b *bot.Bot, update *telegrammodels.Update) {
	chatID, userID, _, ok := common.ChatAndUser(update)
	if !ok {
		return
	}
	h.phaseOps.Lock()
	defer h.phaseOps.Unlock()

	if _, err := h.manager.CancelGame(chatID, userID); err != nil {
		h.replyError(ctx, b, update, "cancel game", err)
		return
	}
	h.log.Info("game cancelled via Telegram command", "chat_id", chatID, "user_id", userID)
	h.cancelPhaseTimer(chatID)
	h.clearActionKeyboards(ctx, b, chatID)
	if !h.markLobbyCancelled(ctx, b, chatID, cancelledLobbyText) {
		common.Reply(ctx, b, h.log, update, cancelledLobbyText)
	}
}

func (h *Handler) start(ctx context.Context, b *bot.Bot, update *telegrammodels.Update) {
	chatID, userID, _, ok := common.ChatAndUser(update)
	if !ok {
		return
	}
	current, err := h.manager.Game(chatID)
	if err != nil {
		h.replyError(ctx, b, update, "start game", err)
		return
	}
	if missing := playersWithoutDM(ctx, b, h.log, current.Players); len(missing) > 0 {
		h.replyMissingDM(ctx, b, update, chatID, missing)
		return
	}
	game, err := h.manager.StartGame(chatID, userID)
	if err != nil {
		h.replyError(ctx, b, update, "start game", err)
		return
	}
	h.log.Info("game started via Telegram command", "chat_id", chatID, "user_id", userID)
	h.cancelPhaseTimer(chatID)
	h.deleteLobbyMessage(ctx, b, chatID)
	h.clearActionKeyboards(ctx, b, chatID)
	h.sendRoleMessages(ctx, b, game)
	h.scheduleNightTimer(ctx, b, chatID, time.Until(game.EndsAt))
	h.announceNight(ctx, b, game, "🎬 Игра началась.")
}

func (h *Handler) joinCallback(ctx context.Context, b *bot.Bot, update *telegrammodels.Update) {
	query := update.CallbackQuery
	if query == nil {
		return
	}
	chatID, err := parseLobbyCallback(query.Data)
	if err != nil {
		h.log.Error("invalid lobby join callback", "data", query.Data, sl.Err(err))
		h.answerCallback(ctx, b, query.ID, "⏳ Кнопка лобби устарела.", true)
		return
	}
	// Keep joining and rendering the shared lobby message ordered. Telegram can
	// deliver callback updates concurrently.
	h.phaseOps.Lock()
	defer h.phaseOps.Unlock()
	game, err := h.manager.JoinGame(chatID, playerFromTelegram(query.From))
	if err != nil {
		h.log.Error("lobby join callback rejected", "chat_id", chatID, "user_id", query.From.ID, sl.Err(err))
		h.answerCallbackError(ctx, b, query.ID, err)
		return
	}
	h.log.Info("player joined via Telegram button", "chat_id", chatID, "user_id", query.From.ID, "players", len(game.Players))
	h.updateLobby(ctx, b, game)
	h.answerCallback(ctx, b, query.ID, "✅ Вы присоединились к игре.", false)
}

func (h *Handler) voteCallback(ctx context.Context, b *bot.Bot, update *telegrammodels.Update) {
	query := update.CallbackQuery
	if query == nil {
		return
	}
	chatID, targetID, err := parseVoteCallback(query)
	if err != nil {
		h.log.Error("invalid vote callback", "data", query.Data, sl.Err(err))
		h.answerCallback(ctx, b, query.ID, "⏳ Кнопка голосования устарела.", true)
		return
	}

	h.phaseOps.Lock()
	defer h.phaseOps.Unlock()

	game, err := h.manager.Vote(chatID, query.From.ID, targetID)
	if err != nil {
		h.log.Error("vote callback rejected", "chat_id", chatID, "user_id", query.From.ID, sl.Err(err))
		h.answerCallbackError(ctx, b, query.ID, err)
		return
	}

	h.log.Info("vote accepted via Telegram button", "chat_id", chatID, "user_id", query.From.ID)
	h.answerCallback(ctx, b, query.ID, "✅ Голос принят.", false)
	h.clearPlayerActionKeyboards(ctx, b, chatID, query.From.ID)
	if target, ok := playerByID(game, targetID); ok {
		common.Send(ctx, b, h.log, query.From.ID, "🗳️ Вы проголосовали за: "+DisplayName(target)+".")
	}
	if game.Phase == models.PhaseNight || game.Phase == models.PhaseFinished {
		h.onVotingResolved(ctx, b, game)
	}
}

func (h *Handler) nightActionCallback(ctx context.Context, b *bot.Bot, update *telegrammodels.Update) {
	query := update.CallbackQuery
	if query == nil {
		return
	}
	chatID, targetID, actionType, err := parseNightCallback(query.Data)
	if err != nil {
		h.log.Error("invalid night action callback", "data", query.Data, sl.Err(err))
		h.answerCallback(ctx, b, query.ID, "⏳ Кнопка действия устарела.", true)
		return
	}

	role, err := h.manager.PlayerRole(chatID, query.From.ID)
	if err != nil {
		h.answerCallbackError(ctx, b, query.ID, err)
		return
	}
	if role == models.RoleDetective && actionType != gamemanager.NightActionCheck && actionType != gamemanager.NightActionShoot {
		h.answerCallback(ctx, b, query.ID, "🔍 Сначала выберите: проверить или выстрелить.", true)
		return
	}
	targetRole, err := h.manager.PlayerRole(chatID, targetID)
	if err != nil {
		h.answerCallbackError(ctx, b, query.ID, err)
		return
	}

	h.phaseOps.Lock()
	defer h.phaseOps.Unlock()

	game, err := h.manager.SubmitNightAction(chatID, query.From.ID, targetID, actionType)
	if err != nil {
		h.log.Error("night action callback rejected", "chat_id", chatID, "user_id", query.From.ID, sl.Err(err))
		h.answerCallbackError(ctx, b, query.ID, err)
		return
	}

	message := "✅ Действие принято."
	if role == models.RoleDetective && actionType == gamemanager.NightActionCheck {
		message = "😇 Проверка завершена: игрок не мафия."
		if targetRole == models.RoleMafia {
			message = "🔪 Проверка завершена: игрок — мафия."
		}
	}
	if role == models.RoleDetective && actionType == gamemanager.NightActionShoot {
		message = "🔫 Выстрел сделан."
	}
	if role == models.RoleBeauty {
		message = "💋 Алиби выдано."
	}
	h.log.Info("night action accepted via Telegram button", "chat_id", chatID, "user_id", query.From.ID, "action_type", actionType)
	h.answerCallback(ctx, b, query.ID, message, true)
	h.clearPlayerActionKeyboards(ctx, b, chatID, query.From.ID)
	if role == models.RoleDetective && actionType == gamemanager.NightActionCheck {
		common.Send(ctx, b, h.log, query.From.ID, message)
	}
	if announce := nightChoiceAnnounce(role, actionType); announce != "" {
		if flavor := h.manager.TakeNightFlavor(chatID, role, actionType); flavor != "" {
			announce += " " + flavor
		}
		common.Send(ctx, b, h.log, chatID, announce)
	}
	if role == models.RoleMafia {
		h.notifyMafiaPartnerChoice(ctx, b, game, query.From.ID, targetID)
	}
	if game.Phase == models.PhaseDiscussion || game.Phase == models.PhaseFinished {
		h.onNightResolved(ctx, b, game)
	}
}

func (h *Handler) detectiveActionCallback(ctx context.Context, b *bot.Bot, update *telegrammodels.Update) {
	query := update.CallbackQuery
	if query == nil {
		return
	}
	chatID, actionType, err := parseDetectiveActionCallback(query.Data)
	if err != nil {
		h.log.Error("invalid detective action callback", "data", query.Data, sl.Err(err))
		h.answerCallback(ctx, b, query.ID, "⏳ Кнопка устарела.", true)
		return
	}
	role, err := h.manager.PlayerRole(chatID, query.From.ID)
	if err != nil {
		h.answerCallbackError(ctx, b, query.ID, err)
		return
	}
	if role != models.RoleDetective {
		h.answerCallback(ctx, b, query.ID, "🔍 Это действие только для детектива.", true)
		return
	}
	targets, err := h.manager.NightActionTargets(chatID, query.From.ID, actionType)
	if err != nil {
		h.answerCallbackError(ctx, b, query.ID, err)
		return
	}
	prompt := "🔍 Выберите игрока для проверки:"
	if actionType == gamemanager.NightActionShoot {
		prompt = "🔫 Выберите игрока для выстрела:"
	}
	h.answerCallback(ctx, b, query.ID, "", false)
	h.clearPlayerActionKeyboards(ctx, b, chatID, query.From.ID)
	h.sendTrackedKeyboard(ctx, b, chatID, query.From.ID, prompt, nightKeyboard(chatID, targets, actionType))
}

func (h *Handler) replyError(ctx context.Context, b *bot.Bot, update *telegrammodels.Update, operation string, err error) {
	h.log.Error("Telegram game command failed", "operation", operation, sl.Err(err))
	common.Reply(ctx, b, h.log, update, userError(err))
}

func (h *Handler) answerCallback(ctx context.Context, b *bot.Bot, callbackID, text string, alert bool) {
	if _, err := b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: callbackID, Text: text, ShowAlert: alert}); err != nil {
		h.log.Error("failed to answer Telegram callback", sl.Err(err))
	}
}

func (h *Handler) answerCallbackError(ctx context.Context, b *bot.Bot, callbackID string, err error) {
	alert := true
	if errors.Is(err, gamemanager.ErrUnauthorized) || errors.Is(err, gamemanager.ErrPlayerAlreadyExists) {
		alert = false
	}
	h.answerCallback(ctx, b, callbackID, userError(err), alert)
}

func parseVoteCallback(query *telegrammodels.CallbackQuery) (chatID, targetID int64, err error) {
	return parseCallback(query.Data, "vote")
}

func parseDetectiveActionCallback(data string) (chatID int64, actionType string, err error) {
	parts := strings.Split(data, ":")
	if len(parts) != 3 || parts[0] != "detact" {
		return 0, "", errors.New("unexpected callback data")
	}
	chatID, err = strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return 0, "", fmt.Errorf("parse chat ID: %w", err)
	}
	actionType = parts[2]
	if actionType != gamemanager.NightActionCheck && actionType != gamemanager.NightActionShoot {
		return 0, "", errors.New("unexpected detective action")
	}
	return chatID, actionType, nil
}

func parseNightCallback(data string) (chatID, targetID int64, actionType string, err error) {
	parts := strings.Split(data, ":")
	if parts[0] != "night" {
		return 0, 0, "", errors.New("unexpected callback data")
	}
	if len(parts) != 3 && len(parts) != 4 {
		return 0, 0, "", errors.New("unexpected callback data")
	}
	if chatID, err = strconv.ParseInt(parts[1], 10, 64); err != nil {
		return 0, 0, "", fmt.Errorf("parse chat ID: %w", err)
	}
	if targetID, err = strconv.ParseInt(parts[2], 10, 64); err != nil {
		return 0, 0, "", fmt.Errorf("parse target ID: %w", err)
	}
	if len(parts) == 4 {
		actionType = parts[3]
	}
	return chatID, targetID, actionType, nil
}

func parseLobbyCallback(data string) (int64, error) {
	parts := strings.Split(data, ":")
	if len(parts) != 2 || parts[0] != "lobbyjoin" {
		return 0, errors.New("unexpected callback data")
	}
	chatID, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse chat ID: %w", err)
	}
	return chatID, nil
}
func parseCallback(data, prefix string) (chatID, targetID int64, err error) {
	parts := strings.Split(data, ":")
	if len(parts) != 3 || parts[0] != prefix {
		return 0, 0, errors.New("unexpected callback data")
	}
	if chatID, err = strconv.ParseInt(parts[1], 10, 64); err != nil {
		return 0, 0, fmt.Errorf("parse chat ID: %w", err)
	}
	if targetID, err = strconv.ParseInt(parts[2], 10, 64); err != nil {
		return 0, 0, fmt.Errorf("parse target ID: %w", err)
	}
	return chatID, targetID, nil
}

// VoteKeyboard lists living players except the voter and the alibi target.
func VoteKeyboard(chatID, voterID, alibiID int64, players []models.Player) *telegrammodels.InlineKeyboardMarkup {
	keyboard := make([][]telegrammodels.InlineKeyboardButton, 0, len(players))
	for _, player := range players {
		if !player.Alive || player.ID == voterID || player.ID == alibiID {
			continue
		}
		keyboard = append(keyboard, []telegrammodels.InlineKeyboardButton{{
			Text:         PlayerLabel(player),
			CallbackData: fmt.Sprintf("vote:%d:%d", chatID, player.ID),
		}})
	}
	return &telegrammodels.InlineKeyboardMarkup{InlineKeyboard: keyboard}
}

func (h *Handler) sendRoleMessages(ctx context.Context, b *bot.Bot, game models.Game) {
	for _, player := range game.Players {
		text := "🎭 Ваша роль: " + roleName(player.Role) + "."
		if player.Role == models.RoleMafia {
			text += mafiaAlliesText(h, game.ChatID, player.ID)
		}
		if !canActAtNight(player.Role) {
			common.Send(ctx, b, h.log, player.ID, text)
			continue
		}
		if player.Role == models.RoleDetective {
			h.sendTrackedKeyboard(ctx, b, game.ChatID, player.ID, text+"\n\n"+nightPrompt(player.Role), detectiveActionKeyboard(game.ChatID))
			h.log.Info("role and detective action keyboard sent", "chat_id", game.ChatID, "player_id", player.ID)
			continue
		}
		targets, err := h.manager.NightActionTargets(game.ChatID, player.ID, "")
		if err != nil {
			h.log.Error("failed to build night targets", "chat_id", game.ChatID, "player_id", player.ID, sl.Err(err))
			common.Send(ctx, b, h.log, player.ID, text)
			continue
		}
		prompt := nightPrompt(player.Role)
		if player.Role == models.RoleMafia {
			prompt = mafiaNightPrompt(game, player.ID)
		}
		h.sendTrackedKeyboard(ctx, b, game.ChatID, player.ID, text+"\n\n"+prompt, nightKeyboard(game.ChatID, targets, ""))
		h.log.Info("role and night action keyboard sent", "chat_id", game.ChatID, "player_id", player.ID)
	}
}

func (h *Handler) sendNightActionMessages(ctx context.Context, b *bot.Bot, game models.Game) {
	for _, player := range nightActorsOrdered(game) {
		h.sendNightActionTo(ctx, b, game, player)
	}
}

func nightActorsOrdered(game models.Game) []models.Player {
	actors := make([]models.Player, 0)
	var firstMafia *models.Player
	for i := range game.Players {
		player := game.Players[i]
		if !player.Alive || !canActAtNight(player.Role) {
			continue
		}
		if player.Role == models.RoleMafia && player.ID == game.MafiaFirstVoterID {
			copy := player
			firstMafia = &copy
			continue
		}
		actors = append(actors, player)
	}
	if firstMafia != nil {
		return append([]models.Player{*firstMafia}, actors...)
	}
	return actors
}

func (h *Handler) sendNightActionTo(ctx context.Context, b *bot.Bot, game models.Game, player models.Player) {
	if player.Role == models.RoleDetective {
		h.sendTrackedKeyboard(ctx, b, game.ChatID, player.ID, "🌙 Наступила ночь.\n\n"+nightPrompt(player.Role), detectiveActionKeyboard(game.ChatID))
		h.log.Info("detective action keyboard sent", "chat_id", game.ChatID, "player_id", player.ID)
		return
	}
	targets, err := h.manager.NightActionTargets(game.ChatID, player.ID, "")
	if err != nil {
		h.log.Error("failed to build night targets", "chat_id", game.ChatID, "player_id", player.ID, sl.Err(err))
		return
	}
	prompt := nightPrompt(player.Role)
	if player.Role == models.RoleMafia {
		prompt = mafiaNightPrompt(game, player.ID)
	}
	h.sendTrackedKeyboard(ctx, b, game.ChatID, player.ID, "🌙 Наступила ночь.\n\n"+prompt, nightKeyboard(game.ChatID, targets, ""))
	h.log.Info("night action keyboard sent", "chat_id", game.ChatID, "player_id", player.ID)
}

func (h *Handler) notifyMafiaPartnerChoice(ctx context.Context, b *bot.Bot, game models.Game, actorID, targetID int64) {
	allies, err := h.manager.LivingMafiaAllies(game.ChatID, actorID)
	if err != nil {
		h.log.Error("failed to load mafia allies for notify", "chat_id", game.ChatID, "actor_id", actorID, sl.Err(err))
		return
	}
	actor, actorOK := playerByID(game, actorID)
	target, targetOK := playerByID(game, targetID)
	if !actorOK || !targetOK {
		return
	}
	text := fmt.Sprintf("🔪 Напарник %s выбрал жертву: %s.", DisplayName(actor), DisplayName(target))
	for _, ally := range allies {
		common.Send(ctx, b, h.log, ally.ID, text)
	}
}

func mafiaAlliesText(h *Handler, chatID, playerID int64) string {
	allies, err := h.manager.LivingMafiaAllies(chatID, playerID)
	if err != nil || len(allies) == 0 {
		return ""
	}
	names := make([]string, 0, len(allies))
	for _, ally := range allies {
		names = append(names, DisplayName(ally))
	}
	if len(names) == 1 {
		return "\n\n🤝 Ваш союзник: " + names[0] + "."
	}
	return "\n\n🤝 Ваши союзники: " + strings.Join(names, ", ") + "."
}

func mafiaNightPrompt(game models.Game, playerID int64) string {
	if game.MafiaFirstVoterID == playerID {
		return "🔪 Этой ночью вы голосуете первым. Выберите жертву:"
	}
	if game.MafiaFirstVoterID != 0 {
		return "🔪 Этой ночью первым голосует напарник. Выберите жертву:"
	}
	return "🔪 Выберите игрока, которого хотите устранить:"
}

func detectiveActionKeyboard(chatID int64) *telegrammodels.InlineKeyboardMarkup {
	return &telegrammodels.InlineKeyboardMarkup{InlineKeyboard: [][]telegrammodels.InlineKeyboardButton{
		{{Text: "🔍 Проверить", CallbackData: fmt.Sprintf("detact:%d:%s", chatID, gamemanager.NightActionCheck)}},
		{{Text: "🔫 Выстрелить", CallbackData: fmt.Sprintf("detact:%d:%s", chatID, gamemanager.NightActionShoot)}},
	}}
}

func nightKeyboard(chatID int64, targets []models.Player, actionType string) *telegrammodels.InlineKeyboardMarkup {
	keyboard := make([][]telegrammodels.InlineKeyboardButton, 0, len(targets))
	for _, player := range targets {
		callback := fmt.Sprintf("night:%d:%d", chatID, player.ID)
		if actionType != "" {
			callback = fmt.Sprintf("night:%d:%d:%s", chatID, player.ID, actionType)
		}
		keyboard = append(keyboard, []telegrammodels.InlineKeyboardButton{{Text: PlayerLabel(player), CallbackData: callback}})
	}
	return &telegrammodels.InlineKeyboardMarkup{InlineKeyboard: keyboard}
}

func lobbyKeyboard(chatID int64) *telegrammodels.InlineKeyboardMarkup {
	return &telegrammodels.InlineKeyboardMarkup{InlineKeyboard: [][]telegrammodels.InlineKeyboardButton{{{
		Text: "✅ Присоединиться", CallbackData: fmt.Sprintf("lobbyjoin:%d", chatID),
	}}}}
}

// LobbyText is the group lobby message with player name links.
func LobbyText(game models.Game) string {
	players := make([]string, 0, len(game.Players))
	for index, player := range game.Players {
		line := fmt.Sprintf("    %d. %s", index+1, DisplayName(player))
		if player.ID == game.CreatorID {
			line += " 👑"
		}
		players = append(players, line)
	}
	text := fmt.Sprintf("🎭 Игра в мафию\n\n👥 Участники (%d/%d):\n\n%s", len(game.Players), game.Settings.MaxPlayers, strings.Join(players, "\n"))
	return text + "\n\nНажмите «Присоединиться», чтобы войти в игру.\n\n💬 Перед стартом напишите боту в личные сообщения, если вы этого ещё не делали ни разу (/start)."
}

func (h *Handler) updateLobby(ctx context.Context, b *bot.Bot, game models.Game) {
	h.mu.RLock()
	messageID, exists := h.lobbyMessages[game.ChatID]
	h.mu.RUnlock()
	if !exists {
		return
	}
	if _, err := b.EditMessageText(ctx, &bot.EditMessageTextParams{
		ChatID:      game.ChatID,
		MessageID:   messageID,
		Text:        LobbyText(game),
		ParseMode:   telegrammodels.ParseModeHTML,
		ReplyMarkup: lobbyKeyboard(game.ChatID),
	}); err != nil {
		h.log.Error("failed to update lobby message", "chat_id", game.ChatID, "message_id", messageID, sl.Err(err))
		return
	}
	h.log.Debug("lobby message updated", "chat_id", game.ChatID, "players", len(game.Players))
}

func (h *Handler) setLobbyMessage(chatID int64, messageID int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.lobbyMessages[chatID] = messageID
}

func (h *Handler) deleteLobbyMessage(ctx context.Context, b *bot.Bot, chatID int64) {
	h.mu.Lock()
	messageID, exists := h.lobbyMessages[chatID]
	if exists {
		delete(h.lobbyMessages, chatID)
	}
	h.mu.Unlock()
	if !exists {
		return
	}
	common.DeleteMessage(ctx, b, h.log, chatID, messageID)
}

func (h *Handler) announceVoting(ctx context.Context, b *bot.Bot, game models.Game) {
	h.clearActionKeyboards(ctx, b, game.ChatID)
	text := votingStartText(game)
	if link, err := common.BotPrivateChatLink(ctx, b); err != nil {
		h.log.Error("failed to resolve bot username for voting deep link", sl.Err(err))
		common.Send(ctx, b, h.log, game.ChatID, text)
	} else {
		h.sendTrackedKeyboard(ctx, b, game.ChatID, game.ChatID, text, &telegrammodels.InlineKeyboardMarkup{
			InlineKeyboard: [][]telegrammodels.InlineKeyboardButton{{{
				Text: "Перейти в бота",
				URL:  link,
			}}},
		})
	}
	for _, player := range game.Players {
		if !player.Alive {
			continue
		}
		h.sendTrackedKeyboard(ctx, b, game.ChatID, player.ID, votingPrompt(game), VoteKeyboard(game.ChatID, player.ID, game.AlibiPlayerID, game.Players))
		h.log.Info("voting keyboard sent in DM", "chat_id", game.ChatID, "player_id", player.ID)
	}
	h.scheduleVotingTimer(ctx, b, game.ChatID, time.Until(game.EndsAt))
}

func votingStartText(game models.Game) string {
	text := "🗳️ Голосование началось. Живые игроки, проголосуйте в личных сообщениях с ботом."
	if alibi := AlibiText(game); alibi != "" {
		return text + "\n\n" + alibi
	}
	return text
}

func votingPrompt(game models.Game) string {
	text := "🗳️ Голосование началось. Выберите живого игрока кнопкой:"
	if alibi := AlibiText(game); alibi != "" {
		return text + "\n\n" + alibi
	}
	return text
}

// AlibiText announces who cannot be voted today, or empty if nobody has an alibi.
func AlibiText(game models.Game) string {
	if game.AlibiPlayerID == 0 {
		return ""
	}
	player, ok := playerByID(game, game.AlibiPlayerID)
	if !ok || !player.Alive {
		return ""
	}
	return fmt.Sprintf("💋 У %s алиби. Сегодня за этого игрока нельзя голосовать.", DisplayName(player))
}

func (h *Handler) onVotingResolved(ctx context.Context, b *bot.Bot, game models.Game) {
	h.cancelPhaseTimer(game.ChatID)
	h.clearActionKeyboards(ctx, b, game.ChatID)
	h.notifyLynchedVictim(ctx, b, game)
	common.Send(ctx, b, h.log, game.ChatID, votingOutcomeText(game))
	common.Send(ctx, b, h.log, game.ChatID, formatStatus(game))
	if game.Phase == models.PhaseFinished {
		h.finishGame(ctx, b, game)
		return
	}
	if game.Phase == models.PhaseNight {
		h.announceNight(ctx, b, game, "")
		h.sendNightActionMessages(ctx, b, game)
		h.scheduleNightTimer(ctx, b, game.ChatID, time.Until(game.EndsAt))
	}
}

func (h *Handler) onNightResolved(ctx context.Context, b *bot.Bot, game models.Game) {
	h.cancelPhaseTimer(game.ChatID)
	h.clearActionKeyboards(ctx, b, game.ChatID)
	common.Send(ctx, b, h.log, game.ChatID, nightOutcomeText(game))
	common.Send(ctx, b, h.log, game.ChatID, formatStatus(game))
	h.notifyNightVictims(ctx, b, game)
	if game.Phase == models.PhaseFinished {
		h.finishGame(ctx, b, game)
		return
	}
	if game.Phase == models.PhaseDiscussion {
		common.Send(ctx, b, h.log, game.ChatID, "💬 Началось обсуждение. Решите, кого вы хотите повесить.")
		if alibi := AlibiText(game); alibi != "" {
			common.Send(ctx, b, h.log, game.ChatID, alibi)
		}
		h.scheduleDiscussionTimer(ctx, b, game.ChatID, time.Until(game.EndsAt))
	}
}

func (h *Handler) notifyNightVictims(ctx context.Context, b *bot.Bot, game models.Game) {
	for _, id := range game.LastKilledIDs {
		common.Send(ctx, b, h.log, id, "💀 Вас убили этой ночью. Вы выбываете из игры.")
	}
}

func (h *Handler) notifyLynchedVictim(ctx context.Context, b *bot.Bot, game models.Game) {
	if game.LastLynchedID == 0 {
		return
	}
	common.Send(ctx, b, h.log, game.LastLynchedID, "⛓️ Вас повесили по итогам голосования. Вы выбываете из игры.")
}

func (h *Handler) finishGame(ctx context.Context, b *bot.Bot, game models.Game) {
	h.clearActionKeyboards(ctx, b, game.ChatID)
	h.sendGameResultsToPlayers(ctx, b, game)
	if err := h.manager.ClearFinishedGame(game.ChatID); err != nil {
		h.log.Error("failed to clear finished game", "chat_id", game.ChatID, sl.Err(err))
		return
	}
	h.log.Info("finished game cleared after results", "chat_id", game.ChatID)
}

func (h *Handler) sendGameResultsToPlayers(ctx context.Context, b *bot.Bot, game models.Game) {
	if game.Results == nil {
		return
	}
	text := FormatResults(*game.Results)
	for _, player := range game.Players {
		common.Send(ctx, b, h.log, player.ID, text)
	}
}

func (h *Handler) scheduleLobbyTimer(ctx context.Context, b *bot.Bot, chatID int64, duration time.Duration) {
	if duration <= 0 {
		return
	}
	h.schedulePhaseTimer(chatID, duration, models.PhaseLobby, "🏠 Лобби", b, func() {
		h.onLobbyExpired(context.Background(), b, chatID)
	})
}

func (h *Handler) scheduleNightTimer(ctx context.Context, b *bot.Bot, chatID int64, duration time.Duration) {
	h.schedulePhaseTimer(chatID, duration, models.PhaseNight, "🌙 Ночь", b, func() {
		h.onNightExpired(context.Background(), b, chatID)
	})
}

func (h *Handler) scheduleDiscussionTimer(ctx context.Context, b *bot.Bot, chatID int64, duration time.Duration) {
	h.schedulePhaseTimer(chatID, duration, models.PhaseDiscussion, "💬 Обсуждение", b, func() {
		h.onDiscussionExpired(context.Background(), b, chatID)
	})
}

func (h *Handler) scheduleVotingTimer(ctx context.Context, b *bot.Bot, chatID int64, duration time.Duration) {
	h.schedulePhaseTimer(chatID, duration, models.PhaseVoting, "🗳️ Голосование", b, func() {
		h.onVotingExpired(context.Background(), b, chatID)
	})
}

func (h *Handler) schedulePhaseTimer(chatID int64, duration time.Duration, expected models.Phase, label string, b *bot.Bot, onFire func()) {
	h.cancelPhaseTimer(chatID)
	if duration < 0 {
		duration = 0
	}
	if delay, remaining, ok := phaseWarning(expected, duration); ok {
		warning := time.AfterFunc(delay, func() {
			game, err := h.manager.Game(chatID)
			if err != nil || game.Phase != expected {
				return
			}
			left := remaining
			if !game.EndsAt.IsZero() {
				if until := time.Until(game.EndsAt); until > 0 {
					left = until
				}
			}
			text := fmt.Sprintf("⏰ %s закончится через %s.", label, formatRemaining(left))
			if expected == models.PhaseLobby {
				text = "⏳ Лобби закроется через " + formatRemaining(left) + "."
			}
			common.Send(context.Background(), b, h.log, chatID, text)
			h.log.Info("phase ending warning sent", "chat_id", chatID, "phase", expected, "remaining", left)
		})
		h.mu.Lock()
		h.warningTimers[chatID] = warning
		h.mu.Unlock()
	}
	timer := time.AfterFunc(duration, onFire)
	h.mu.Lock()
	h.phaseTimers[chatID] = timer
	h.mu.Unlock()
}

func (h *Handler) cancelPhaseTimer(chatID int64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if timer, ok := h.phaseTimers[chatID]; ok {
		timer.Stop()
		delete(h.phaseTimers, chatID)
	}
	if warning, ok := h.warningTimers[chatID]; ok {
		warning.Stop()
		delete(h.warningTimers, chatID)
	}
}

func (h *Handler) sendTrackedKeyboard(ctx context.Context, b *bot.Bot, gameChatID, destChatID int64, text string, keyboard *telegrammodels.InlineKeyboardMarkup) {
	message := common.SendWithKeyboard(ctx, b, h.log, destChatID, text, keyboard)
	if message == nil {
		return
	}
	h.trackActionMessage(gameChatID, destChatID, message.ID)
}

func (h *Handler) trackActionMessage(gameChatID, destChatID int64, messageID int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.actionMessages[gameChatID] = append(h.actionMessages[gameChatID], trackedMessage{ChatID: destChatID, MessageID: messageID})
}

func (h *Handler) clearActionKeyboards(ctx context.Context, b *bot.Bot, gameChatID int64) {
	h.mu.Lock()
	messages := h.actionMessages[gameChatID]
	delete(h.actionMessages, gameChatID)
	h.mu.Unlock()
	for _, message := range messages {
		common.ClearInlineKeyboard(ctx, b, h.log, message.ChatID, message.MessageID)
	}
}

func (h *Handler) clearPlayerActionKeyboards(ctx context.Context, b *bot.Bot, gameChatID, playerChatID int64) {
	h.mu.Lock()
	messages := h.actionMessages[gameChatID]
	kept := messages[:0]
	var toClear []trackedMessage
	for _, message := range messages {
		if message.ChatID == playerChatID {
			toClear = append(toClear, message)
			continue
		}
		kept = append(kept, message)
	}
	if len(kept) == 0 {
		delete(h.actionMessages, gameChatID)
	} else {
		h.actionMessages[gameChatID] = kept
	}
	h.mu.Unlock()
	for _, message := range toClear {
		common.ClearInlineKeyboard(ctx, b, h.log, message.ChatID, message.MessageID)
	}
}

const maxPhaseWarningLead = time.Minute

// phaseWarning returns when to ping that a phase is about to end.
// Lobby is warned at half the timer; other phases are capped at maxPhaseWarningLead.
func phaseWarning(phase models.Phase, duration time.Duration) (delay, remaining time.Duration, ok bool) {
	if duration <= 0 {
		return 0, 0, false
	}
	remaining = duration / 2
	if phase != models.PhaseLobby && remaining > maxPhaseWarningLead {
		remaining = maxPhaseWarningLead
	}
	delay = duration - remaining
	if delay <= 0 || remaining <= 0 {
		return 0, 0, false
	}
	return delay, remaining, true
}

func formatRemaining(d time.Duration) string {
	secs := int(d.Round(time.Second) / time.Second)
	if secs < 1 {
		secs = 1
	}
	if secs < 60 {
		return fmt.Sprintf("%d с", secs)
	}
	minutes := secs / 60
	rem := secs % 60
	if rem == 0 {
		return fmt.Sprintf("%d мин", minutes)
	}
	return fmt.Sprintf("%d мин %d с", minutes, rem)
}

func (h *Handler) onLobbyExpired(ctx context.Context, b *bot.Bot, chatID int64) {
	h.phaseOps.Lock()
	defer h.phaseOps.Unlock()

	current, err := h.manager.Game(chatID)
	if err != nil {
		if !errors.Is(err, gamemanager.ErrGameNotFound) {
			h.log.Error("failed to load lobby before expire", "chat_id", chatID, sl.Err(err))
		}
		return
	}
	if len(current.Players) >= current.Settings.MinPlayers {
		if missing := playersWithoutDM(ctx, b, h.log, current.Players); len(missing) > 0 {
			h.log.Info("lobby cancelled: players have not opened DM", "chat_id", chatID, "missing", len(missing))
			if _, cancelErr := h.manager.CancelGame(chatID, current.CreatorID); cancelErr != nil {
				h.log.Error("failed to cancel lobby after DM check", "chat_id", chatID, sl.Err(cancelErr))
				return
			}
			h.cancelPhaseTimer(chatID)
			h.deleteLobbyMessage(ctx, b, chatID)
			h.sendMissingDM(ctx, b, chatID, missing)
			return
		}
	}

	game, action, err := h.manager.ExpireLobby(chatID)
	if err != nil {
		h.log.Error("failed to expire lobby", "chat_id", chatID, sl.Err(err))
		return
	}
	h.cancelPhaseTimer(chatID)
	switch action {
	case gamemanager.LobbyExpireStarted:
		h.log.Info("lobby timer elapsed, game started", "chat_id", chatID, "players", len(game.Players))
		h.deleteLobbyMessage(ctx, b, chatID)
		h.clearActionKeyboards(ctx, b, chatID)
		h.sendRoleMessages(ctx, b, game)
		h.scheduleNightTimer(ctx, b, chatID, time.Until(game.EndsAt))
		h.announceNight(ctx, b, game, "🎬 Игра началась.")
	case gamemanager.LobbyExpireCancelled:
		h.log.Info("lobby cancelled due to timeout", "chat_id", chatID, "players", len(game.Players))
		h.clearActionKeyboards(ctx, b, chatID)
		const cancelledText = "🚫 Игра отменена: не набралось достаточно игроков."
		if !h.markLobbyCancelled(ctx, b, chatID, cancelledText) {
			common.Send(ctx, b, h.log, chatID, cancelledText)
		}
	}
}

func (h *Handler) onNightExpired(ctx context.Context, b *bot.Bot, chatID int64) {
	h.phaseOps.Lock()
	defer h.phaseOps.Unlock()

	game, resolved, err := h.manager.ExpireNight(chatID)
	if err != nil {
		h.log.Error("failed to expire night", "chat_id", chatID, sl.Err(err))
		return
	}
	if !resolved {
		return
	}
	h.log.Info("night timer elapsed", "chat_id", chatID, "phase", game.Phase)
	h.onNightResolved(ctx, b, game)
}

func (h *Handler) onDiscussionExpired(ctx context.Context, b *bot.Bot, chatID int64) {
	h.phaseOps.Lock()
	defer h.phaseOps.Unlock()

	game, started, err := h.manager.ExpireDiscussion(chatID)
	if err != nil {
		h.log.Error("failed to expire discussion", "chat_id", chatID, sl.Err(err))
		return
	}
	if !started {
		return
	}
	h.log.Info("discussion timer elapsed, voting started", "chat_id", chatID)
	h.announceVoting(ctx, b, game)
}

func (h *Handler) onVotingExpired(ctx context.Context, b *bot.Bot, chatID int64) {
	h.phaseOps.Lock()
	defer h.phaseOps.Unlock()

	game, finished, err := h.manager.ExpireVoting(chatID)
	if err != nil {
		h.log.Error("failed to expire voting", "chat_id", chatID, sl.Err(err))
		return
	}
	if !finished {
		return
	}
	h.log.Info("voting timer elapsed", "chat_id", chatID, "phase", game.Phase)
	h.onVotingResolved(ctx, b, game)
}

const cancelledLobbyText = "🚫 Игра отменена."

func (h *Handler) markLobbyCancelled(ctx context.Context, b *bot.Bot, chatID int64, text string) bool {
	h.mu.Lock()
	messageID, exists := h.lobbyMessages[chatID]
	if exists {
		delete(h.lobbyMessages, chatID)
	}
	h.mu.Unlock()
	if !exists {
		return false
	}
	_, err := b.EditMessageText(ctx, &bot.EditMessageTextParams{
		ChatID:    chatID,
		MessageID: messageID,
		Text:      text,
		ParseMode: telegrammodels.ParseModeHTML,
		ReplyMarkup: &telegrammodels.InlineKeyboardMarkup{
			InlineKeyboard: [][]telegrammodels.InlineKeyboardButton{},
		},
	})
	if err != nil {
		h.log.Error("failed to mark lobby as cancelled", "chat_id", chatID, "message_id", messageID, sl.Err(err))
		return false
	}
	h.log.Debug("lobby message marked as cancelled", "chat_id", chatID)
	return true
}

func userError(err error) string {
	switch {
	case errors.Is(err, gamemanager.ErrGameNotFound):
		return "❌ В этом чате нет активной игры."
	case errors.Is(err, gamemanager.ErrGameAlreadyExists):
		return "⚠️ Игра уже создана."
	case errors.Is(err, gamemanager.ErrPlayerAlreadyExists):
		return "ℹ️ Вы уже в лобби."
	case errors.Is(err, gamemanager.ErrPlayerNotFound):
		return "❌ Игрок не найден."
	case errors.Is(err, gamemanager.ErrNotEnoughPlayers):
		return "👥 Недостаточно игроков для начала."
	case errors.Is(err, gamemanager.ErrUnauthorized):
		return "🚫 Это действие недоступно вам сейчас."
	case errors.Is(err, gamemanager.ErrGameIsNotInLobby):
		return "🏠 Это действие доступно только в лобби."
	case errors.Is(err, gamemanager.ErrInvalidPhase):
		return "⏳ Сейчас это действие недоступно."
	case errors.Is(err, gamemanager.ErrPlayerIsDead):
		return "💀 Вы уже не участвуете в этом раунде."
	case errors.Is(err, gamemanager.ErrVoteAlreadyCast):
		return "✅ Ваш голос уже учтён."
	case errors.Is(err, gamemanager.ErrInvalidTarget):
		return "👉 Выберите другого живого игрока."
	case errors.Is(err, gamemanager.ErrHealRepeatTarget):
		return "💉 Нельзя лечить одного и того же игрока две ночи подряд."
	case errors.Is(err, gamemanager.ErrHealSelfAlreadyUsed):
		return "💉 Себя можно вылечить только один раз за игру."
	case errors.Is(err, gamemanager.ErrAlreadyInvestigated):
		return "🔍 Вы уже проверяли этого игрока."
	case errors.Is(err, gamemanager.ErrAlibiProtected):
		return "💋 За этого игрока сегодня нельзя голосовать: у него алиби."
	default:
		return "⚠️ Не удалось выполнить команду. Попробуйте ещё раз."
	}
}

func formatStatus(game models.Game) string {
	if game.Phase == models.PhaseFinished && game.Results != nil {
		return FormatResults(*game.Results)
	}
	var players []string
	for _, player := range game.Players {
		state := "☠"
		if player.Alive {
			state = "●"
		}
		players = append(players, fmt.Sprintf("    %s %s", state, DisplayName(player)))
	}
	return fmt.Sprintf("📋 Этап: %s\n\n👥 Игроки:\n\n%s", phase(game.Phase), strings.Join(players, "\n"))
}

func nightOutcomeText(game models.Game) string {
	if len(game.LastKilledIDs) == 0 {
		return "🌙 Сегодня ночью погиб: никто."
	}
	lines := make([]string, 0, len(game.LastKilledIDs))
	for _, id := range game.LastKilledIDs {
		player, ok := playerByID(game, id)
		if !ok {
			continue
		}
		lines = append(lines, playerWithRole(player))
	}
	if len(lines) == 0 {
		return "🌙💀 Сегодня ночью погиб: кто-то из игроков."
	}
	if len(lines) == 1 {
		return "🌙💀 Сегодня ночью погиб: " + lines[0]
	}
	for i, line := range lines {
		lines[i] = "    " + line
	}
	return "🌙💀 Сегодня ночью погибли:\n\n" + strings.Join(lines, "\n")
}

func votingOutcomeText(game models.Game) string {
	if game.LastLynchedID == 0 {
		return "🤷 Мирные жители не смогли определиться. Сегодня никто не повешен."
	}
	player, ok := playerByID(game, game.LastLynchedID)
	if !ok {
		return "⛓️ По итогам голосования повесили игрока."
	}
	return "⛓️ По итогам голосования повесили: " + playerWithRole(player)
}

func playerByID(game models.Game, id int64) (models.Player, bool) {
	for _, player := range game.Players {
		if player.ID == id {
			return player, true
		}
	}
	return models.Player{}, false
}

func playerWithRole(player models.Player) string {
	return fmt.Sprintf("%s (%s)", DisplayName(player), roleName(player.Role))
}

func (h *Handler) announceNight(ctx context.Context, b *bot.Bot, game models.Game, prefix string) {
	text := alivePlayersText(game) + "\n\n\n🌙 Наступила ночь."
	if prefix != "" {
		text = prefix + "\n\n" + text
	}
	common.Send(ctx, b, h.log, game.ChatID, text)
}

func alivePlayersText(game models.Game) string {
	names := make([]string, 0, len(game.Players))
	for _, player := range game.Players {
		if player.Alive {
			names = append(names, "    "+DisplayName(player))
		}
	}
	if len(names) == 0 {
		return "💀 Живых игроков не осталось."
	}
	return "💚 Живые игроки:\n\n" + strings.Join(names, "\n")
}

// FormatResults is the end-of-game summary sent to the group and players.
func FormatResults(results models.GameResults) string {
	winnerTeam := "Мирные жители"
	if results.Winner == models.TeamMafia {
		winnerTeam = "Мафия"
	}

	winners := make([]string, 0, len(results.Players))
	others := make([]string, 0, len(results.Players))
	for _, player := range results.Players {
		line := resultPlayerLine(player)
		if isResultWinner(player, results.Winner) {
			winners = append(winners, line)
		} else {
			others = append(others, line)
		}
	}

	var text strings.Builder
	text.WriteString("Игра завершена!\n")
	text.WriteString("Победили: " + winnerTeam + "\n")
	text.WriteString("\nПобедители:\n")
	text.WriteString(formatResultSection(winners))
	text.WriteString("\nОстальные участники:\n")
	text.WriteString(formatResultSection(others))
	if results.Duration > 0 {
		text.WriteString("\nИгра длилась: " + formatGameDuration(results.Duration))
	}
	return text.String()
}

func formatResultSection(lines []string) string {
	if len(lines) == 0 {
		return "    —\n"
	}
	return strings.Join(lines, "\n") + "\n"
}

func isResultWinner(player models.Player, winner models.Team) bool {
	if !player.Alive {
		return false
	}
	if winner == models.TeamMafia {
		return player.Role == models.RoleMafia
	}
	return player.Role != models.RoleMafia
}

func resultPlayerLine(player models.Player) string {
	return fmt.Sprintf("    %s - %s", DisplayName(player), ResultRoleName(player.Role))
}

// ResultRoleName is the role label used in the game-over player list.
func ResultRoleName(role models.Role) string {
	switch role {
	case models.RoleMafia:
		return "🤵🏻 Мафия"
	case models.RoleDoctor:
		return "👨🏼‍⚕️ Доктор"
	case models.RoleDetective:
		return "🕵️ Детектив"
	case models.RoleBeauty:
		return "💋 Красотка"
	default:
		return "👨🏼 Мирный житель"
	}
}

func formatGameDuration(d time.Duration) string {
	secs := int(d.Round(time.Second) / time.Second)
	minutes := secs / 60
	rem := secs % 60
	switch {
	case minutes > 0 && rem > 0:
		return fmt.Sprintf("%d мин. %d сек.", minutes, rem)
	case minutes > 0:
		return fmt.Sprintf("%d мин.", minutes)
	default:
		return fmt.Sprintf("%d сек.", rem)
	}
}

func playerFromTelegram(user telegrammodels.User) models.Player {
	return models.Player{
		ID:        user.ID,
		Username:  user.Username,
		FirstName: user.FirstName,
	}
}

// PlayerLabel is the short display name used on buttons.
func PlayerLabel(player models.Player) string {
	if name := strings.TrimSpace(player.FirstName); name != "" {
		return name
	}
	if name := strings.TrimPrefix(player.Username, "@"); name != "" {
		return name
	}
	return fmt.Sprintf("Игрок %d", player.ID)
}

// DisplayName is an HTML link to the player's Telegram profile.
func DisplayName(player models.Player) string {
	return fmt.Sprintf(`<a href="tg://user?id=%d">%s</a>`, player.ID, html.EscapeString(PlayerLabel(player)))
}

func playersWithoutDM(ctx context.Context, b *bot.Bot, log *slog.Logger, players []models.Player) []models.Player {
	missing := make([]models.Player, 0)
	for _, player := range players {
		if common.CanMessageUser(ctx, b, player.ID) {
			continue
		}
		log.Info("player has not opened private chat with bot", "user_id", player.ID)
		missing = append(missing, player)
	}
	return missing
}

func missingDMText(players []models.Player) string {
	names := make([]string, 0, len(players))
	for _, player := range players {
		names = append(names, "    "+DisplayName(player))
	}
	return "💬 Нельзя начать игру: эти игроки ещё не написали боту в личные сообщения:\n\n" +
		strings.Join(names, "\n") +
		"\n\nОткройте бота, нажмите Start и попробуйте снова."
}

func (h *Handler) replyMissingDM(ctx context.Context, b *bot.Bot, update *telegrammodels.Update, chatID int64, players []models.Player) {
	text := missingDMText(players)
	link, err := common.BotDeepLink(ctx, b)
	if err != nil {
		h.log.Error("failed to resolve bot username for deep link", sl.Err(err))
		common.Reply(ctx, b, h.log, update, text)
		return
	}
	common.SendWithKeyboard(ctx, b, h.log, chatID, text, &telegrammodels.InlineKeyboardMarkup{
		InlineKeyboard: [][]telegrammodels.InlineKeyboardButton{{{
			Text: "💬 Открыть бота",
			URL:  link,
		}}},
	})
}

func (h *Handler) sendMissingDM(ctx context.Context, b *bot.Bot, chatID int64, players []models.Player) {
	text := missingDMText(players) + "\n🚫 Лобби закрыто."
	link, err := common.BotDeepLink(ctx, b)
	if err != nil {
		h.log.Error("failed to resolve bot username for deep link", sl.Err(err))
		common.Send(ctx, b, h.log, chatID, text)
		return
	}
	common.SendWithKeyboard(ctx, b, h.log, chatID, text, &telegrammodels.InlineKeyboardMarkup{
		InlineKeyboard: [][]telegrammodels.InlineKeyboardButton{{{
			Text: "💬 Открыть бота",
			URL:  link,
		}}},
	})
}

func phase(value models.Phase) string {
	return [...]string{"🏠 лобби", "🌙 ночь", "💬 обсуждение", "🗳️ голосование", "🏁 завершена"}[value]
}
func roleName(role models.Role) string {
	switch role {
	case models.RoleMafia:
		return "🔪 мафия"
	case models.RoleDoctor:
		return "💉 доктор"
	case models.RoleDetective:
		return "🔍 детектив"
	case models.RoleBeauty:
		return "💋 красотка"
	default:
		return "😇 мирный житель"
	}
}
func canActAtNight(role models.Role) bool {
	return role == models.RoleMafia || role == models.RoleDoctor || role == models.RoleDetective || role == models.RoleBeauty
}
func nightPrompt(role models.Role) string {
	switch role {
	case models.RoleMafia:
		return "🔪 Выберите игрока, которого хотите устранить:"
	case models.RoleDoctor:
		return "💉 Выберите игрока, которого хотите вылечить:"
	case models.RoleDetective:
		return "🕵️ Выберите действие на эту ночь:"
	case models.RoleBeauty:
		return "💋 Выберите игрока, которому хотите дать алиби:"
	default:
		return "🔍 Выберите игрока, которого хотите проверить:"
	}
}

func nightChoiceAnnounce(role models.Role, actionType string) string {
	switch role {
	case models.RoleMafia:
		return "🔪 Мафия сделала свой выбор."
	case models.RoleDoctor:
		return "💉 Доктор сделал свой выбор."
	case models.RoleDetective:
		if actionType == gamemanager.NightActionShoot {
			return "🔫 Детектив решил застрелить одного из игроков."
		}
		return "🔍 Детектив решил проверить одну из ролей."
	case models.RoleBeauty:
		return "💋 Красотка сделала свой выбор."
	default:
		return ""
	}
}
