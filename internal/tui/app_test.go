package tui_test

import (
	"bytes"
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
	machines     []model.Machine
	jobs         []model.Job
	logs         map[string][]model.LogEntry
	disconnected bool
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

func (f *fakeStore) IsConnected() bool {
	return !f.disconnected
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

func TestTUIModel_HeaderAndStatusGlyphs(t *testing.T) {
	st := &fakeStore{
		machines: []model.Machine{
			{ID: "node-1", Name: "node-1", Online: true},
			{ID: "node-2", Name: "node-2", Online: false},
		},
		jobs: []model.Job{
			{ID: "job-run", MachineID: "node-1", Engine: model.EngineAgy, Status: model.StatusRunning, Progress: 42, Title: "Running Task"},
			{ID: "job-fail", MachineID: "node-1", Engine: model.EngineOpenCode, Status: model.StatusFailed, Error: "command crashed", Title: "Failing Task"},
			{ID: "job-attn", MachineID: "node-2", Engine: model.EngineAgy, Status: "waiting_input", Title: "Needs Input Task"},
			{ID: "job-done", MachineID: "node-2", Engine: model.EngineAgy, Status: model.StatusDone, Progress: 100, Title: "Finished Task"},
		},
		logs: map[string][]model.LogEntry{
			"job-run":  {{JobID: "job-run", Level: model.LevelInfo, Message: "running step 1"}},
			"job-fail": {{JobID: "job-fail", Level: model.LevelError, Message: "unexpected exit code"}},
		},
	}
	defer st.Close()

	m := tui.NewModel(st)
	var tm tea.Model = m
	tm, _ = tm.Update(tea.WindowSizeMsg{Width: 120, Height: 40})

	view := tm.View()

	// 1. Header checks: online machine count, running job count, needs attention count, broker status
	if !strings.Contains(view, "1/2 online") {
		t.Errorf("expected view to contain '1/2 online', got:\n%s", view)
	}
	if !strings.Contains(view, "1 running") {
		t.Errorf("expected view to contain '1 running', got:\n%s", view)
	}
	if !strings.Contains(view, "2 needs attention") {
		t.Errorf("expected view to contain '2 needs attention', got:\n%s", view)
	}
	if !strings.Contains(view, "Broker: ● connected") {
		t.Errorf("expected view to show broker connected, got:\n%s", view)
	}

	// 2. Status glyphs in JOBS panel
	if !strings.Contains(view, "● 42%") {
		t.Errorf("expected view to contain running glyph '● 42%%%%', got:\n%s", view)
	}
	if !strings.Contains(view, "✖ FAIL") {
		t.Errorf("expected view to contain failed glyph '✖ FAIL', got:\n%s", view)
	}
	if !strings.Contains(view, "▲ ATTN") {
		t.Errorf("expected view to contain attention glyph '▲ ATTN', got:\n%s", view)
	}
	if !strings.Contains(view, "✓ DONE") {
		t.Errorf("expected view to contain done glyph '✓ DONE', got:\n%s", view)
	}

	// 3. Footer Chinese hints
	if !strings.Contains(view, "退出") ||
		!strings.Contains(view, "切换面板") ||
		!strings.Contains(view, "选择任务") ||
		!strings.Contains(view, "追踪/聚焦") ||
		!strings.Contains(view, "自动滚动") {
		t.Errorf("expected Chinese hints in footer, got:\n%s", view)
	}
}

func TestTUIModel_FollowStreamAndEnterKey(t *testing.T) {
	st := &fakeStore{
		machines: []model.Machine{
			{ID: "node-1", Name: "node-1", Online: true},
		},
		jobs: []model.Job{
			{ID: "job-01", MachineID: "node-1", Engine: model.EngineAgy, Status: model.StatusRunning, Progress: 10, Title: "Job One"},
			{ID: "job-02", MachineID: "node-1", Engine: model.EngineOpenCode, Status: model.StatusFailed, Error: "fatal error", Title: "Job Two"},
		},
		logs: map[string][]model.LogEntry{
			"job-01": {{JobID: "job-01", Level: model.LevelInfo, Message: "job 1 log A"}},
			"job-02": {{JobID: "job-02", Level: model.LevelError, Message: "job 2 log B"}},
		},
	}
	defer st.Close()

	m := tui.NewModel(st)
	var tm tea.Model = m
	tm, _ = tm.Update(tea.WindowSizeMsg{Width: 120, Height: 40})

	// Initial selection is job-01, autoScroll is true (following live stream)
	if m.SelectedJobIdxForTest() != 0 {
		t.Fatalf("expected initial selectedJobIdx 0, got %d", m.SelectedJobIdxForTest())
	}
	if !m.AutoScrollForTest() {
		t.Fatalf("expected autoScroll to be true by default")
	}
	if m.ActivePaneForTest() != tui.PaneJobs {
		t.Fatalf("expected activePane to be PaneJobs initially")
	}

	view1 := tm.View()
	if !strings.Contains(view1, "[FOLLOWING]") {
		t.Errorf("expected view to indicate [FOLLOWING], got:\n%s", view1)
	}
	if !strings.Contains(view1, "job 1 log A") {
		t.Errorf("expected initial logs for job-01, got:\n%s", view1)
	}

	// Press 'j' to navigate to job-02
	tm, _ = tm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	if m.SelectedJobIdxForTest() != 1 {
		t.Fatalf("expected selectedJobIdx 1 after 'j', got %d", m.SelectedJobIdxForTest())
	}
	if !m.AutoScrollForTest() {
		t.Fatalf("expected autoScroll to remain true (following stream) when navigating to new job")
	}
	view2 := tm.View()
	if !strings.Contains(view2, "job 2 log B") {
		t.Errorf("expected view to follow job-02 stream, got:\n%s", view2)
	}
	if !strings.Contains(view2, "⚠ Attention: fatal error") {
		t.Errorf("expected attention banner in detail box for job-02, got:\n%s", view2)
	}

	// Press 'enter': should switch active pane to PaneLogs and keep autoScroll
	tm, _ = tm.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.ActivePaneForTest() != tui.PaneLogs {
		t.Fatalf("expected activePane to switch to PaneLogs on Enter, got %v", m.ActivePaneForTest())
	}
	if !m.AutoScrollForTest() {
		t.Fatalf("expected autoScroll to remain true on Enter")
	}

	// Press 'enter' again from PaneLogs: should switch back to PaneJobs
	tm, _ = tm.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.ActivePaneForTest() != tui.PaneJobs {
		t.Fatalf("expected activePane to switch back to PaneJobs on Enter, got %v", m.ActivePaneForTest())
	}
}

func TestTUIModel_BrokerDisconnectedStatus(t *testing.T) {
	st := &fakeStore{
		disconnected: true,
		machines: []model.Machine{
			{ID: "node-1", Online: true},
		},
	}
	defer st.Close()

	m := tui.NewModel(st)
	var tm tea.Model = m
	tm, _ = tm.Update(tea.WindowSizeMsg{Width: 100, Height: 40})

	if m.BrokerConnectedForTest() {
		t.Fatalf("expected brokerConnected to be false for disconnected store")
	}

	view := tm.View()
	if !strings.Contains(view, "Broker: ○ disconnected") {
		t.Errorf("expected view to show 'Broker: ○ disconnected', got:\n%s", view)
	}
}

func TestTUIModel_HeightBudgetingRegression(t *testing.T) {
	st := &fakeStore{
		machines: []model.Machine{
			{ID: "node-1", Name: "node-1", Online: true},
			{ID: "node-2", Name: "node-2", Online: false},
		},
		jobs: []model.Job{
			{ID: "job-run", MachineID: "node-1", Engine: model.EngineAgy, Status: model.StatusRunning, Progress: 42, Title: "Running Task"},
			{ID: "job-fail", MachineID: "node-1", Engine: model.EngineOpenCode, Status: model.StatusFailed, Error: "command crashed", Title: "Failing Task"},
			{ID: "job-attn", MachineID: "node-2", Engine: model.EngineAgy, Status: "waiting_input", Title: "Needs Input Task"},
			{ID: "job-done", MachineID: "node-2", Engine: model.EngineAgy, Status: model.StatusDone, Progress: 100, Title: "Finished Task"},
		},
		logs: map[string][]model.LogEntry{
			"job-run":  {{JobID: "job-run", Level: model.LevelInfo, Message: "running step 1"}},
			"job-fail": {{JobID: "job-fail", Level: model.LevelError, Message: "unexpected exit code"}},
		},
	}
	defer st.Close()

	// Test typical terminal heights and widths
	heights := []int{24, 30, 40, 50, 80, 120, 200}
	widths := []int{80, 100, 120, 160, 200}

	for _, h := range heights {
		for _, w := range widths {
			m := tui.NewModel(st)
			var tm tea.Model = m
			tm, _ = tm.Update(tea.WindowSizeMsg{Width: w, Height: h})

			v := tm.View()
			lines := strings.Split(v, "\n")
			if len(lines) > h {
				t.Errorf("w=%d, h=%d: View() produced %d lines, exceeding terminal height %d", w, h, len(lines), h)
			}

			// Required header strings must always be present and not pushed off or clipped
			for _, req := range []string{"AgentPulse", "Nodes:", "needs attention", "Broker"} {
				if !strings.Contains(v, req) {
					t.Errorf("w=%d, h=%d: View() missing expected header content %q", w, h, req)
				}
			}
		}
	}
}

func TestTUIModel_HeightBudgeting_LargeDatasetAndNavigation(t *testing.T) {
	var machs []model.Machine
	for i := 0; i < 20; i++ {
		machs = append(machs, model.Machine{
			ID:     fmt.Sprintf("node-%02d", i),
			Name:   fmt.Sprintf("node-%02d", i),
			Online: i%2 == 0,
		})
	}

	var jobs []model.Job
	for i := 0; i < 40; i++ {
		jobs = append(jobs, model.Job{
			ID:        fmt.Sprintf("job-%03d", i),
			MachineID: fmt.Sprintf("node-%02d", i%20),
			Engine:    model.EngineAgy,
			Status:    model.StatusRunning,
			Title:     fmt.Sprintf("Long Project Job Description %d", i),
			Cwd:       "/deep/nested/path/to/project/repo/internal/package",
			Command:   "go test -v -race ./...",
		})
	}

	st := &fakeStore{machines: machs, jobs: jobs}
	defer st.Close()

	for _, h := range []int{24, 40, 80} {
		for _, w := range []int{80, 120} {
			m := tui.NewModel(st)
			var tm tea.Model = m
			tm, _ = tm.Update(tea.WindowSizeMsg{Width: w, Height: h})

			// Navigate down 20 jobs to test list windowing
			for k := 0; k < 20; k++ {
				tm, _ = tm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
			}

			v := tm.View()
			lines := strings.Split(v, "\n")
			if len(lines) > h {
				t.Errorf("large dataset w=%d, h=%d: View() produced %d lines, exceeding %d", w, h, len(lines), h)
			}
			if !strings.Contains(v, "AgentPulse") || !strings.Contains(v, "Nodes:") {
				t.Errorf("large dataset w=%d, h=%d: View() missing header content", w, h)
			}
		}
	}
}

func TestTUIModel_TeaProgramSimulation(t *testing.T) {
	st := &fakeStore{
		machines: []model.Machine{
			{ID: "node-pty", Name: "node-pty", Online: true},
		},
		jobs: []model.Job{
			{ID: "job-pty", MachineID: "node-pty", Engine: model.EngineAgy, Status: model.StatusRunning, Progress: 50, Title: "PTY Sim Task"},
		},
	}
	defer st.Close()

	in := bytes.NewBuffer([]byte{})
	out := &bytes.Buffer{}

	m := tui.NewModel(st)
	p := tea.NewProgram(m, tea.WithInput(in), tea.WithOutput(out), tea.WithoutCatchPanics())

	go func() {
		time.Sleep(30 * time.Millisecond)
		p.Send(tea.WindowSizeMsg{Width: 100, Height: 40})
		time.Sleep(30 * time.Millisecond)
		p.Quit()
	}()

	if _, err := p.Run(); err != nil {
		t.Fatalf("tea program execution failed: %v", err)
	}

	output := out.String()
	for _, req := range []string{"AgentPulse", "Nodes:", "Broker"} {
		if !strings.Contains(output, req) {
			t.Errorf("simulated program output missing %q", req)
		}
	}
}
