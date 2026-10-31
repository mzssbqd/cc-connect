package telegram

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/core"
	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

type panelBot struct {
	*stubTelegramBot
	sends []*tgbot.SendMessageParams
	edits []*tgbot.EditMessageTextParams
	fail  bool
}

func (b *panelBot) SendMessage(_ context.Context, p *tgbot.SendMessageParams) (*models.Message, error) {
	b.sends = append(b.sends, p)
	return &models.Message{ID: 100 + len(b.sends)}, nil
}
func (b *panelBot) EditMessageText(_ context.Context, p *tgbot.EditMessageTextParams) (*models.Message, error) {
	b.edits = append(b.edits, p)
	if b.fail {
		return nil, errors.New("network unavailable")
	}
	return &models.Message{ID: p.MessageID}, nil
}
func TestPanelOpaqueCallbacksEditInPlaceAndEnforceOwner(t *testing.T) {
	bot := &panelBot{stubTelegramBot: newStubTelegramBot()}
	p := &Platform{bot: bot, allowFrom: "*"}
	ctx := context.Background()
	msg := &core.Message{SessionKey: "test:key", UserID: "42", ReplyCtx: replyContext{chatID: 42}}
	action := "@workspace model " + strings.Repeat("long-name", 20)
	card := core.NewCard().Title("Panel", "blue").Buttons(core.DefaultBtn("Model", action)).Build()
	if err := p.SendPanel(ctx, msg, card, nil, "Keyboard", "Expired"); err != nil {
		t.Fatal(err)
	}
	markup := bot.sends[len(bot.sends)-1].ReplyMarkup.(*models.InlineKeyboardMarkup)
	token := markup.InlineKeyboard[0][0].CallbackData
	if len(token) > 64 || strings.Contains(token, "long-name") {
		t.Fatal(token)
	}
	var received *core.Message
	p.handler = func(_ core.Platform, m *core.Message) { received = m }
	rc := replyContext{chatID: 42, messageID: 102}
	cb := &models.CallbackQuery{Data: token}
	p.handlePanelCallback(ctx, bot, cb, msg.SessionKey, "chat", "43", "", "", rc)
	if received != nil {
		t.Fatal("another user used panel")
	}
	wrongTopic := rc
	wrongTopic.threadID = 8
	p.handlePanelCallback(ctx, bot, cb, msg.SessionKey, "chat", "42", "", "", wrongTopic)
	if received != nil {
		t.Fatal("another topic used panel")
	}
	p.handlePanelCallback(ctx, bot, cb, msg.SessionKey, "chat", "42", "", "", rc)
	if received == nil || received.Content != "/panel "+action {
		t.Fatal(received)
	}
	before := len(bot.sends)
	if err := p.SendPanel(ctx, received, card, nil, "Keyboard", "Expired"); err != nil {
		t.Fatal(err)
	}
	if len(bot.sends) != before || len(bot.edits) != 1 {
		t.Fatal("navigation sent new message")
	}
	if _, exists := p.panelTokens[token]; exists {
		t.Fatal("old page token retained")
	}
	newToken := bot.edits[0].ReplyMarkup.(*models.InlineKeyboardMarkup).InlineKeyboard[0][0].CallbackData
	bot.fail = true
	if err := p.SendPanel(ctx, received, card, nil, "Keyboard", "Expired"); err == nil {
		t.Fatal("expected edit error")
	}
	if _, exists := p.panelTokens[newToken]; !exists {
		t.Fatal("failed edit revoked visible buttons")
	}
}
