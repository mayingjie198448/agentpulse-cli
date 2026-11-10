package tui_test

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

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
	refreshErr   error
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
	cp := make([]model.Job, len(f.jobs))
	copy(cp, f.jobs)
	return cp, nil
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

func (f *fakeStore) DeleteJob(_ context.Context, id string) error {
	for i, j := range f.jobs {
		if j.ID == id {
			if j.Status == model.StatusRunning {
				return fmt.Errorf("任务 %s 正在运行中，无法删除", id)
			}
			f.jobs = append(f.jobs[:i], f.jobs[i+1:]...)
			delete(f.logs, id)
			return nil
		}
	}
	return fmt.Errorf("job %s not found", id)
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

func (f *fakeStore) Refresh(context.Context) error {
	return f.refreshErr
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

func TestTUIModel_JobsSelectionScrolling_LongIDs(t *testing.T) {
	// Create a dataset with long job IDs that wrap onto multiple lines in the left column
	var jobs []model.Job
	for i := 0; i < 25; i++ {
		jobs = append(jobs, model.Job{
			ID:        fmt.Sprintf("job-matrix-runner-prod-deploy-cluster-%02d-long-identifier", i),
			MachineID: fmt.Sprintf("node-%02d", i%3),
			Engine:    model.EngineAgy,
			Status:    model.StatusDone,
			Progress:  100,
			Title:     fmt.Sprintf("Task title %d", i),
		})
	}
	machs := []model.Machine{
		{ID: "node-00", Name: "node-00", Online: true},
		{ID: "node-01", Name: "node-01", Online: true},
		{ID: "node-02", Name: "node-02", Online: true},
	}

	st := &fakeStore{machines: machs, jobs: jobs}
	defer st.Close()

	// Heights 24 (tight) and 40 (normal), widths 80 (tight 38-col left pane) and 120 (44-col left pane)
	for _, h := range []int{24, 40} {
		for _, w := range []int{80, 120} {
			m := tui.NewModel(st)
			var tm tea.Model = m
			tm, _ = tm.Update(tea.WindowSizeMsg{Width: w, Height: h})

			// 1. Initial selection at 0 must be visible
			v := tm.View()
			token0 := fmt.Sprintf("cluster-%02d-long-identifier", 0)
			if !strings.Contains(v, token0) {
				t.Fatalf("w=%d h=%d: initial job 0 token %q not visible in view:\n%s", w, h, token0, v)
			}

			// 2. Step down all the way to 24 using 'j'
			for step := 1; step < len(jobs); step++ {
				tm, _ = tm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
				currView := tm.View()
				currLines := strings.Split(currView, "\n")
				if len(currLines) > h {
					t.Fatalf("w=%d h=%d step=%d: view lines %d exceeded height %d", w, h, step, len(currLines), h)
				}
				// The currently selected job must be visible in the JOBS pane!
				tokenStep := fmt.Sprintf("cluster-%02d-long-identifier", step)
				if !strings.Contains(currView, tokenStep) {
					t.Fatalf("w=%d h=%d: after %d 'j' presses, selected job %d (%s) not visible in view:\n%s",
						w, h, step, step, jobs[step].ID, currView)
				}
			}

			// 3. Step back up using 'k'
			for step := len(jobs) - 2; step >= 0; step-- {
				tm, _ = tm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
				currView := tm.View()
				currLines := strings.Split(currView, "\n")
				if len(currLines) > h {
					t.Fatalf("w=%d h=%d step_up=%d: view lines %d exceeded height %d", w, h, step, len(currLines), h)
				}
				tokenStep := fmt.Sprintf("cluster-%02d-long-identifier", step)
				if !strings.Contains(currView, tokenStep) {
					t.Fatalf("w=%d h=%d: after 'k' stepping up to %d (%s), job not visible in view:\n%s",
						w, h, step, jobs[step].ID, currView)
				}
			}
		}
	}
}

func TestTUIModel_DeleteJobFlow(t *testing.T) {
	st := &fakeStore{
		machines: []model.Machine{
			{ID: "node-1", Name: "node-1", Online: true},
		},
		jobs: []model.Job{
			{ID: "job-run", MachineID: "node-1", Engine: model.EngineAgy, Status: model.StatusRunning, Progress: 30, Title: "Running Task"},
			{ID: "job-done-1", MachineID: "node-1", Engine: model.EngineOpenCode, Status: model.StatusDone, Progress: 100, Title: "Finished 1"},
			{ID: "job-done-2", MachineID: "node-1", Engine: model.EngineAgy, Status: model.StatusDone, Progress: 100, Title: "Finished 2"},
		},
	}
	defer st.Close()

	m := tui.NewModel(st)
	var tm tea.Model = m
	tm, _ = tm.Update(tea.WindowSizeMsg{Width: 120, Height: 40})

	// 1. Try deleting running job (index 0)
	tm, _ = tm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	v := tm.View()
	if !strings.Contains(v, "正在运行中，无法删除") {
		t.Fatalf("expected running job deletion refusal message, got:\n%s", v)
	}
	if len(m.JobsForTest()) != 3 {
		t.Fatalf("expected 3 jobs to remain after refused delete, got %d", len(m.JobsForTest()))
	}

	// 2. Navigate down to job-done-1 (index 1)
	tm, _ = tm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	if m.SelectedJobIdxForTest() != 1 {
		t.Fatalf("expected selectedJobIdx 1, got %d", m.SelectedJobIdxForTest())
	}

	// 3. Press 'd': enter confirmation mode
	tm, _ = tm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	if !m.ConfirmDeleteForTest() {
		t.Fatalf("expected confirmDelete to be true")
	}
	v = tm.View()
	if !strings.Contains(v, "确认删除任务 job-done-1?") {
		t.Fatalf("expected confirmation prompt for job-done-1, got:\n%s", v)
	}

	// 4. Press 'n' to cancel
	tm, _ = tm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	if m.ConfirmDeleteForTest() {
		t.Fatalf("expected confirmDelete to be false after cancel")
	}
	if len(m.JobsForTest()) != 3 {
		t.Fatalf("expected 3 jobs after cancel, got %d", len(m.JobsForTest()))
	}

	// 5. Press 'd' again and confirm with 'y'
	tm, _ = tm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	tm, _ = tm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})

	// Verify job-done-1 removed, job count 2
	if len(m.JobsForTest()) != 2 {
		t.Fatalf("expected 2 jobs after deletion, got %d", len(m.JobsForTest()))
	}
	// Selection should move to index 1 (which is now job-done-2, the neighbor)
	if m.SelectedJobIdxForTest() != 1 || m.JobsForTest()[1].ID != "job-done-2" {
		t.Fatalf("expected selection to point to neighbor job-done-2 at index 1, got idx %d", m.SelectedJobIdxForTest())
	}

	// Header count should update: was 1 done, now 1 done (job-done-2)
	v = tm.View()
	if !strings.Contains(v, "1 done") {
		t.Fatalf("expected header to show '1 done', got:\n%s", v)
	}

	// 6. Delete job-done-2 (the last job)
	tm, _ = tm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	tm, _ = tm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	if len(m.JobsForTest()) != 1 {
		t.Fatalf("expected 1 job after deletion, got %d", len(m.JobsForTest()))
	}
	// Selection moves back to index 0 (job-run)
	if m.SelectedJobIdxForTest() != 0 || m.JobsForTest()[0].ID != "job-run" {
		t.Fatalf("expected selection to fall back to index 0 (job-run), got %d", m.SelectedJobIdxForTest())
	}

	// 7. EventJobDeleted reception from broker
	tm, _ = tm.Update(model.Event{
		Type: model.EventJobDeleted,
		Job:  &model.Job{ID: "job-run"},
	})
	if len(m.JobsForTest()) != 0 {
		t.Fatalf("expected 0 jobs after EventJobDeleted, got %d", len(m.JobsForTest()))
	}
	if m.SelectedJobIdxForTest() != 0 {
		t.Fatalf("expected selectedJobIdx 0 when empty, got %d", m.SelectedJobIdxForTest())
	}
	v = tm.View()
	if !strings.Contains(v, "No active jobs") {
		t.Fatalf("expected 'No active jobs' when all jobs deleted, got:\n%s", v)
	}
}

