package bot

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"go.uber.org/zap"

	"github.com/Nakray/proxy-switcher/internal/config"
	"github.com/Nakray/proxy-switcher/internal/healthcheck"
	"github.com/Nakray/proxy-switcher/internal/metrics"
)

// Bot represents the Telegram bot
type Bot struct {
	config      *config.Config
	healthCheck *healthcheck.Checker
	metrics     *metrics.SafeCollector
	logger      *zap.Logger

	api *tgbotapi.BotAPI
	mu  sync.Mutex

	alertTicker   *time.Ticker
	alertDone     chan struct{}
	lastAlertTime time.Time

	// State for step-by-step upstream addition
	addSteps map[int64]*addStep
}

// addStep represents the current step in adding an upstream
type addStep struct {
	name     string
	typ      config.UpstreamType
	host     string
	port     int
	username string
	password string
}

// NewBot creates a new Telegram bot
func NewBot(
	cfg *config.Config,
	hc *healthcheck.Checker,
	m *metrics.SafeCollector,
	logger *zap.Logger,
) (*Bot, error) {
	if cfg.Bot.Token == "" {
		logger.Info("has not bot token")
		return nil, nil
	}

	api, err := tgbotapi.NewBotAPI(cfg.Bot.Token)
	if err != nil {
		return nil, fmt.Errorf("failed to create bot: %w", err)
	}

	bot := &Bot{
		config:      cfg,
		healthCheck: hc,
		metrics:     m,
		logger:      logger,
		api:         api,
		alertDone:   make(chan struct{}),
		addSteps:    make(map[int64]*addStep),
	}

	logger.Info("Telegram bot initialized", zap.String("username", api.Self.UserName))

	return bot, nil
}

// Start starts the bot
func (b *Bot) Start(ctx context.Context) error {
	if b == nil {
		return nil
	}

	b.logger.Info("Starting Telegram bot")

	// Start update listener
	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60

	updates := b.api.GetUpdatesChan(u)

	go b.handleUpdates(ctx, updates)
	go b.sendPeriodicAlerts(ctx)

	return nil
}

// Stop stops the bot
func (b *Bot) Stop() error {
	if b == nil {
		return nil
	}

	b.logger.Info("Stopping Telegram bot")
	if b.alertTicker != nil {
		b.alertTicker.Stop()
	}
	close(b.alertDone)

	return nil
}

func (b *Bot) handleUpdates(ctx context.Context, updates tgbotapi.UpdatesChannel) {
	for {
		select {
		case <-ctx.Done():
			return
		case update, ok := <-updates:
			if !ok {
				return
			}

			if update.Message != nil && b.isAdmin(update.Message.From.ID) {
				b.handleMessage(update.Message)
			}
			if update.CallbackQuery != nil && b.isAdmin(update.CallbackQuery.From.ID) {
				b.handleCallback(update.CallbackQuery)
			}
		}
	}
}

func (b *Bot) handleMessage(msg *tgbotapi.Message) {
	text := strings.TrimSpace(msg.Text)
	if text == "" {
		return
	}

	// Check if user is in the middle of adding upstream
	b.mu.Lock()
	_, inAddFlow := b.addSteps[msg.Chat.ID]
	b.mu.Unlock()

	if inAddFlow {
		b.handleAddStepInput(msg)
		return
	}

	command := strings.TrimPrefix(text, "/")
	parts := strings.SplitN(command, "@", 2)
	cmd := strings.ToLower(parts[0])

	b.logger.Debug("Bot command received",
		zap.String("command", cmd),
		zap.Int64("user_id", msg.From.ID))

	b.metrics.IncBotCommand(cmd)

	switch cmd {
	case "start", "help":
		b.sendHelp(msg.Chat.ID)
	case "status":
		b.sendStatus(msg.Chat.ID)
	case "upstreams":
		b.sendUpstreams(msg.Chat.ID)
	case "metrics":
		b.sendMetrics(msg.Chat.ID)
	case "add":
		b.handleAdd(msg)
	case "remove":
		b.handleRemove(msg)
	case "enable":
		b.handleEnable(msg)
	case "disable":
		b.handleDisable(msg)
	case "manage":
		b.sendManageMenu(msg.Chat.ID)
	default:
		b.sendUnknownCommand(msg.Chat.ID, cmd)
	}
}

