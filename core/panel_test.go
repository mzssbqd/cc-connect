package core

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type panelTestPlatform struct {
	stubPlatformEngine
	card *Card
	hide bool
}

func (p *panelTestPlatform) SendPanel(ctx context.Context, msg *Message, c *Card, _ [][]ButtonOption, _, _ string) error {
	p.card = c
	return p.Reply(ctx, msg.ReplyCtx, c.RenderText())
}
func (p *panelTestPlatform) HideProgressOnFinish() bool        { return p.hide }
func (p *panelTestPlatform) SetHideProgressOnFinish(hide bool) { p.hide = hide }
func panelAction(t *testing.T, c *Card, contains string) string {
	t.Helper()
	for _, el := range c.Elements {
		if row, ok := el.(CardActions); ok {
			for _, b := range row.Buttons {
				if strings.Contains(b.Value, " "+contains) {
					return "/panel " + b.Value
				}
			}
		}
	}
	t.Fatalf("action %q missing in %+v", contains, c)
	return ""
}
func newPanelTest(t *testing.T) (*Engine, *panelTestPlatform, *stubListAgent, func(string)) {
	t.Helper()
	p := &panelTestPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}, hide: true}
	a := &stubListAgent{sessions: []AgentSessionInfo{{ID: "session-a", Summary: "Alpha"}, {ID: "session-b", Summary: "Beta"}}}
	e := NewEngine("test", a, []Platform{p}, "", LangEnglish)
	t.Cleanup(func() { e.Stop() })
	send := func(text string) {
		e.ReceiveMessage(p, &Message{SessionKey: "test:user", Platform: p.Name(), UserID: "user", Content: text})
	}
	return e, p, a, send
}
func TestPanelRejectsBusyStaleAndDisabledActions(t *testing.T) {
	e, p, _, send := newPanelTest(t)
	s := e.sessions.GetOrCreateActive("test:user")
	gen, _ := s.TryLock()
	send("/panel create")
	if !strings.Contains(p.card.RenderText(), "in progress") {
		t.Fatal(p.card.RenderText())
	}
	if e.sessions.GetOrCreateActive("test:user") != s {
		t.Fatal("busy session replaced")
	}
	s.Unlock(gen)
	send("/panel @wrong create")
	if !strings.Contains(p.card.RenderText(), "out of date") {
		t.Fatal(p.card.RenderText())
	}
	e.SetDisabledCommands([]string{"new"})
	send("/panel create")
	if !strings.Contains(p.card.RenderText(), "disabled") {
		t.Fatal(p.card.RenderText())
	}
	if e.sessions.GetOrCreateActive("test:user") != s {
		t.Fatal("disabled action executed")
	}
}
func TestPanelSettingsPersistBeforeLiveUpdate(t *testing.T) {
	e, p, _, send := newPanelTest(t)
	e.SetProgressVisibilitySaveFunc(func(string, bool) error { return errors.New("disk full") })
	send("/panel hide false")
	if !p.hide || !strings.Contains(p.card.RenderText(), "could not be saved") {
		t.Fatal(p.card.RenderText())
	}
	var saved bool
	e.SetProgressVisibilitySaveFunc(func(name string, hide bool) error {
		if name != "test" || hide {
			t.Fatal("wrong patch")
		}
		saved = true
		return nil
	})
	send("/panel hide false")
	if !saved || p.hide {
		t.Fatal("setting not applied")
	}
}
func TestPanelPaginationBounds(t *testing.T) {
	for _, v := range []string{"-1", "0", "999", "bad"} {
		_, start, end := panelPage(v, 7)
		if start < 0 || end > 7 || start > end {
			t.Fatal(v, start, end)
		}
	}
}

func TestPanelModelUsesExactNameAndSaveFailureKeepsSelection(t *testing.T) {
	e, p, _, send := newPanelTest(t)
	a := &stubStrictModelAgent{stubModelModeAgent: stubModelModeAgent{model: "model-a"}, models: []ModelOption{{Name: "model-a"}, {Name: "model-b"}}}
	e.agent = a
	send("/panel models")
	target := panelAction(t, p.card, "model model-b")
	a.models[0], a.models[1] = a.models[1], a.models[0]
	e.SetModelSaveFunc(func(string) error { return errors.New("disk full") })
	send(target)
	if a.model != "model-a" || !strings.Contains(p.card.RenderText(), "disk full") {
		t.Fatal(a.model, p.card.RenderText())
	}
	e.SetModelSaveFunc(func(string) error { return nil })
	send(target)
	if a.model != "model-b" {
		t.Fatal(a.model)
	}
	send("/panel model missing")
	if a.model != "model-b" || !strings.Contains(p.card.RenderText(), "out of date") {
		t.Fatal(a.model, p.card.RenderText())
	}
}
func TestPanelShortcutsSurviveRestartWithoutConsumingOrdinaryText(t *testing.T) {
	for _, lang := range []Language{LangEnglish, LangChinese, LangTraditionalChinese, LangJapanese, LangSpanish} {
		for _, row := range panelShortcuts(NewI18n(lang)) {
			for _, button := range row {
				if PanelShortcutAction(button.Text) != button.Data {
					t.Fatal(button)
				}
			}
		}
	}
	for _, text := range []string{"新建会话", "会话列表", "Switch model", "please open settings"} {
		if PanelShortcutAction(text) != "" {
			t.Fatal(text)
		}
	}
}

