package telegram

import (
	"context"
	"fmt"
	"html"
	"strings"
	"testing"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/chenhg5/cc-connect/core"
	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

type progressCaptureBot struct {
	*stubTelegramBot
	sent       []tgbot.SendMessageParams
	edited     []tgbot.EditMessageTextParams
	rejectHTML bool
}

func (b *progressCaptureBot) SendMessage(_ context.Context, params *tgbot.SendMessageParams) (*models.Message, error) {
	b.sent = append(b.sent, *params)
	if b.rejectHTML && params.ParseMode != "" {
		return nil, fmt.Errorf("can't parse entities")
	}
	return &models.Message{ID: len(b.sent)}, nil
}

func (b *progressCaptureBot) EditMessageText(_ context.Context, params *tgbot.EditMessageTextParams) (*models.Message, error) {
	b.edited = append(b.edited, *params)
	if b.rejectHTML && params.ParseMode != "" {
		return nil, fmt.Errorf("can't parse entities")
	}
	return &models.Message{ID: params.MessageID}, nil
}

func TestProgressCardCompletion_CollapsesProcessAndKeepsFinalAnswer(t *testing.T) {
	bot := &progressCaptureBot{stubTelegramBot: newStubTelegramBot()}
	p := &Platform{bot: bot, selfUser: &models.User{ID: 1}, progressStyle: "card"}
	ctx := context.Background()
	rc := replyContext{chatID: 123, threadID: 42}
	// Three consecutive turns must each collapse their own progress and leave
	// the separately delivered final answer readable.
	for turn := range 3 {
		items := []core.ProgressCardEntry{
			{Kind: core.ProgressEntryThinking, Text: "Inspect the project"},
			{Kind: core.ProgressEntryToolUse, Tool: "Bash", Text: "echo <ok> & done"},
			{Kind: core.ProgressEntryToolResult, Tool: "Bash", Text: "ok"},
		}
		payload := core.BuildProgressCardPayloadV2(items, false, "Codex", core.LangEnglish, core.ProgressCardStateRunning)
		handle, err := p.SendPreviewStart(ctx, rc, payload)
		if err != nil {
			t.Fatal(err)
		}
		start := bot.sent[len(bot.sent)-1]
		if strings.Contains(start.Text, "blockquote expandable") || strings.Contains(start.Text, core.ProgressCardPayloadPrefix) {
			t.Fatalf("running progress should be expanded, readable HTML: %q", start.Text)
		}
		if !strings.Contains(start.Text, "echo &lt;ok&gt; &amp; done") || start.MessageThreadID != 42 {
			t.Fatalf("progress lost literal tool input or topic: %#v", start)
		}
		completed := core.BuildProgressCardPayloadV2(items, false, "Codex", core.LangEnglish, core.ProgressCardStateCompleted)
		if err := p.UpdateMessage(ctx, handle, completed); err != nil {
			t.Fatal(err)
		}
		edit := bot.edited[len(bot.edited)-1]
		if !strings.Contains(edit.Text, "<blockquote expandable>") || !strings.Contains(edit.Text, "Inspect the project") {
			t.Fatalf("completed progress should be expandable with its details preserved: %q", edit.Text)
		}
		if edit.MessageID != handle.(*telegramPreviewHandle).messageID || edit.ParseMode != models.ParseModeHTML {
			t.Fatalf("completion should edit the same HTML progress message: %#v", edit)
		}
		answer := fmt.Sprintf("Final answer %d", turn)
		if err := p.Send(ctx, rc, answer); err != nil {
			t.Fatal(err)
		}
		if final := bot.sent[len(bot.sent)-1]; final.Text != answer {
			t.Fatalf("final answer should remain separate and fully visible: %#v", final)
		}
	}
	if len(bot.sent) != 6 || len(bot.edited) != 3 {
		t.Fatalf("want one progress message and one final answer per turn: sends=%d edits=%d", len(bot.sent), len(bot.edited))
	}
}

func TestProgressCard_FailedProcessStaysExpanded(t *testing.T) {
	bot := &progressCaptureBot{stubTelegramBot: newStubTelegramBot()}
	p := &Platform{bot: bot, selfUser: &models.User{ID: 1}}
	payload := core.BuildProgressCardPayloadV2([]core.ProgressCardEntry{{Kind: core.ProgressEntryError, Text: "permission denied"}}, false, "Codex", core.LangChinese, core.ProgressCardStateFailed)
	if _, err := p.SendPreviewStart(context.Background(), replyContext{chatID: 1}, payload); err != nil {
		t.Fatal(err)
	}
	text := bot.sent[0].Text
	if strings.Contains(text, "blockquote expandable") || !strings.Contains(text, "执行失败") || !strings.Contains(text, "permission denied") {
		t.Fatalf("failed process must keep its error visible: %q", text)
	}
}

func TestProgressCard_HTMLFallbackDoesNotLeakTransportPayload(t *testing.T) {
	bot := &progressCaptureBot{stubTelegramBot: newStubTelegramBot(), rejectHTML: true}
	p := &Platform{bot: bot, selfUser: &models.User{ID: 1}}
	payload := core.BuildProgressCardPayloadV2([]core.ProgressCardEntry{{Kind: core.ProgressEntryThinking, Text: "planning"}}, false, "Codex", core.LangEnglish, core.ProgressCardStateRunning)
	handle, err := p.SendPreviewStart(context.Background(), replyContext{chatID: 1}, payload)
	if err != nil {
		t.Fatal(err)
	}
	payload = core.BuildProgressCardPayloadV2([]core.ProgressCardEntry{{Kind: core.ProgressEntryThinking, Text: "planning"}}, false, "Codex", core.LangEnglish, core.ProgressCardStateCompleted)
	if err := p.UpdateMessage(context.Background(), handle, payload); err != nil {
		t.Fatal(err)
	}
	for _, result := range []struct{ text, mode string }{{bot.sent[1].Text, string(bot.sent[1].ParseMode)}, {bot.edited[1].Text, string(bot.edited[1].ParseMode)}} {
		if result.mode != "" || strings.Contains(result.text, core.ProgressCardPayloadPrefix) || !strings.Contains(result.text, "planning") {
			t.Fatalf("fallback should be readable plain text, never transport JSON: %#v", result)
		}
	}
}

func TestProgressCard_LongUnicodeOutputFitsTelegramLimit(t *testing.T) {
	bot := &progressCaptureBot{stubTelegramBot: newStubTelegramBot()}
	p := &Platform{bot: bot, selfUser: &models.User{ID: 1}}
	payload := core.BuildProgressCardPayloadV2([]core.ProgressCardEntry{{Kind: core.ProgressEntryToolResult, Tool: "Bash", Text: strings.Repeat("😀<&工具\n", 2000) + "latest output"}}, true, "Codex", core.LangChinese, core.ProgressCardStateCompleted)
	if _, err := p.SendPreviewStart(context.Background(), replyContext{chatID: 1}, payload); err != nil {
		t.Fatal(err)
	}
	text := bot.sent[0].Text
	plain := strings.NewReplacer("<b>", "", "</b>", "", "<blockquote expandable>", "", "</blockquote>", "").Replace(text)
	plain = html.UnescapeString(plain)
	if units := len(utf16.Encode([]rune(plain))); units > telegramMaxMessageLen {
		t.Fatalf("progress has %d UTF-16 units, exceeds %d", units, telegramMaxMessageLen)
	}
	if !utf8.ValidString(text) || !strings.Contains(text, "latest output") || strings.Contains(text, "<script>") {
		t.Fatalf("truncated progress should preserve valid Unicode and the latest output")
	}
}