func TestTUIModel_MachinesPrimaryLabelMachineID(t *testing.T) {
	st := &fakeStore{
		machines: []model.Machine{
			{
				ID:     "mac-mini",
				Name:   "hostname-mini",
				Online: true,
			},
		},
	}
	defer st.Close()

	m := tui.NewModel(st)
	var tm tea.Model = m
	tm, _ = tm.Update(tea.WindowSizeMsg{Width: 120, Height: 40})

	v := tm.View()
	// Must contain config machine_id=mac-mini, not only hostname pretending to be primary
	if !strings.Contains(v, "mac-mini") {
		t.Fatalf("expected View to contain machine_id 'mac-mini', got:\n%s", v)
	}
	if !strings.Contains(v, "mac-mini") || !strings.Contains(v, "hostname-mini") {
		t.Fatalf("expected View to contain 'mac-mini' primary and 'hostname-mini' secondary, got:\n%s", v)
	}
}

func TestTUIModel_ExplicitRefresh(t *testing.T) {
	st := &fakeStore{
		machines: []model.Machine{
			{ID: "node-1", Name: "node-1", Online: true},
			{ID: "node-stale", Name: "node-stale", Online: true},
		},
		jobs: []model.Job{
			{ID: "job-1", MachineID: "node-1", Status: model.StatusRunning, Title: "Job 1"},
			{ID: "job-stale", MachineID: "node-stale", Status: model.StatusDone, Title: "Job Stale"},
		},
		logs: map[string][]model.LogEntry{
			"job-1":     {{JobID: "job-1", Message: "log 1"}},
			"job-stale": {{JobID: "job-stale", Message: "log stale"}},
		},
	}
	defer st.Close()

	m := tui.NewModel(st)
	var tm tea.Model = m
	tm, _ = tm.Update(tea.WindowSizeMsg{Width: 120, Height: 40})

	// Check footer includes 'r' and '刷新'
	view := tm.View()
	if !strings.Contains(view, "r") || !strings.Contains(view, "刷新") {
		t.Fatalf("expected footer to show 'r 刷新', got:\n%s", view)
	}

	// Simulate external state change (node-stale dropped, new node announced)
	st.machines = []model.Machine{
		{ID: "node-1", Name: "node-1", Online: true},
		{ID: "node-new", Name: "node-new", Online: true},
	}
	st.jobs = []model.Job{
		{ID: "job-1", MachineID: "node-1", Status: model.StatusRunning, Title: "Job 1"},
	}

	// Trigger manual refresh via 'r' key
	var cmd tea.Cmd
	tm, cmd = tm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	if cmd == nil {
		t.Fatalf("expected 'r' key to produce a tea.Cmd for refresh")
	}

	// Execute cmd
	msg := cmd()
	if msg == nil {
		t.Fatalf("expected refresh cmd to return a message")
	}

	// Deliver refreshDoneMsg
	tm, _ = tm.Update(msg)

	mRefreshed := tm.(*tui.Model)
	if len(mRefreshed.MachinesForTest()) != 2 {
		t.Fatalf("expected 2 machines after refresh, got %d", len(mRefreshed.MachinesForTest()))
	}
	machIDs := make(map[string]bool)
	for _, mach := range mRefreshed.MachinesForTest() {
		machIDs[mach.ID] = true
	}
	if !machIDs["node-1"] || !machIDs["node-new"] || machIDs["node-stale"] {
		t.Fatalf("expected node-stale to be cleared and node-new present, got %+v", machIDs)
	}

	if len(mRefreshed.JobsForTest()) != 1 || mRefreshed.JobsForTest()[0].ID != "job-1" {
		t.Fatalf("expected job-stale to be dropped after refresh, got %+v", mRefreshed.JobsForTest())
	}
}

