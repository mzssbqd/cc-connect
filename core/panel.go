package core

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"
)

func (e *Engine) SetProgressVisibilitySaveFunc(fn func(string, bool) error) {
	e.progressVisibilitySaveFunc = fn
}

// panelScope invalidates buttons when their workspace changes. IDs and model
// names are carried verbatim inside the adapter's opaque callback tokens.
func panelScope(key string, agent Agent) string {
	dir := ""
	if wd, ok := agent.(interface{ GetWorkDir() string }); ok {
		dir = wd.GetWorkDir()
	}
	return fmt.Sprintf("%x", sha256.Sum256([]byte(key+"\x00"+dir)))[:16]
}

func panelCommand(action string) string {
	switch action {
	case "efforts", "effort":
		return "reasoning"
	case "models", "model":
		return "model"
	case "sessions", "session":
		return "list"
	case "switch":
		return "switch"
	case "new", "create":
		return "new"
	case "settings", "hide":
		return "config"
	case "stop":
		return "stop"
	default:
		return "status"
	}
}

func (e *Engine) panelPermission(userID, action string) string {
	cmd := panelCommand(action)
	e.userRolesMu.RLock()
	disabled, roles := e.disabledCmds, e.userRoles
	e.userRolesMu.RUnlock()
	if roles != nil {
		if role := roles.ResolveRole(userID); role != nil {
			disabled = role.DisabledCmds
		}
	}
	if disabled[cmd] {
		return e.i18n.Tf(MsgCommandDisabled, "/"+cmd)
	}
	if isPrivilegedCommandInvocation(cmd, nil) && !e.isAdmin(userID) {
		return e.i18n.Tf(MsgAdminRequired, "/"+cmd)
	}
	return ""
}

func (e *Engine) cmdPanel(p Platform, msg *Message, args []string) {
	// Serialize panel mutations and renders, including double-clicks on new/switch.
	e.panelMu.Lock()
	defer e.panelMu.Unlock()
	agent, sessions, ik, err := e.commandContext(p, msg)
	if err != nil {
		e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgWsResolutionError, err))
		return
	}
	scope := panelScope(ik, agent)
	action, value, notice := "home", "", ""
	if len(args) > 0 && strings.HasPrefix(args[0], "@") {
		if args[0] != "@"+scope {
			notice = e.i18n.T(MsgPanelExpired)
			args = nil
		} else {
			args = args[1:]
		}
	}
	if len(args) > 0 {
		action = args[0]
		value = strings.Join(args[1:], " ")
	}
	var card *Card
	if denied := e.panelPermission(msg.UserID, action); denied != "" {
		card = NewCard().Markdown(denied).Build()
	} else {
		active := sessions.GetOrCreateActive(msg.SessionKey)
		mutating := action == "effort" || action == "model" || action == "switch" || action == "create"
		if mutating && active.Busy() {
			card = NewCard().Markdown(e.i18n.T(MsgPanelBusy)).Buttons(DefaultBtn(e.i18n.T(MsgPanelStop), "stop")).Build()
		} else {
			switch action {
			case "efforts", "effort":
				if action == "effort" {
					notice = e.panelEffortAction(agent, ik, msg.SessionKey, value)
				}
				card = e.panelEfforts(agent)
			case "models":
				card = e.panelModels(agent, value)
			case "model":
				notice = e.panelModelAction(agent, sessions, ik, msg.SessionKey, value)
				card = e.panelModels(agent, "1")
			case "sessions":
				card = e.panelSessions(agent, sessions, msg.SessionKey, value)
			case "session", "switch":
				card, notice = e.panelSessionAction(agent, sessions, ik, msg.SessionKey, action, value)
			case "new":
				card = NewCard().Markdown(e.i18n.T(MsgPanelNewConfirm)).Buttons(PrimaryBtn(e.i18n.T(MsgPanelNew), "create"), DefaultBtn(e.i18n.T(MsgCardBack), "home")).Build()
			case "create":
				e.cleanupInteractiveState(ik)
				sessions.NewSession(msg.SessionKey, "")
				sessions.Save()
				notice = e.i18n.T(MsgNewSessionCreated)
			case "stop":
				e.stopInteractiveSession(ik, nil, nil)
			case "settings", "hide":
				card, notice = e.panelSettings(p, action, value)
			}
		}
	}
	if card == nil {
		card = e.panelHome(agent, sessions, msg.SessionKey)
	}
	e.sendPanel(p, msg, card, scope, notice)
}

