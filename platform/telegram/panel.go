package telegram

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/chenhg5/cc-connect/core"
	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

type panelToken struct {
	action, sessionKey, userID string
	chatID                     int64
	threadID, messageID        int
	expires                    time.Time
}

// SendPanel uses independent messages: progress cleanup never owns these IDs.
func (p *Platform) SendPanel(ctx context.Context, msg *core.Message, card *core.Card, shortcuts [][]core.ButtonOption, keyboardText, expiredText string) error {
	rctx, ok := msg.ReplyCtx.(replyContext)
	if !ok {
		return fmt.Errorf("telegram: invalid panel reply context")
	}
	bot, err := p.connectedBot("panel")
	if err != nil {
		return err
	}
	p.panelMu.Lock()
	defer p.panelMu.Unlock()
	if p.panelTokens == nil {
		p.panelTokens = make(map[string]panelToken)
	}
	if p.panelKeyboards == nil {
		p.panelKeyboards = make(map[string]map[string]string)
	}
	p.panelExpiredText = expiredText
	for token, entry := range p.panelTokens {
		if time.Now().After(entry.expires) {
			delete(p.panelTokens, token)
		}
	}
	// Bounded cache; evicted keyboards safely ask the user to reopen the panel.
	if len(p.panelTokens) > 4096 {
		clear(p.panelTokens)
	}
	owner := msg.SessionKey + ":" + msg.UserID
	if !rctx.panel && rctx.chatID > 0 { // Reply keyboards are private-chat shortcuts only.
		actions := make(map[string]string)
		var keyboard [][]models.KeyboardButton
		for _, row := range shortcuts {
			var buttons []models.KeyboardButton
			for _, b := range row {
				buttons = append(buttons, models.KeyboardButton{Text: b.Text})
				actions[b.Text] = b.Data
			}
			keyboard = append(keyboard, buttons)
		}
		if p.panelKeyboards[owner] == nil {
			_, err = bot.SendMessage(ctx, &tgbot.SendMessageParams{ChatID: rctx.chatID, MessageThreadID: rctx.threadID, Text: keyboardText, ReplyMarkup: &models.ReplyKeyboardMarkup{Keyboard: keyboard, ResizeKeyboard: true}})
			if err != nil {
				return fmt.Errorf("telegram: send panel shortcuts: %w", err)
			}
		}
		p.panelKeyboards[owner] = actions
	}
	var text strings.Builder
	if card.Header != nil {
		text.WriteString(card.Header.Title + "\n\n")
	}
	var keyboard [][]models.InlineKeyboardButton
	pending := make(map[string]panelToken)
	for _, element := range card.Elements {
		switch el := element.(type) {
		case core.CardMarkdown:
			text.WriteString(el.Content + "\n\n")
		case core.CardNote:
			text.WriteString(el.Text + "\n\n")
		case core.CardActions:
			var row []models.InlineKeyboardButton
			for _, b := range el.Buttons {
				var random [16]byte
				if _, err := rand.Read(random[:]); err != nil {
					return err
				}
				token := "panel:" + hex.EncodeToString(random[:])
				pending[token] = panelToken{action: b.Value, sessionKey: msg.SessionKey, userID: msg.UserID, chatID: rctx.chatID, threadID: rctx.threadID, expires: time.Now().Add(24 * time.Hour)}
				row = append(row, models.InlineKeyboardButton{Text: b.Text, CallbackData: token})
			}
			if len(row) > 0 {
				keyboard = append(keyboard, row)
			}
		}
	}
	markup := &models.InlineKeyboardMarkup{InlineKeyboard: keyboard}
	body := strings.TrimSpace(text.String())
	runes := []rune(body)
	if len(runes) > 3500 {
		body = string(runes[:3500]) + "…"
	}
	messageID := rctx.messageID
	if rctx.panel {
		_, err = bot.EditMessageText(ctx, &tgbot.EditMessageTextParams{ChatID: rctx.chatID, MessageID: messageID, Text: body, ReplyMarkup: markup})
	} else {
		var sent *models.Message
		sent, err = bot.SendMessage(ctx, &tgbot.SendMessageParams{ChatID: rctx.chatID, MessageThreadID: rctx.threadID, Text: body, ReplyMarkup: markup})
		if err == nil {
			messageID = sent.ID
		}
	}
	if err != nil {
		return fmt.Errorf("telegram: render panel: %w", err)
	}
	// Only revoke the previous page after the new page was accepted by Telegram.
	for token, entry := range p.panelTokens {
		if entry.chatID == rctx.chatID && entry.messageID == messageID {
			delete(p.panelTokens, token)
		}
	}
	for token, entry := range pending {
		entry.messageID = messageID
		p.panelTokens[token] = entry
	}
	return nil
}

func (p *Platform) handlePanelCallback(ctx context.Context, bot telegramBot, cb *models.CallbackQuery, sessionKey, channelKey, userID, userName, chatName string, rctx replyContext) {
	p.panelDispatchMu.Lock()
	defer p.panelDispatchMu.Unlock()
	p.panelMu.Lock()
	entry, ok := p.panelTokens[cb.Data]
	expiredText := p.panelExpiredText
	valid := ok && time.Now().Before(entry.expires) && entry.chatID == rctx.chatID && entry.threadID == rctx.threadID && entry.messageID == rctx.messageID && entry.userID == userID && entry.sessionKey == sessionKey
	p.panelMu.Unlock()
	if !valid {
		if expiredText == "" {
			expiredText = core.NewI18n(core.LangEnglish).T(core.MsgPanelExpired)
		}
		// The initial callback acknowledgement already cleared the spinner. Send a
		// separate notice rather than mutating another user's panel.
		if _, err := bot.SendMessage(ctx, &tgbot.SendMessageParams{ChatID: rctx.chatID, MessageThreadID: rctx.threadID, Text: expiredText}); err != nil {
			slog.Warn("telegram: panel expiry notice failed", "error", err)
			return
		}
		return
	}
	rctx.panel = true
	handler := p.messageHandler()
	if handler == nil {
		return
	}
	handler(p, &core.Message{SessionKey: sessionKey, Platform: p.Name(), UserID: userID, UserName: userName, ChatName: chatName, Content: "/panel " + entry.action, ChannelKey: channelKey, ReplyCtx: rctx})
}