func TestTUIModel_RefreshFailure(t *testing.T) {
	st := &fakeStore{
		machines: []model.Machine{
			{ID: "node-orig", Name: "node-orig", Online: true},
		},
		jobs: []model.Job{
			{ID: "job-orig", MachineID: "node-orig", Status: model.StatusRunning, Title: "Job Orig"},
		},
		logs: map[string][]model.LogEntry{
			"job-orig": {{JobID: "job-orig", Message: "log orig"}},
		},
		refreshErr: fmt.Errorf("MQTT subscription timeout"),
	}
	defer st.Close()

	m := tui.NewModel(st)
	var tm tea.Model = m
	tm, _ = tm.Update(tea.WindowSizeMsg{Width: 120, Height: 40})

	// Trigger manual refresh via 'r' key
	var cmd tea.Cmd
	tm, cmd = tm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	if cmd == nil {
		t.Fatalf("expected 'r' key to produce a tea.Cmd for refresh")
	}

	// Execute cmd (fails with refreshErr)
	msg := cmd()
	if msg == nil {
		t.Fatalf("expected refresh cmd to return a message")
	}

	// Deliver refreshDoneMsg
	tm, _ = tm.Update(msg)

	mAfter := tm.(*tui.Model)

	// 1. Existing view data must be kept
	if len(mAfter.MachinesForTest()) != 1 || mAfter.MachinesForTest()[0].ID != "node-orig" {
		t.Fatalf("expected original machines to be preserved, got %+v", mAfter.MachinesForTest())
	}
	if len(mAfter.JobsForTest()) != 1 || mAfter.JobsForTest()[0].ID != "job-orig" {
		t.Fatalf("expected original jobs to be preserved, got %+v", mAfter.JobsForTest())
	}

	// 2. Visible Chinese error hint in status/help area
	if !strings.Contains(mAfter.RefreshErrorForTest(), "刷新失败") {
		t.Fatalf("expected RefreshErrorForTest to contain '刷新失败', got %q", mAfter.RefreshErrorForTest())
	}
	view := mAfter.View()
	if !strings.Contains(view, "刷新失败") {
		t.Fatalf("expected view to contain '刷新失败', got:\n%s", view)
	}

	// 3. Next key clears the error hint
	tm, _ = tm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	mCleared := tm.(*tui.Model)
	if mCleared.RefreshErrorForTest() != "" {
		t.Fatalf("expected error hint to be cleared on next key, got %q", mCleared.RefreshErrorForTest())
	}
}