func (b *Bot) handleCallback(callback *tgbotapi.CallbackQuery) {
	data := callback.Data
	b.logger.Debug("Callback received", zap.String("data", data))

	b.metrics.IncBotCommand("callback")

	switch {
	case data == "refresh_upstreams":
		b.sendUpstreams(callback.Message.Chat.ID)
	case data == "back_menu":
		b.sendManageMenu(callback.Message.Chat.ID)
	case data == "add_upstream":
		b.startAddUpstreamFlow(callback)
	case strings.HasPrefix(data, "enable_"):
		name := strings.TrimPrefix(data, "enable_")
		b.enableUpstreamCallback(callback, name)
	case strings.HasPrefix(data, "disable_"):
		name := strings.TrimPrefix(data, "disable_")
		b.disableUpstreamCallback(callback, name)
	case strings.HasPrefix(data, "remove_"):
		name := strings.TrimPrefix(data, "remove_")
		b.confirmRemoveCallback(callback, name)
	case strings.HasPrefix(data, "confirm_remove_"):
		name := strings.TrimPrefix(data, "confirm_remove_")
		b.removeUpstreamCallback(callback, name)
	case strings.HasPrefix(data, "cancel_remove_"):
		name := strings.TrimPrefix(data, "cancel_remove_")
		b.cancelRemoveCallback(callback, name)
	}
}

func (b *Bot) sendHelp(chatID int64) {
	text := `*Proxy Manager Bot Commands:*

*Status:*
/status - Show current proxy status
/upstreams - List all upstreams with health status
/metrics - Show Prometheus metrics summary

*Management:*
/manage - Open management menu
/add <name> <type> <host> <port> [username] [password] - Add upstream
/remove <name> - Remove upstream
/enable <name> - Enable upstream
/disable <name> - Disable upstream

*Types:* socks5, mtproto

/help - Show this help message

*Alerts:*
You will receive alerts when all upstreams are down.`

	msg := tgbotapi.NewMessage(chatID, text)
	msg.ParseMode = "Markdown"
	b.sendMessage(msg)
}

func (b *Bot) sendStatus(chatID int64) {
	healthyCount := b.healthCheck.GetHealthyCount()
	totalUpstreams := len(b.healthCheck.GetUpstreamNames())

	status := "🟢 Healthy"
	if healthyCount == 0 {
		status = "🔴 All Down"
	} else if healthyCount < totalUpstreams {
		status = "🟡 Degraded"
	}

	text := fmt.Sprintf(`*Proxy Status*

Status: %s
Healthy Upstreams: %d/%d

Use /upstreams for details.`, status, healthyCount, totalUpstreams)

	msg := tgbotapi.NewMessage(chatID, text)
	msg.ParseMode = "Markdown"
	b.sendMessage(msg)
}

func (b *Bot) sendUpstreams(chatID int64) {
	var sb strings.Builder
	sb.WriteString("*Upstreams Status*\n\n")

	statuses := b.healthCheck.GetAllStatuses()
	for _, status := range statuses {
		emoji := "🔴"
		if status.Healthy {
			emoji = "🟢"
		}
		if !status.Upstream.Enabled {
			emoji = "⚪"
		}

		latencyStr := "N/A"
		if status.Latency > 0 {
			latencyStr = fmt.Sprintf("%dms", status.Latency.Milliseconds())
		}

		enabledStr := "✓"
		if !status.Upstream.Enabled {
			enabledStr = "✗"
		}

		sb.WriteString(fmt.Sprintf("%s *%s* (%s)\n", emoji, status.Upstream.Name, status.Upstream.Type))
		sb.WriteString(fmt.Sprintf("  Host: %s:%d\n", status.Upstream.Host, status.Upstream.Port))
		sb.WriteString(fmt.Sprintf("  Latency: %s | Enabled: %s\n", latencyStr, enabledStr))
		sb.WriteString(fmt.Sprintf("  Last Check: %s\n", status.LastCheck.Format(time.RFC822)))
		sb.WriteString("\n")
	}

	if len(statuses) == 0 {
		sb.WriteString("No upstreams configured.\n")
	}

	// Add inline keyboard for management
	keyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("🔄 Refresh", "refresh_upstreams"),
			tgbotapi.NewInlineKeyboardButtonData("🔙 Menu", "back_menu"),
		),
	)

	msg := tgbotapi.NewMessage(chatID, sb.String())
	msg.ParseMode = "Markdown"
	msg.ReplyMarkup = keyboard
	b.sendMessage(msg)
}

