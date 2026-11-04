package tui_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mayingjie198448/agentpulse-cli/internal/model"
	"github.com/mayingjie198448/agentpulse-cli/internal/store"
	"github.com/mayingjie198448/agentpulse-cli/internal/tui"
)

// fakeStore is local test data. It is not wired to any CLI flag.
type fakeStore struct {
	machines []model.Machine
	jobs     []model.Job
	logs     map[string][]model.LogEntry
}

var _ store.Store = (*fakeStore)(nil)

func (f *fakeStore) ListMachines(context.Context) ([]model.Machine, error) {
	return f.machines, nil
}

func (f *fakeStore) GetMachine(_ context.Context, id string) (*model.Machine, error) {
	for i := range f.machines {
		if f.machines[i].ID == id {
			m := f.machines[i]
			return &m, nil
		}
	}
	return nil, fmt.Errorf("machine %s not found", id)
}

func (f *fakeStore) ListJobs(context.Context) ([]model.Job, error) {
	return f.jobs, nil
}

func (f *fakeStore) GetJob(_ context.Context, id string) (*model.Job, error) {
	for i := range f.jobs {
		if f.jobs[i].ID == id {
			j := f.jobs[i]
			return &j, nil
		}
	}
	return nil, fmt.Errorf("job %s not found", id)
}

func (f *fakeStore) GetLogs(_ context.Context, jobID string, limit int) ([]model.LogEntry, error) {
	entries := f.logs[jobID]
	if limit > 0 && len(entries) > limit {
		entries = entries[len(entries)-limit:]
	}
	return entries, nil
}

func (f *fakeStore) ListAnnounces(context.Context) ([]model.MachineAnnounce, error) {
	return nil, nil
}

func (f *fakeStore) GetAnnounce(context.Context, string) (*model.MachineAnnounce, error) {
	return nil, fmt.Errorf("announce not found")
}

func (f *fakeStore) Subscribe(context.Context) (<-chan model.Event, error) {
	ch := make(chan model.Event)
	return ch, nil
}

func (f *fakeStore) Close() error { return nil }

func TestTUIModel_Navigation(t *testing.T) {
	st := &fakeStore{
		machines: []model.Machine{
			{ID: "ccdq", Name: "ccdq", Online: true, Platform: "linux/amd64"},
		},
		jobs: []model.Job{
			{ID: "job-agy-01", MachineID: "ccdq", Engine: model.EngineAgy, Status: model.StatusRunning, Title: "Auth Middleware Refactor", Progress: 68},
		},
		logs: map[string][]model.LogEntry{
			"job-agy-01": {{JobID: "job-agy-01", Level: model.LevelInfo, Message: "started"}},
		},
	}
	defer st.Close()

	m := tui.NewModel(st)

	var model tea.Model = m
	model, _ = model.Update(tea.WindowSizeMsg{Width: 100, Height: 40})

	view := model.View()
	if !strings.Contains(view, "AgentPulse") {
		t.Fatalf("expected view to contain 'AgentPulse', got: %s", view)
	}
	if strings.Contains(view, "MOCK") {
		t.Fatalf("view must not advertise a mock stream, got: %s", view)
	}
	if !strings.Contains(view, "ccdq") {
		t.Fatalf("expected view to contain machine 'ccdq'")
	}

	model, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	if model.View() == "" {
		t.Fatalf("view should not be empty")
	}

	model, _ = model.Update(tea.KeyMsg{Type: tea.KeyTab})
	model, _ = model.Update(tea.KeyMsg{Type: tea.KeySpace})

	viewPaused := model.View()
	if !strings.Contains(viewPaused, "PAUSED") {
		t.Fatalf("expected view to show PAUSED after space, got: %s", viewPaused)
	}

	var cmd tea.Cmd
	model, cmd = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	if cmd == nil {
		t.Fatalf("expected quit command on 'q'")
	}
}
