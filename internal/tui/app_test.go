package tui_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

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

func TestTUIModel_AppendWhenEmpty(t *testing.T) {
	st := &fakeStore{}
	defer st.Close()

	m := tui.NewModel(st)
	if len(m.MachinesForTest()) != 0 {
		t.Fatalf("expected 0 initial machines, got %d", len(m.MachinesForTest()))
	}
	if len(m.JobsForTest()) != 0 {
		t.Fatalf("expected 0 initial jobs, got %d", len(m.JobsForTest()))
	}

	var tm tea.Model = m
	tm, _ = tm.Update(tea.WindowSizeMsg{Width: 100, Height: 40})

	initialView := tm.View()
	if !strings.Contains(initialView, "No active jobs") {
		t.Fatalf("expected view to show 'No active jobs', got: %s", initialView)
	}

	// 1. Receive EventMachineUpdated when machines list is empty -> should append
	machEv := model.Event{
		Type: model.EventMachineUpdated,
		Machine: &model.Machine{
			ID:            "node-mac-01",
			Name:          "node-mac-01",
			Online:        true,
			LastHeartbeat: time.Now(),
			Platform:      "darwin/arm64",
			JobCount:      0,
		},
	}
	tm, _ = tm.Update(machEv)
	if len(m.MachinesForTest()) != 1 {
		t.Fatalf("expected 1 machine after EventMachineUpdated, got %d", len(m.MachinesForTest()))
	}
	if m.MachinesForTest()[0].ID != "node-mac-01" {
		t.Fatalf("expected machine ID 'node-mac-01', got %s", m.MachinesForTest()[0].ID)
	}
	machView := tm.View()
	if !strings.Contains(machView, "node-mac-01") {
		t.Fatalf("expected view to contain machine 'node-mac-01', got: %s", machView)
	}

	// 2. Receive EventJobUpdated when jobs list is empty -> should append
	jobEv := model.Event{
		Type: model.EventJobUpdated,
		Job: &model.Job{
			ID:        "job-dispatched-01",
			MachineID: "node-mac-01",
			Engine:    model.EngineAgy,
			Status:    model.StatusRunning,
			Title:     "Refactor Core Logic",
			Progress:  25.0,
		},
	}
	tm, _ = tm.Update(jobEv)
	if len(m.JobsForTest()) != 1 {
		t.Fatalf("expected 1 job after EventJobUpdated, got %d", len(m.JobsForTest()))
	}
	if m.JobsForTest()[0].ID != "job-dispatched-01" {
		t.Fatalf("expected job ID 'job-dispatched-01', got %s", m.JobsForTest()[0].ID)
	}
	jobView := tm.View()
	if !strings.Contains(jobView, "job-dispatched-01") {
		t.Fatalf("expected view to contain job 'job-dispatched-01', got: %s", jobView)
	}
	if !strings.Contains(jobView, "Refactor Core Logic") {
		t.Fatalf("expected view to contain job title, got: %s", jobView)
	}
	if !strings.Contains(jobView, "LIVE LOG STREAM (job-dispatched-01)") {
		t.Fatalf("expected live log stream for job-dispatched-01, got: %s", jobView)
	}

	// 3. Receive EventLogAdded for the newly appended job -> logs appear in view
	logEv := model.Event{
		Type: model.EventLogAdded,
		Log: &model.LogEntry{
			JobID:     "job-dispatched-01",
			MachineID: "node-mac-01",
			Level:     model.LevelInfo,
			Message:   "Starting step 1 of refactor",
		},
	}
	tm, _ = tm.Update(logEv)
	logs := m.LogsForTest()["job-dispatched-01"]
	if len(logs) != 1 {
		t.Fatalf("expected 1 log entry, got %d", len(logs))
	}
	logView := tm.View()
	if !strings.Contains(logView, "Starting step 1 of refactor") {
		t.Fatalf("expected view to contain log message, got: %s", logView)
	}

	// 4. Update existing job -> in-place update without duplicate
	jobUpdateEv := model.Event{
		Type: model.EventJobUpdated,
		Job: &model.Job{
			ID:        "job-dispatched-01",
			MachineID: "node-mac-01",
			Engine:    model.EngineAgy,
			Status:    model.StatusDone,
			Title:     "Refactor Core Logic",
			Progress:  100.0,
		},
	}
	tm, _ = tm.Update(jobUpdateEv)
	if len(m.JobsForTest()) != 1 {
		t.Fatalf("expected still 1 job after update, got %d", len(m.JobsForTest()))
	}
	if m.JobsForTest()[0].Status != model.StatusDone {
		t.Fatalf("expected job status 'done', got %s", m.JobsForTest()[0].Status)
	}
	doneView := tm.View()
	if !strings.Contains(doneView, "DONE") {
		t.Fatalf("expected view to show DONE, got: %s", doneView)
	}

	// 5. Update existing machine (e.g. heartbeat with updated job count) -> in-place update
	machUpdateEv := model.Event{
		Type: model.EventMachineUpdated,
		Machine: &model.Machine{
			ID:            "node-mac-01",
			Name:          "node-mac-01",
			Online:        true,
			LastHeartbeat: time.Now(),
			JobCount:      1,
		},
	}
	tm, _ = tm.Update(machUpdateEv)
	if len(m.MachinesForTest()) != 1 {
		t.Fatalf("expected still 1 machine after update, got %d", len(m.MachinesForTest()))
	}
	if m.MachinesForTest()[0].JobCount != 1 {
		t.Fatalf("expected job count 1, got %d", m.MachinesForTest()[0].JobCount)
	}
	machUpdateView := tm.View()
	if !strings.Contains(machUpdateView, "[1 jobs]") {
		t.Fatalf("expected view to show [1 jobs], got: %s", machUpdateView)
	}

	// 6. Append second job and machine when list is non-empty
	mach2Ev := model.Event{
		Type: model.EventMachineUpdated,
		Machine: &model.Machine{
			ID:     "node-linux-02",
			Name:   "node-linux-02",
			Online: true,
		},
	}
	job2Ev := model.Event{
		Type: model.EventJobUpdated,
		Job: &model.Job{
			ID:        "job-dispatched-02",
			MachineID: "node-linux-02",
			Engine:    model.EngineOpenCode,
			Status:    model.StatusRunning,
			Title:     "Secondary Job",
		},
	}
	tm, _ = tm.Update(mach2Ev)
	tm, _ = tm.Update(job2Ev)
	if len(m.MachinesForTest()) != 2 {
		t.Fatalf("expected 2 machines, got %d", len(m.MachinesForTest()))
	}
	if len(m.JobsForTest()) != 2 {
		t.Fatalf("expected 2 jobs, got %d", len(m.JobsForTest()))
	}
	multiView := tm.View()
	if !strings.Contains(multiView, "node-linux-02") || !strings.Contains(multiView, "job-dispatched-02") {
		t.Fatalf("expected multiView to contain node-linux-02 and job-dispatched-02, got: %s", multiView)
	}
}