func (b *Bot) sendMetrics(chatID int64) {
	summary := b.metrics.GetSummary()

	var sb strings.Builder
	sb.WriteString("*Metrics Summary*\n\n")

	sb.WriteString(fmt.Sprintf("Active Connections: %v\n", summary["active_connections"]))
	sb.WriteString(fmt.Sprintf("Total Connections: %v\n", summary["total_connections"]))
	sb.WriteString(fmt.Sprintf("Bytes Transferred: %v\n", summary["bytes_transferred"]))
	sb.WriteString("\n")
	sb.WriteString(fmt.Sprintf("_%v_\n", summary["note"]))

	msg := tgbotapi.NewMessage(chatID, sb.String())
	msg.ParseMode = "Markdown"
	b.sendMessage(msg)
}

func (b *Bot) sendManageMenu(chatID int64) {
	text := "*Proxy Management Menu*\n\nSelect an action:"

	// Build inline keyboard with upstreams
	statuses := b.healthCheck.GetAllStatuses()
	var rows [][]tgbotapi.InlineKeyboardButton

	// Add rows for each upstream
	for _, status := range statuses {
		var row []tgbotapi.InlineKeyboardButton

		// Enable/Disable button
		if status.Upstream.Enabled {
			row = append(row, tgbotapi.NewInlineKeyboardButtonData("⏸️ "+status.Upstream.Name, "disable_"+status.Upstream.Name))
		} else {
			row = append(row, tgbotapi.NewInlineKeyboardButtonData("▶️ "+status.Upstream.Name, "enable_"+status.Upstream.Name))
		}

		// Remove button
		row = append(row, tgbotapi.NewInlineKeyboardButtonData("🗑️", "remove_"+status.Upstream.Name))

		rows = append(rows, row)
	}

	// Add action buttons
	var actionRow []tgbotapi.InlineKeyboardButton
	actionRow = append(actionRow, tgbotapi.NewInlineKeyboardButtonData("🔄 Refresh", "refresh_upstreams"))
	actionRow = append(actionRow, tgbotapi.NewInlineKeyboardButtonData("➕ Add", "add_upstream"))
	rows = append(rows, actionRow)

	keyboard := tgbotapi.NewInlineKeyboardMarkup(rows...)

	msg := tgbotapi.NewMessage(chatID, text)
	msg.ParseMode = "Markdown"
	msg.ReplyMarkup = keyboard
	b.sendMessage(msg)
}

func (b *Bot) handleAdd(msg *tgbotapi.Message) {
	args := strings.Fields(msg.Text)
	if len(args) < 5 {
		b.sendMessage(tgbotapi.NewMessage(msg.Chat.ID,
			"Usage: /add <name> <type> <host> <port> [username] [password]\n\nExample:\n/add myproxy socks5 proxy.com 1080 user pass"))
		return
	}

	name := args[1]
	proxyType := config.UpstreamType(args[2])
	host := args[3]
	port, err := strconv.Atoi(args[4])
	if err != nil {
		b.sendMessage(tgbotapi.NewMessage(msg.Chat.ID, "Invalid port number"))
		return
	}

	if proxyType != config.UpstreamTypeSOCKS5 && proxyType != config.UpstreamTypeMTProto {
		b.sendMessage(tgbotapi.NewMessage(msg.Chat.ID, "Invalid type. Use 'socks5' or 'mtproto'"))
		return
	}

	upstream := config.Upstream{
		Name:    name,
		Type:    proxyType,
		Host:    host,
		Port:    port,
		Enabled: true,
	}

	if len(args) >= 6 {
		upstream.Username = args[5]
	}
	if len(args) >= 7 {
		upstream.Password = args[6]
	}

	if err := b.healthCheck.AddUpstream(upstream); err != nil {
		b.sendMessage(tgbotapi.NewMessage(msg.Chat.ID, fmt.Sprintf("Error: %v", err)))
		return
	}

	b.sendMessage(tgbotapi.NewMessage(msg.Chat.ID, fmt.Sprintf("✅ Upstream *%s* added successfully!", name)))
}

