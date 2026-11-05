package tui

import "github.com/mayingjie198448/agentpulse-cli/internal/model"

// MachinesForTest returns the current internal machines slice for test assertions.
func (m *Model) MachinesForTest() []model.Machine {
	return m.machines
}

// JobsForTest returns the current internal jobs slice for test assertions.
func (m *Model) JobsForTest() []model.Job {
	return m.jobs
}

// SelectedJobIdxForTest returns the current internal selectedJobIdx for test assertions.
func (m *Model) SelectedJobIdxForTest() int {
	return m.selectedJobIdx
}

// LogsForTest returns recorded logs map for test assertions.
func (m *Model) LogsForTest() map[string][]model.LogEntry {
	return m.logs
}
