package telegram

import (
	"context"
	"errors"
	"github.com/chenhg5/cc-connect/core"
	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"testing"
)

type progressDeleteBot struct {
	*stubTelegramBot
	deleted   []tgbot.DeleteMessageParams
	deleteErr error
}

func (b *progressDeleteBot) DeleteMessage(_ context.Context, params *tgbot.DeleteMessageParams) (bool, error) {
	b.deleted = append(b.deleted, *params)
	return b.deleteErr == nil, b.deleteErr
}

func TestProgressVisibility_DefaultAndLiveUpdates(t *testing.T) {
	for _, tc := range []struct {
		name    string
		options map[string]any
		want    bool
	}{
		{"default", map[string]any{"token": "test-token"}, true},
		{"disabled", map[string]any{"token": "test-token", "hide_progress_on_finish": false}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			platform, err := New(tc.options)
			if err != nil {
				t.Fatal(err)
			}
			p := platform.(*Platform)
			if p.HideProgressOnFinish() != tc.want {
				t.Fatal("unexpected visibility default")
			}
			p.SetHideProgressOnFinish(!tc.want)
			if p.HideProgressOnFinish() == tc.want {
				t.Fatal("live update did not apply")
			}
			if _, ok := platform.(core.ProgressCardPayloadSupport); ok {
				t.Fatal("Telegram must not use the abandoned card payload renderer")
			}
		})
	}
}

func TestDeleteProgress_UsesExactChatAndMessage(t *testing.T) {
	bot := &progressDeleteBot{stubTelegramBot: newStubTelegramBot()}
	p := &Platform{bot: bot, selfUser: &models.User{ID: 1}}
	handle := &telegramPreviewHandle{chatID: 123, messageID: 42}
	if err := p.DeletePreviewMessage(context.Background(), handle); err != nil {
		t.Fatal(err)
	}
	if len(bot.deleted) != 1 || bot.deleted[0].ChatID != int64(123) || bot.deleted[0].MessageID != 42 {
		t.Fatalf("wrong deletion: %#v", bot.deleted)
	}
	bot.deleteErr = errors.New("delete rejected")
	if err := p.DeletePreviewMessage(context.Background(), handle); !errors.Is(err, bot.deleteErr) {
		t.Fatalf("lost deletion error: %v", err)
	}
}