func (b *Bot) handleRemove(msg *tgbotapi.Message) {
	args := strings.Fields(msg.Text)
	if len(args) < 2 {
		b.sendMessage(tgbotapi.NewMessage(msg.Chat.ID, "Usage: /remove <name>"))
		return
	}

	name := args[1]
	if err := b.healthCheck.RemoveUpstream(name); err != nil {
		b.sendMessage(tgbotapi.NewMessage(msg.Chat.ID, fmt.Sprintf("Error: %v", err)))
		return
	}

	b.sendMessage(tgbotapi.NewMessage(msg.Chat.ID, fmt.Sprintf("🗑️ Upstream *%s* removed!", name)))
}

func (b *Bot) handleEnable(msg *tgbotapi.Message) {
	args := strings.Fields(msg.Text)
	if len(args) < 2 {
		b.sendMessage(tgbotapi.NewMessage(msg.Chat.ID, "Usage: /enable <name>"))
		return
	}

	name := args[1]
	if err := b.healthCheck.EnableUpstream(name); err != nil {
		b.sendMessage(tgbotapi.NewMessage(msg.Chat.ID, fmt.Sprintf("Error: %v", err)))
		return
	}

	b.sendMessage(tgbotapi.NewMessage(msg.Chat.ID, fmt.Sprintf("✅ Upstream *%s* enabled!", name)))
}

func (b *Bot) handleDisable(msg *tgbotapi.Message) {
	args := strings.Fields(msg.Text)
	if len(args) < 2 {
		b.sendMessage(tgbotapi.NewMessage(msg.Chat.ID, "Usage: /disable <name>"))
		return
	}

	name := args[1]
	if err := b.healthCheck.DisableUpstream(name); err != nil {
		b.sendMessage(tgbotapi.NewMessage(msg.Chat.ID, fmt.Sprintf("Error: %v", err)))
		return
	}

	b.sendMessage(tgbotapi.NewMessage(msg.Chat.ID, fmt.Sprintf("⏸️ Upstream *%s* disabled!", name)))
}

func (b *Bot) sendUnknownCommand(chatID int64, cmd string) {
	text := fmt.Sprintf("Unknown command: /%s\nUse /help for available commands.", cmd)
	msg := tgbotapi.NewMessage(chatID, text)
	b.sendMessage(msg)
}

func (b *Bot) sendMessage(msg tgbotapi.MessageConfig) {
	b.mu.Lock()
	defer b.mu.Unlock()

	result, err := b.api.Send(msg)
	if err != nil {
		b.logger.Error("Failed to send message", zap.Error(err))
		return
	}

	b.logger.Debug("Message sent",
		zap.Int64("chat_id", result.Chat.ID),
		zap.Int("message_id", result.MessageID))

	b.metrics.IncBotMessagesSent()
}

func (b *Bot) sendPeriodicAlerts(ctx context.Context) {
	b.alertTicker = time.NewTicker(b.config.Bot.AlertInterval)
	defer b.alertTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-b.alertDone:
			return
		case <-b.alertTicker.C:
			b.checkAndSendAlert()
		}
	}
}

func (b *Bot) checkAndSendAlert() {
	if !b.healthCheck.AreAllUpstreamsDown() {
		return
	}

	// Rate limit alerts
	b.mu.Lock()
	if time.Since(b.lastAlertTime) < b.config.Bot.AlertInterval {
		b.mu.Unlock()
		return
	}
	b.lastAlertTime = time.Now()
	b.mu.Unlock()

	text := `🚨 *ALERT: All Upstreams Down!*

All upstream proxies are currently unavailable.
Please check the server and upstream status.`

	msg := tgbotapi.NewMessage(0, text)
	msg.ParseMode = "Markdown"

	for _, chatID := range b.config.Bot.AdminChatIDs {
		msg.ChatID = chatID
		b.sendMessage(msg)
	}

	b.logger.Warn("All upstreams down alert sent")
}