func (e *Engine) sendPanel(p Platform, msg *Message, card *Card, scope, notice string) {
	if notice != "" {
		card.Elements = append([]CardElement{CardMarkdown{Content: notice}, CardDivider{}}, card.Elements...)
	}
	card.Elements = append(card.Elements, CardActions{Buttons: []CardButton{DefaultBtn(e.i18n.T(MsgPanelHome), "home")}})
	for i, el := range card.Elements {
		if row, ok := el.(CardActions); ok {
			for j := range row.Buttons {
				row.Buttons[j].Value = "@" + scope + " " + row.Buttons[j].Value
			}
			card.Elements[i] = row
		}
	}
	shortcuts := panelShortcuts(e.i18n)
	if sender, ok := p.(PanelSender); ok {
		if err := sender.SendPanel(e.ctx, msg, card, shortcuts, e.i18n.T(MsgPanelKeyboard), e.i18n.T(MsgPanelExpired)); err != nil {
			slog.Warn("panel: send failed", "error", err)
			e.reply(p, msg.ReplyCtx, e.i18n.T(MsgPanelSendFailed))
		}
		return
	}
	e.reply(p, msg.ReplyCtx, card.RenderText())
}

func (e *Engine) panelHome(agent Agent, sessions *SessionManager, key string) *Card {
	s := sessions.GetOrCreateActive(key)
	name := sessions.GetSessionName(s.GetAgentSessionID())
	if name == "" {
		name = s.GetName()
	}
	if name == "" && s.GetAgentSessionID() != "" {
		name = e.currentSessionDisplayName(agent, sessions, s.GetAgentSessionID())
	}
	if name == "" {
		name = e.i18n.T(MsgSessionNotStarted)
	}
	model := e.i18n.T(MsgModelDefault)
	if ms, ok := agent.(ModelSwitcher); ok && ms.GetModel() != "" {
		model = ms.GetModel()
	}
	if rs, ok := agent.(ReasoningEffortSwitcher); ok {
		effort := rs.GetReasoningEffort()
		if effort == "" {
			effort = e.i18n.T(MsgModelDefault)
		}
		model += " · " + effort
	}
	dir := "—"
	if wd, ok := agent.(interface{ GetWorkDir() string }); ok {
		dir = wd.GetWorkDir()
	}
	status := "🟢 " + e.i18n.T(MsgPanelIdle)
	if s.Busy() {
		status = "⏳ " + e.i18n.T(MsgPanelRunning)
	}
	c := NewCard().Title("🎛️ "+e.i18n.T(MsgPanelHome), "blue").Markdown(e.i18n.Tf(MsgPanelSummary, e.name, dir, name, model, status)).
		Buttons(DefaultBtn(e.i18n.T(MsgPanelModels), "models"), DefaultBtn(e.i18n.T(MsgPanelSessions), "sessions")).
		Buttons(DefaultBtn(e.i18n.T(MsgPanelNew), "new"), DefaultBtn(e.i18n.T(MsgPanelSettings), "settings"))
	if s.Busy() {
		c.Buttons(DefaultBtn(e.i18n.T(MsgPanelStop), "stop"))
	}
	return c.Build()
}