func TestTUIModel_GroupingAndSelectionPreservation(t *testing.T) {
	t1 := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 11, 2, 11, 30, 0, 0, time.UTC)

	st := &fakeStore{
		machines: []model.Machine{
			{ID: "node-1", Name: "Studio M2", Online: true},
			{ID: "node-2", Name: "Linux Box", Online: true},
		},
		jobs: []model.Job{
			{
				ID:        "job-legacy",
				MachineID: "node-1",
				Engine:    model.EngineAgy,
				Status:    model.StatusRunning,
				Title:     "Legacy Task without fields",
				Progress:  50,
			},
			{
				ID:          "job-repo-a",
				MachineID:   "node-1",
				Engine:      model.EngineAgy,
				Status:      model.StatusRunning,
				Title:       "Task Alpha",
				Repo:        "owner/repo-a",
				Project:     "repo-a",
				Prompt:      "Implement secret sauce algorithm",
				StartedAt:   t1,
				RequestedBy: "alice@company.internal",
				Progress:    75,
			},
			{
				ID:          "job-repo-b",
				MachineID:   "node-2",
				Engine:      model.EngineOpenCode,
				Status:      model.StatusDone,
				Title:       "Task Beta",
				Repo:        "owner/repo-b",
				Project:     "repo-b",
				Prompt:      "Refactor legacy database access layer",
				StartedAt:   t1,
				EndedAt:     t2,
				RequestedBy: "bob@remote",
				Progress:    100,
			},
		},
		logs: map[string][]model.LogEntry{
			"job-repo-a": {{JobID: "job-repo-a", Message: "step 1"}},
		},
	}
	defer st.Close()

	m := tui.NewModel(st)
	var tm tea.Model = m
	tm, _ = tm.Update(tea.WindowSizeMsg{Width: 120, Height: 45})

	// Initial grouping should be GroupDefault
	if m.GroupingForTest() != tui.GroupDefault {
		t.Fatalf("expected initial grouping to be GroupDefault, got %v", m.GroupingForTest())
	}

	// Navigate selection to job-repo-b
	for i := 0; i < len(m.JobsForTest()); i++ {
		if m.JobsForTest()[m.SelectedJobIdxForTest()].ID == "job-repo-b" {
			break
		}
		tm, _ = tm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	}
	if m.JobsForTest()[m.SelectedJobIdxForTest()].ID != "job-repo-b" {
		t.Fatalf("failed to navigate to job-repo-b")
	}

	// Press 'g': GroupDefault -> GroupRepo
	tm, _ = tm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}})
	if m.GroupingForTest() != tui.GroupRepo {
		t.Fatalf("expected GroupRepo after 'g', got %v", m.GroupingForTest())
	}
	// Selection must remain on job-repo-b
	if m.JobsForTest()[m.SelectedJobIdxForTest()].ID != "job-repo-b" {
		t.Fatalf("expected selection preserved on job-repo-b, got %s", m.JobsForTest()[m.SelectedJobIdxForTest()].ID)
	}

	// View must contain repo headers and fallback for legacy job
	viewRepo := tm.View()
	if !strings.Contains(viewRepo, "owner/repo-a") || !strings.Contains(viewRepo, "owner/repo-b") {
		t.Errorf("expected view to contain repo names, got:\n%s", viewRepo)
	}
	if !strings.Contains(viewRepo, "未知/未标注") {
		t.Errorf("expected view to contain fallback header '未知/未标注' for legacy job, got:\n%s", viewRepo)
	}

	// Press 'g' again: GroupRepo -> GroupMachine
	tm, _ = tm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}})
	if m.GroupingForTest() != tui.GroupMachine {
		t.Fatalf("expected GroupMachine after 'g', got %v", m.GroupingForTest())
	}
	if m.JobsForTest()[m.SelectedJobIdxForTest()].ID != "job-repo-b" {
		t.Fatalf("expected selection preserved on job-repo-b, got %s", m.JobsForTest()[m.SelectedJobIdxForTest()].ID)
	}
	viewMach := tm.View()
	if !strings.Contains(viewMach, "node-1") || !strings.Contains(viewMach, "node-2") {
		t.Errorf("expected view to contain machine IDs, got:\n%s", viewMach)
	}

	// Press 'g': GroupMachine -> GroupStatus
	tm, _ = tm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}})
	if m.GroupingForTest() != tui.GroupStatus {
		t.Fatalf("expected GroupStatus, got %v", m.GroupingForTest())
	}

	// Press 'g': GroupStatus -> GroupEngine
	tm, _ = tm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}})
	if m.GroupingForTest() != tui.GroupEngine {
		t.Fatalf("expected GroupEngine, got %v", m.GroupingForTest())
	}

	// Press 'g': GroupEngine -> GroupStarted
	tm, _ = tm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}})
	if m.GroupingForTest() != tui.GroupStarted {
		t.Fatalf("expected GroupStarted, got %v", m.GroupingForTest())
	}

	// Press 'g': GroupStarted -> GroupRequested
	tm, _ = tm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}})
	if m.GroupingForTest() != tui.GroupRequested {
		t.Fatalf("expected GroupRequested, got %v", m.GroupingForTest())
	}
	viewReq := tm.View()
	if !strings.Contains(viewReq, "alice@company.internal") || !strings.Contains(viewReq, "bob@remote") {
		t.Errorf("expected view to contain requested_by usernames, got:\n%s", viewReq)
	}

	// Press 'g': GroupRequested -> GroupDefault
	tm, _ = tm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}})
	if m.GroupingForTest() != tui.GroupDefault {
		t.Fatalf("expected cycle back to GroupDefault, got %v", m.GroupingForTest())
	}

	// Press 'G' (shift-g): cycle backwards GroupDefault -> GroupRequested
	tm, _ = tm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'G'}})
	if m.GroupingForTest() != tui.GroupRequested {
		t.Fatalf("expected GroupRequested after 'G', got %v", m.GroupingForTest())
	}

	// Navigate to job-repo-a to inspect detail box
	for i := 0; i < len(m.JobsForTest()); i++ {
		if m.JobsForTest()[m.SelectedJobIdxForTest()].ID == "job-repo-a" {
			break
		}
		tm, _ = tm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	}
	if m.JobsForTest()[m.SelectedJobIdxForTest()].ID != "job-repo-a" {
		t.Fatalf("failed to navigate to job-repo-a")
	}

	viewDetail := tm.View()
	if !strings.Contains(viewDetail, "Prompt: \"Implement secret sauce algorithm\"") {
		t.Errorf("expected detail box to display prompt, got:\n%s", viewDetail)
	}
	if !strings.Contains(viewDetail, "owner/repo-a") {
		t.Errorf("expected detail box to display repo, got:\n%s", viewDetail)
	}
	if !strings.Contains(viewDetail, "alice@company.internal") {
		t.Errorf("expected detail box to display requested_by, got:\n%s", viewDetail)
	}
}