// Callback handlers

func (b *Bot) enableUpstreamCallback(callback *tgbotapi.CallbackQuery, name string) {
	if err := b.healthCheck.EnableUpstream(name); err != nil {
		b.answerCallback(callback, fmt.Sprintf("Error: %v", err))
		return
	}
	b.answerCallback(callback, fmt.Sprintf("✅ %s enabled", name))
	b.sendManageMenu(callback.Message.Chat.ID)
}

func (b *Bot) disableUpstreamCallback(callback *tgbotapi.CallbackQuery, name string) {
	if err := b.healthCheck.DisableUpstream(name); err != nil {
		b.answerCallback(callback, fmt.Sprintf("Error: %v", err))
		return
	}
	b.answerCallback(callback, fmt.Sprintf("⏸️ %s disabled", name))
	b.sendManageMenu(callback.Message.Chat.ID)
}

func (b *Bot) confirmRemoveCallback(callback *tgbotapi.CallbackQuery, name string) {
	keyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("✅ Confirm", "confirm_remove_"+name),
			tgbotapi.NewInlineKeyboardButtonData("❌ Cancel", "cancel_remove_"+name),
		),
	)

	msg := tgbotapi.NewEditMessageText(callback.Message.Chat.ID, callback.Message.MessageID,
		fmt.Sprintf("⚠️ Are you sure you want to remove *%s*?", name))
	msg.ParseMode = "Markdown"
	msg.ReplyMarkup = &keyboard

	b.api.Send(msg)
	b.answerCallback(callback, "")
}

func (b *Bot) removeUpstreamCallback(callback *tgbotapi.CallbackQuery, name string) {
	if err := b.healthCheck.RemoveUpstream(name); err != nil {
		b.answerCallback(callback, fmt.Sprintf("Error: %v", err))
		return
	}
	b.answerCallback(callback, fmt.Sprintf("🗑️ %s removed", name))
	b.sendManageMenu(callback.Message.Chat.ID)
}

func (b *Bot) cancelRemoveCallback(callback *tgbotapi.CallbackQuery, name string) {
	b.answerCallback(callback, "Cancelled")
	b.sendManageMenu(callback.Message.Chat.ID)
}

func (b *Bot) answerCallback(callback *tgbotapi.CallbackQuery, text string) {
	answer := tgbotapi.NewCallback(callback.ID, text)
	if _, err := b.api.Request(answer); err != nil {
		b.logger.Error("Failed to answer callback", zap.Error(err))
	}
}

func (b *Bot) isAdmin(userID int64) bool {
	for _, adminID := range b.config.Bot.AdminChatIDs {
		if userID == adminID {
			return true
		}
	}
	return false
}

// SendAllUpstreamsDownAlert sends an immediate alert about all upstreams being down
func (b *Bot) SendAllUpstreamsDownAlert() {
	if b == nil {
		return
	}

	text := `🚨 *CRITICAL: All Upstreams Down!*

All upstream proxies are currently unavailable.
Immediate attention required!`

	msg := tgbotapi.NewMessage(0, text)
	msg.ParseMode = "Markdown"

	for _, chatID := range b.config.Bot.AdminChatIDs {
		msg.ChatID = chatID
		b.sendMessage(msg)
	}

	b.logger.Warn("All upstreams down alert sent")
}

// SendUpstreamRecoveredAlert sends an alert when an upstream recovers
func (b *Bot) SendUpstreamRecoveredAlert(upstreamName, upstreamType string) {
	if b == nil {
		return
	}

	text := fmt.Sprintf("✅ *Upstream Recovered*\n\n%s (%s) is now healthy.", upstreamName, upstreamType)

	msg := tgbotapi.NewMessage(0, text)
	msg.ParseMode = "Markdown"

	for _, chatID := range b.config.Bot.AdminChatIDs {
		msg.ChatID = chatID
		b.sendMessage(msg)
	}
}