func panelPage(value string, total int) (int, int, int) {
	page, _ := strconv.Atoi(value)
	if page < 1 {
		page = 1
	}
	pages := (total + 5) / 6
	if pages < 1 {
		pages = 1
	}
	if page > pages {
		page = pages
	}
	start := (page - 1) * 6
	end := start + 6
	if end > total {
		end = total
	}
	return page, start, end
}
func (e *Engine) panelPages(c *CardBuilder, verb string, page, end, total int) {
	var buttons []CardButton
	if page > 1 {
		buttons = append(buttons, DefaultBtn(e.i18n.T(MsgCardPrev), fmt.Sprintf("%s %d", verb, page-1)))
	}
	if end < total {
		buttons = append(buttons, DefaultBtn(e.i18n.T(MsgCardNext), fmt.Sprintf("%s %d", verb, page+1)))
	}
	c.Buttons(buttons...)
}
func (e *Engine) panelModels(agent Agent, value string) *Card {
	c := NewCard().Title(e.i18n.T(MsgPanelModels), "indigo")
	ms, ok := agent.(ModelSwitcher)
	if !ok {
		return c.Markdown(e.i18n.T(MsgModelNotSupported)).Build()
	}
	ctx, cancel := context.WithTimeout(e.ctx, 3*time.Second)
	defer cancel()
	models := ms.AvailableModels(ctx)
	page, start, end := panelPage(value, len(models))
	c.Markdown(e.i18n.Tf(MsgModelCurrent, ms.GetModel())).Note(e.i18n.T(MsgPanelModelScope))
	for _, m := range models[start:end] {
		label := m.Name
		if m.Name == ms.GetModel() {
			label = "✓ " + label
		}
		c.Buttons(DefaultBtn(label, "model "+m.Name))
	}
	e.panelPages(c, "models", page, end, len(models))
	if rs, ok := agent.(ReasoningEffortSwitcher); ok && len(rs.AvailableReasoningEfforts()) > 0 {
		c.Markdown(e.reasoningCurrentText(rs))
		c.Buttons(DefaultBtn(e.i18n.T(MsgPanelReasoning), "efforts"))
	}
	return c.Build()
}
func (e *Engine) panelSessions(agent Agent, sessions *SessionManager, key, value string) *Card {
	c := NewCard().Title(e.i18n.T(MsgPanelSessions), "turquoise")
	list, err := agent.ListSessions(e.ctx)
	if err != nil {
		return c.Markdown(e.i18n.Tf(MsgListError, err)).Build()
	}
	list = e.applySessionFilter(list, sessions, key)
	page, start, end := panelPage(value, len(list))
	active := sessions.GetOrCreateActive(key).GetAgentSessionID()
	if len(list) == 0 {
		c.Markdown(e.i18n.T(MsgListEmpty))
	}
	for _, item := range list[start:end] {
		label := e.deleteSessionDisplayName(sessions, &item)
		r := []rune(label)
		if len(r) > 35 {
			label = string(r[:35]) + "…"
		}
		if item.ID == active {
			label = "✓ " + label
		}
		c.Buttons(DefaultBtn(label+" · "+item.ModifiedAt.Format("01-02 15:04"), "session "+item.ID))
	}
	e.panelPages(c, "sessions", page, end, len(list))
	c.Buttons(DefaultBtn(e.i18n.T(MsgPanelNew), "new"))
	return c.Build()
}

func (e *Engine) panelModelAction(agent Agent, sessions *SessionManager, ik, key, value string) (notice string) {
	// Resolve only an exact currently offered name, never a moving numeric index.
	if ms, ok := agent.(ModelSwitcher); ok {
		ctx, cancel := context.WithTimeout(e.ctx, 3*time.Second)
		models := ms.AvailableModels(ctx)
		cancel()
		found := false
		for _, model := range models {
			if model.Name == value {
				found = true
				break
			}
		}
		if found {
			resolved, switchErr := e.switchModelOnAgent(agent, value, agent == e.agent)
			if switchErr != nil {
				notice = e.i18n.Tf(MsgError, switchErr)
			} else {
				e.persistWorkspaceModelOverride(ik, key, agent, resolved)
				e.cleanupInteractiveState(ik)
				sessions.Save()
				notice = e.i18n.Tf(MsgModelCardSwitched, resolved)
			}
		} else {
			notice = e.i18n.T(MsgPanelExpired)
		}
	}
	return notice
}

func (e *Engine) panelSessionAction(agent Agent, sessions *SessionManager, ik, key, action, value string) (card *Card, notice string) {
	list, listErr := agent.ListSessions(e.ctx)
	if listErr != nil {
		notice = e.i18n.Tf(MsgListError, listErr)
		return nil, notice
	}
	list = e.applySessionFilter(list, sessions, key)
	found := false
	for _, item := range list {
		if item.ID != value {
			continue
		}
		found = true
		if action == "switch" {
			e.cleanupInteractiveState(ik)
			sessions.SwitchToAgentSession(key, item.ID, agent.Name(), item.Summary)
			sessions.Save()
			notice = e.i18n.T(MsgPanelSwitched)
		} else {
			card = NewCard().Title(e.i18n.T(MsgPanelSessions), "turquoise").Markdown(e.deleteSessionDisplayName(sessions, &item)+"\n"+item.ModifiedAt.Format("2006-01-02 15:04")+"\n"+item.ID).
				Buttons(PrimaryBtn(e.i18n.T(MsgPanelEnter), "switch "+item.ID), DefaultBtn(e.i18n.T(MsgCardBack), "sessions")).Build()
		}
		break
	}
	if !found {
		notice = e.i18n.T(MsgPanelExpired)
	}
	return card, notice
}