func TestTUIModel_LiveUpdateAndContinuousGrouping(t *testing.T) {
	st := &fakeStore{}
	defer st.Close()
	m := tui.NewModel(st)

	t0 := time.Now().UTC()
	jobs := []model.Job{
		{
			ID:        "job-1",
			MachineID: "node-1",
			Engine:    model.EngineAgy,
			Status:    model.StatusRunning,
			Repo:      "org/beta",
			StartedAt: t0,
		},
		{
			ID:        "job-2",
			MachineID: "node-1",
			Engine:    model.EngineAgy,
			Status:    model.StatusRunning,
			Repo:      "org/gamma",
			StartedAt: t0,
		},
		{
			ID:        "job-3",
			MachineID: "node-1",
			Engine:    model.EngineAgy,
			Status:    model.StatusRunning,
			Repo:      "org/zeta",
			StartedAt: t0,
		},
	}
	m.SetJobsForTest(jobs)

	// Set terminal size
	var tm tea.Model = m
	tm, _ = tm.Update(tea.WindowSizeMsg{Width: 120, Height: 30})

	// Switch to GroupRepo: press 'g'
	tm, _ = tm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}})
	if m.GroupingForTest() != tui.GroupRepo {
		t.Fatalf("expected GroupRepo, got %v", m.GroupingForTest())
	}

	// Navigate cursor to job-2
	for i := 0; i < len(m.JobsForTest()); i++ {
		if m.JobsForTest()[m.SelectedJobIdxForTest()].ID == "job-2" {
			break
		}
		tm, _ = tm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	}
	if m.JobsForTest()[m.SelectedJobIdxForTest()].ID != "job-2" {
		t.Fatalf("failed to select job-2")
	}

	// 1. Receive EventJobUpdated for existing job-3: changes repo from org/zeta to org/alpha
	// In GroupRepo, org/alpha must sort BEFORE org/beta and org/gamma!
	updatedJob3 := jobs[2]
	updatedJob3.Repo = "org/alpha"
	tm, _ = tm.Update(model.Event{
		Type: model.EventJobUpdated,
		Job:  &updatedJob3,
	})

	// Assert job list was re-sorted according to new group order
	firstJob := m.JobsForTest()[0]
	if firstJob.ID != "job-3" || firstJob.Repo != "org/alpha" {
		t.Errorf("expected job-3 (org/alpha) to be reordered to front, got %+v", firstJob)
	}

	// Assert selection preserved on job-2!
	if m.JobsForTest()[m.SelectedJobIdxForTest()].ID != "job-2" {
		t.Errorf("expected selected job to remain job-2, got %s (idx %d)",
			m.JobsForTest()[m.SelectedJobIdxForTest()].ID, m.SelectedJobIdxForTest())
	}

	// 2. Receive EventJobUpdated for a NEW job: job-new with repo "org/00-first"
	newJob := model.Job{
		ID:        "job-new",
		MachineID: "node-1",
		Engine:    model.EngineAgy,
		Status:    model.StatusRunning,
		Repo:      "org/00-first",
		StartedAt: t0,
	}
	tm, _ = tm.Update(model.Event{
		Type: model.EventJobUpdated,
		Job:  &newJob,
	})

	// Assert newly added job was sorted into correct place (first)
	if m.JobsForTest()[0].ID != "job-new" {
		t.Errorf("expected job-new at index 0 after grouping, got %s", m.JobsForTest()[0].ID)
	}

	// Assert selection still preserved on job-2!
	if m.JobsForTest()[m.SelectedJobIdxForTest()].ID != "job-2" {
		t.Errorf("expected selected job to remain job-2 after adding new job, got %s (idx %d)",
			m.JobsForTest()[m.SelectedJobIdxForTest()].ID, m.SelectedJobIdxForTest())
	}

	// 3. Continuous sequential updates (simulating live broker stream)
	for i := 0; i < 5; i++ {
		liveJob := model.Job{
			ID:        fmt.Sprintf("job-live-%d", i),
			MachineID: "node-2",
			Engine:    model.EngineOpenCode,
			Status:    model.StatusRunning,
			Repo:      fmt.Sprintf("org/stream-%d", i),
			StartedAt: t0.Add(time.Duration(i) * time.Minute),
		}
		tm, _ = tm.Update(model.Event{
			Type: model.EventJobUpdated,
			Job:  &liveJob,
		})

		// Selection must remain on job-2 throughout all live events
		if m.JobsForTest()[m.SelectedJobIdxForTest()].ID != "job-2" {
			t.Fatalf("iteration %d: selection lost, got %s instead of job-2",
				i, m.JobsForTest()[m.SelectedJobIdxForTest()].ID)
		}
	}
}

