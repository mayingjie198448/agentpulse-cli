package tui_test

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mayingjie198448/agentpulse-cli/internal/mock"
	"github.com/mayingjie198448/agentpulse-cli/internal/tui"
)

func TestTUIModel_Navigation(t *testing.T) {
	st := mock.NewMockStore()
	defer st.Close()

	m := tui.NewModel(st)

	// Send WindowSizeMsg
	var model tea.Model = m
	model, _ = model.Update(tea.WindowSizeMsg{Width: 100, Height: 40})

	// Initial view rendering should contain AgentPulse
	view := model.View()
	if !strings.Contains(view, "AgentPulse") {
		t.Fatalf("expected view to contain 'AgentPulse', got: %s", view)
	}

	if !strings.Contains(view, "ccdq") {
		t.Fatalf("expected view to contain machine 'ccdq'")
	}

	// Press 'j' (down)
	model, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	viewAfterDown := model.View()
	if viewAfterDown == "" {
		t.Fatalf("view should not be empty")
	}

	// Press 'tab' to switch to logs pane
	model, _ = model.Update(tea.KeyMsg{Type: tea.KeyTab})

	// Press 'space' to pause
	model, _ = model.Update(tea.KeyMsg{Type: tea.KeySpace})

	// View should reflect pause
	viewPaused := model.View()
	if !strings.Contains(viewPaused, "PAUSED") {
		t.Fatalf("expected view to show PAUSED after space, got: %s", viewPaused)
	}

	// Press 'q' to quit
	var cmd tea.Cmd
	model, cmd = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	if cmd == nil {
		t.Fatalf("expected quit command on 'q'")
	}
}
