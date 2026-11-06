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

// BrokerConnectedForTest returns the broker connection state for test assertions.
func (m *Model) BrokerConnectedForTest() bool {
	return m.brokerConnected
}

// ActivePaneForTest returns the active pane for test assertions.
func (m *Model) ActivePaneForTest() ActivePane {
	return m.activePane
}

// AutoScrollForTest returns the autoscroll state for test assertions.
func (m *Model) AutoScrollForTest() bool {
	return m.autoScroll
}

// JobsScrollOffsetForTest returns the current jobs scroll offset for test assertions.
func (m *Model) JobsScrollOffsetForTest() int {
	return m.jobsScrollOffset
}

// ConfirmDeleteForTest returns the confirmation dialog state for test assertions.
func (m *Model) ConfirmDeleteForTest() bool {
	return m.confirmDelete
}

// DeleteErrorForTest returns the current delete error message for test assertions.
func (m *Model) DeleteErrorForTest() string {
	return m.deleteError
}