func TestTUIModel_CJKAndEmojiTruncationAndNarrowWindow(t *testing.T) {
	st := &fakeStore{}
	defer st.Close()
	m := tui.NewModel(st)

	t0 := time.Now().UTC()
	jobs := []model.Job{
		{
			ID:          "job-cjk-1",
			MachineID:   "node-cjk",
			Engine:      model.EngineAgy,
			Status:      model.StatusRunning,
			Title:       "优化数据库连接池与查询性能处理长标题测试",
			Repo:        "mayingjie/微服务核心组件仓",
			Project:     "微服务核心组件",
			Prompt:      "优化数据库连接池与查询性能，确保高并发下不发生死锁或连接泄漏 🚀🔥",
			Cwd:         "/Users/张三/开发工程/🚀/agentpulse-cli",
			Command:     "agy run --prompt '优化数据库连接池' --concurrency=10",
			RequestedBy: "张三@公司内网",
			StartedAt:   t0,
			DurationStr: "12m 30s",
		},
		{
			ID:          "job-emoji-2",
			MachineID:   "node-gpu",
			Engine:      model.EngineOpenCode,
			Status:      model.StatusDone,
			Title:       "🚀🔥模型微调与量化评估✨🎉",
			Repo:        "org/🚀-ai-models",
			Prompt:      "Fine-tune LLaMA-3 with LoRA and QLoRA 🚀🔥🎉",
			Cwd:         "/home/ubuntu/models/🚀",
			Command:     "opencode train --config lora.yaml",
			RequestedBy: "ai-engineer@remote",
			StartedAt:   t0.Add(-time.Hour),
			EndedAt:     t0,
			DurationStr: "1h 0s",
		},
	}
	m.SetJobsForTest(jobs)

	// Test a matrix of narrow and normal terminal window sizes
	sizes := []struct {
		w, h int
	}{
		{w: 120, h: 30},
		{w: 60, h: 20},
		{w: 40, h: 15},
		{w: 25, h: 10},
		{w: 15, h: 8},
		{w: 10, h: 5},
	}

	var tm tea.Model = m
	for _, sz := range sizes {
		t.Run(fmt.Sprintf("%dx%d", sz.w, sz.h), func(t *testing.T) {
			tm, _ = tm.Update(tea.WindowSizeMsg{Width: sz.w, Height: sz.h})

			// View rendering must never panic, even in extremely narrow terminals
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("render panicked at size %dx%d: %v", sz.w, sz.h, r)
				}
			}()

			view := tm.View()
			if !utf8.ValidString(view) {
				t.Fatalf("View output is not valid UTF-8 at size %dx%d", sz.w, sz.h)
			}
		})
	}
}