// Step-by-step upstream addition flow

func (b *Bot) startAddUpstreamFlow(callback *tgbotapi.CallbackQuery) {
	b.answerCallback(callback, "")
	
	b.mu.Lock()
	b.addSteps[callback.Message.Chat.ID] = &addStep{}
	b.mu.Unlock()

	msg := tgbotapi.NewMessage(callback.Message.Chat.ID, 
		"➕ *Adding New Upstream*\n\n"+
		"Let's configure your upstream step by step.\n\n"+
		"Step 1/6: Enter the upstream *name* (e.g., `my-proxy-1`):")
	msg.ParseMode = "Markdown"
	b.sendMessage(msg)
}

func (b *Bot) handleAddStepInput(msg *tgbotapi.Message) {
	b.mu.Lock()
	step, exists := b.addSteps[msg.Chat.ID]
	b.mu.Unlock()

	if !exists {
		return
	}

	text := strings.TrimSpace(msg.Text)

	if step.name == "" {
		step.name = text
		b.sendMessage(tgbotapi.NewMessage(msg.Chat.ID,
			"Step 2/6: Enter the upstream *type*:\n"+
			"- `socks5` - SOCKS5 proxy\n"+
			"- `mtproto` - MTProto proxy\n\n"+
			"Send: `socks5` or `mtproto`"))
		return
	}

	if step.typ == "" {
		typ := config.UpstreamType(strings.ToLower(text))
		if typ != config.UpstreamTypeSOCKS5 && typ != config.UpstreamTypeMTProto {
			b.sendMessage(tgbotapi.NewMessage(msg.Chat.ID,
				"❌ Invalid type. Please send `socks5` or `mtproto`"))
			return
		}
		step.typ = typ
		b.sendMessage(tgbotapi.NewMessage(msg.Chat.ID,
			"Step 3/6: Enter the upstream *host* (e.g., `proxy.example.com` or `192.168.1.1`):"))
		return
	}

	if step.host == "" {
		step.host = text
		b.sendMessage(tgbotapi.NewMessage(msg.Chat.ID,
			"Step 4/6: Enter the upstream *port* (e.g., `1080`):"))
		return
	}

	if step.port == 0 {
		port, err := strconv.Atoi(text)
		if err != nil || port <= 0 || port > 65535 {
			b.sendMessage(tgbotapi.NewMessage(msg.Chat.ID,
				"❌ Invalid port. Please send a number between 1 and 65535"))
			return
		}
		step.port = port
		b.sendMessage(tgbotapi.NewMessage(msg.Chat.ID,
			"Step 5/6: Enter *username* (optional, send `-` to skip):"))
		return
	}

	if step.username == "" {
		if text != "-" {
			step.username = text
		}
		b.sendMessage(tgbotapi.NewMessage(msg.Chat.ID,
			"Step 6/6: Enter *password* (optional, send `-` to skip):"))
		return
	}

	if step.password == "" {
		if text != "-" {
			step.password = text
		}

		// Create upstream
		upstream := config.Upstream{
			Name:     step.name,
			Type:     step.typ,
			Host:     step.host,
			Port:     step.port,
			Username: step.username,
			Password: step.password,
			Enabled:  true,
		}

		if err := b.healthCheck.AddUpstream(upstream); err != nil {
			b.sendMessage(tgbotapi.NewMessage(msg.Chat.ID,
				fmt.Sprintf("❌ Error adding upstream: %v", err)))
			b.mu.Lock()
			delete(b.addSteps, msg.Chat.ID)
			b.mu.Unlock()
			return
		}

		authStr := ""
		if step.username != "" && step.password != "" {
			authStr = fmt.Sprintf("\nAuth: %s:***", step.username)
		}

		b.sendMessage(tgbotapi.NewMessage(msg.Chat.ID,
			fmt.Sprintf("✅ Upstream *%s* added successfully!\n\nHost: %s:%d\nType: %s%s",
				step.name, step.host, step.port, step.typ, authStr)))

		// Cleanup
		b.mu.Lock()
		delete(b.addSteps, msg.Chat.ID)
		b.mu.Unlock()
		return
	}
}