func TestPanelHomeShowsExistingUnnamedSessionSummary(t *testing.T) {
	e, p, a, send := newPanelTest(t)
	e.sessions.GetOrCreateActive("test:user").SetAgentSessionID("session-a", a.Name())
	e.sessions.GetOrCreateActive("test:user").SetName("")
	send("/panel")
	if !strings.Contains(p.card.RenderText(), "Alpha") || strings.Contains(p.card.RenderText(), e.i18n.T(MsgSessionNotStarted)) {
		t.Fatal(p.card.RenderText())
	}
}

func TestPanelEffortPreservesSessionAndPersists(t *testing.T) {
	e, p, _, send := newPanelTest(t)
	a := &stubModelModeAgent{reasoningEffort: "low"}
	e.agent = a
	path := t.TempDir() + "/state.json"
	e.SetProjectStateStore(NewProjectStateStore(path))
	s := e.sessions.GetOrCreateActive("test:user")
	s.SetAgentSessionID("existing", a.Name())
	s.AddHistory("user", "keep this")
	send("/panel models")
	send(panelAction(t, p.card, "efforts"))
	send(panelAction(t, p.card, "effort high"))
	if a.GetReasoningEffort() != "high" || s.GetAgentSessionID() != "existing" || len(s.GetHistory(0)) != 1 {
		t.Fatal("effort switch lost conversation or did not apply")
	}
	if NewProjectStateStore(path).ReasoningOverride("") != "high" {
		t.Fatal("selection not persisted")
	}
	a.SetReasoningEffort("low")
	e.SetProjectStateStore(NewProjectStateStore(path))
	if a.GetReasoningEffort() != "high" {
		t.Fatal("restart did not restore selection")
	}
	send("/panel effort invalid")
	if a.GetReasoningEffort() != "high" {
		t.Fatal("invalid selection accepted")
	}
	gen, _ := s.TryLock()
	send("/panel effort low")
	s.Unlock(gen)
	if a.GetReasoningEffort() != "high" {
		t.Fatal("busy selection accepted")
	}
	e.SetDisabledCommands([]string{"reasoning"})
	send("/panel effort low")
	if a.GetReasoningEffort() != "high" {
		t.Fatal("disabled selection accepted")
	}
}

func TestPanelEffortSaveFailureKeepsLiveSelection(t *testing.T) {
	e, p, _, send := newPanelTest(t)
	a := &stubModelModeAgent{reasoningEffort: "low"}
	e.agent = a
	e.SetProjectStateStore(NewProjectStateStore(t.TempDir()))
	send("/panel effort high")
	if a.GetReasoningEffort() != "low" || !strings.Contains(p.card.RenderText(), "Could not save") {
		t.Fatal(p.card.RenderText())
	}
}

func TestPanelEffortWorkspaceRecreation(t *testing.T) {
	name := "panel-effort-recreate-test"
	RegisterAgent(name, func(opts map[string]any) (Agent, error) {
		effort, _ := opts["reasoning_effort"].(string)
		return &namedStubModelModeAgent{name: name, stubModelModeAgent: stubModelModeAgent{reasoningEffort: effort}}, nil
	})
	e, _, _, _ := newPanelTest(t)
	e.agent = &namedStubModelModeAgent{name: name}
	e.SetProjectStateStore(NewProjectStateStore(t.TempDir() + "/state.json"))
	workspace := normalizeWorkspacePath(t.TempDir())
	if err := e.projectState.SaveReasoningOverride(workspace, "xhigh"); err != nil {
		t.Fatal(err)
	}
	a, _, err := e.getOrCreateWorkspaceAgent(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if a.(ReasoningEffortSwitcher).GetReasoningEffort() != "xhigh" {
		t.Fatal("workspace override not applied")
	}
}

func TestPanelHomeShowsEffortAndStatusLayout(t *testing.T) {
	e, p, _, send := newPanelTest(t)
	a := &stubModelModeAgent{model: "gpt-test", reasoningEffort: "high"}
	e.agent = a
	send("/panel")
	text := p.card.RenderText()
	for _, want := range []string{"🎛️", "📁", "📂", "💬", "🤖", "gpt-test · high", "🟢"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q: %s", want, text)
		}
	}
	s := e.sessions.GetOrCreateActive("test:user")
	gen, _ := s.TryLock()
	send("/panel")
	s.Unlock(gen)
	if !strings.Contains(p.card.RenderText(), "⏳") {
		t.Fatal(p.card.RenderText())
	}
	a.reasoningEffort = ""
	send("/panel")
	if !strings.Contains(p.card.RenderText(), "gpt-test · "+e.i18n.T(MsgModelDefault)) {
		t.Fatal(p.card.RenderText())
	}
}
