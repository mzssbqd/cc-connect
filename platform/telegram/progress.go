package telegram

import (
	"html"
	"strconv"
	"strings"
	"unicode/utf16"

	"github.com/chenhg5/cc-connect/core"
)

var _ core.ProgressCardPayloadSupport = (*Platform)(nil)

// SupportsProgressCardPayload lets the engine finalize progress independently
// of the final answer. Telegram renders completed progress as a native
// expandable quotation rather than leaving a wall of tool output in the chat.
func (p *Platform) SupportsProgressCardPayload() bool { return true }

func renderTelegramPreview(content string) (formatted, plain string) {
	payload, ok := core.ParseProgressCardPayload(content)
	if !ok {
		return core.MarkdownToSimpleHTML(content), content
	}
	i18n := core.NewI18n(core.Language(payload.Lang))
	title := i18n.T(core.MsgProgressRunning)
	quoteTag := "<blockquote>"
	switch payload.State {
	case core.ProgressCardStateCompleted:
		title = i18n.T(core.MsgProgressCompleted)
		quoteTag = "<blockquote expandable>"
	case core.ProgressCardStateFailed:
		title = i18n.T(core.MsgProgressFailed)
	}
	entries := make([]string, 0, len(payload.Items))
	for _, item := range payload.Items {
		entries = append(entries, telegramProgressEntry(item, i18n))
	}
	body := strings.Join(entries, "\n\n")
	// Telegram limits the visible text after HTML entity parsing. Count UTF-16
	// units so emoji cannot push an otherwise valid preview over that limit.
	limit := telegramMaxMessageLen - len(utf16.Encode([]rune(title+"\n\n")))
	if payload.Truncated || len(utf16.Encode([]rune(body))) > limit {
		notice := i18n.T(core.MsgProgressTruncated) + "\n"
		body = notice + telegramProgressTail(body, limit-len(utf16.Encode([]rune(notice))))
	}
	plain = title + "\n\n" + body
	formatted = "<b>" + html.EscapeString(title) + "</b>\n\n" + quoteTag + html.EscapeString(body) + "</blockquote>"
	return formatted, plain
}

func telegramProgressEntry(item core.ProgressCardEntry, i18n *core.I18n) string {
	text := item.Text
	switch item.Kind {
	case core.ProgressEntryThinking:
		return "💭 " + text
	case core.ProgressEntryToolUse:
		return strings.TrimSpace("🔧 " + item.Tool + "\n" + text)
	case core.ProgressEntryToolResult:
		lines := []string{strings.TrimSpace("🧾 " + item.Tool)}
		if item.Status != "" {
			lines = append(lines, i18n.T(core.MsgToolResultFmtStatus)+": "+item.Status)
		}
		if item.ExitCode != nil {
			lines = append(lines, i18n.T(core.MsgToolResultFmtExit)+": "+strconv.Itoa(*item.ExitCode))
		}
		if item.Success != nil {
			key := core.MsgToolResultFmtOk
			if !*item.Success {
				key = core.MsgToolResultFmtFailed
			}
			lines = append(lines, i18n.T(key))
		}
		if text != "" {
			lines = append(lines, text)
		}
		return strings.Join(lines, "\n")
	case core.ProgressEntryError:
		return "❌ " + text
	default:
		return text
	}
}

func telegramProgressTail(text string, maxUnits int) string {
	runes := []rune(text)
	start, units := len(runes), 0
	for start > 0 {
		size := 1
		if runes[start-1] > 0xffff {
			size = 2
		}
		if units+size > maxUnits {
			break
		}
		units += size
		start--
	}
	return string(runes[start:])
}