func (e *Engine) panelSettings(p Platform, action, value string) (card *Card, notice string) {
	if action == "hide" {
		if value != "true" && value != "false" {
			notice = e.i18n.T(MsgPanelExpired)
		} else if e.progressVisibilitySaveFunc == nil {
			notice = e.i18n.T(MsgPanelSaveFailed)
		} else if err := e.progressVisibilitySaveFunc(p.Name(), value == "true"); err != nil {
			slog.Warn("panel: settings save failed", "error", err)
			notice = e.i18n.T(MsgPanelSaveFailed)
		} else {
			for _, platform := range e.platforms {
				if platform.Name() == p.Name() {
					if setter, ok := platform.(ProgressVisibilitySetter); ok {
						setter.SetHideProgressOnFinish(value == "true")
					}
				}
			}
		}
	}
	card = NewCard().Title(e.i18n.T(MsgPanelSettings), "grey").Note(e.i18n.Tf(MsgPanelSettingsScope, e.name, p.Name())).Build()
	if provider, ok := p.(ProgressVisibilityProvider); ok {
		enabled := provider.HideProgressOnFinish()
		marker := "○ "
		if enabled {
			marker = "✓ "
		}
		card.Elements = append(card.Elements, CardActions{Buttons: []CardButton{DefaultBtn(marker+e.i18n.T(MsgPanelHide), "hide "+strconv.FormatBool(!enabled))}})
	}
	return card, notice
}

func panelShortcuts(i *I18n) [][]ButtonOption {
	return [][]ButtonOption{{{Text: "🎛 " + i.T(MsgPanelHome), Data: "home"}, {Text: "💬 " + i.T(MsgPanelSessions), Data: "sessions"}}, {{Text: "🤖 " + i.T(MsgPanelModels), Data: "models"}, {Text: "➕ " + i.T(MsgPanelNew), Data: "new"}}}
}

// PanelShortcutAction recognizes only the reserved, emoji-prefixed keyboard
// labels. This also works after a service restart, when Telegram keeps a keyboard.
func PanelShortcutAction(text string) string {
	for _, lang := range []Language{LangEnglish, LangChinese, LangTraditionalChinese, LangJapanese, LangSpanish} {
		for _, row := range panelShortcuts(NewI18n(lang)) {
			for _, button := range row {
				if text == button.Text {
					return button.Data
				}
			}
		}
	}
	return ""
}

func (e *Engine) reasoningCurrentText(rs ReasoningEffortSwitcher) string {
	if rs.GetReasoningEffort() == "" {
		return e.i18n.T(MsgReasoningDefault)
	}
	return e.i18n.Tf(MsgReasoningCurrent, rs.GetReasoningEffort())
}
func (e *Engine) panelEfforts(agent Agent) *Card {
	c := NewCard().Title(e.i18n.T(MsgPanelReasoning), "indigo")
	rs, ok := agent.(ReasoningEffortSwitcher)
	if !ok {
		return c.Markdown(e.i18n.T(MsgReasoningNotSupported)).Build()
	}
	c.Markdown(e.reasoningCurrentText(rs))
	row := []CardButton{}
	for _, effort := range rs.AvailableReasoningEfforts() {
		label := effort
		if effort == rs.GetReasoningEffort() {
			label = "✓ " + label
		}
		row = append(row, DefaultBtn(label, "effort "+effort))
		if len(row) == 3 {
			c.Buttons(row...)
			row = nil
		}
	}
	if len(row) > 0 {
		c.Buttons(row...)
	}
	c.Buttons(DefaultBtn(e.i18n.T(MsgPanelModels), "models"))
	return c.Build()
}
func (e *Engine) panelEffortAction(agent Agent, ik, key, target string) string {
	rs, ok := agent.(ReasoningEffortSwitcher)
	if !ok {
		return e.i18n.T(MsgReasoningNotSupported)
	}
	valid := false
	for _, effort := range rs.AvailableReasoningEfforts() {
		if effort == target {
			valid = true
			break
		}
	}
	if !valid {
		return e.i18n.T(MsgPanelExpired)
	}
	if e.projectState != nil {
		workspace := workspaceModelOverrideKey(ik, key, agent)
		if err := e.projectState.SaveReasoningOverride(workspace, target); err != nil {
			return e.i18n.Tf(MsgPanelEffortSaveFailed, err)
		}
	}
	e.cleanupInteractiveState(ik)
	rs.SetReasoningEffort(target)
	return e.i18n.Tf(MsgPanelEffortChanged, target)
}
