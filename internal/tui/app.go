package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mayingjie198448/agentpulse-cli/internal/model"
	"github.com/mayingjie198448/agentpulse-cli/internal/store"
	"github.com/mayingjie198448/agentpulse-cli/internal/version"
)

// ActivePane defines which pane currently receives keyboard navigation focus
type ActivePane int

const (
	PaneJobs ActivePane = iota
	PaneLogs
)

type tickMsg time.Time

// Model is the Bubble Tea application state for `agentpulse watch`.
type Model struct {
	store   store.Store
	ctx     context.Context
	cancel  context.CancelFunc
	eventCh <-chan model.Event
	paused  bool

	// Dimensions
	width  int
	height int

	// Data
	machines       []model.Machine
	jobs           []model.Job
	selectedJobIdx int
	logs           map[string][]model.LogEntry

	// Views
	activePane      ActivePane
	viewport        viewport.Model
	autoScroll      bool
	ready           bool
	brokerConnected bool
}

// BrokerConnected returns true if the MQTT broker is currently connected.
func (m *Model) BrokerConnected() bool {
	return m.brokerConnected
}

// ActivePane returns the currently active pane.
func (m *Model) ActivePane() ActivePane {
	return m.activePane
}

// AutoScroll returns true if live stream following is active.
func (m *Model) AutoScroll() bool {
	return m.autoScroll
}

// NewModel creates a new TUI model connected to the given store.
func NewModel(st store.Store) *Model {
	ctx, cancel := context.WithCancel(context.Background())
	m := &Model{
		store:           st,
		ctx:             ctx,
		cancel:          cancel,
		paused:          false,
		selectedJobIdx:  0,
		logs:            make(map[string][]model.LogEntry),
		activePane:      PaneJobs,
		autoScroll:      true,
		brokerConnected: false,
	}

	if st != nil {
		m.brokerConnected = st.IsConnected()
	}

	// Fetch initial state
	if machines, err := st.ListMachines(ctx); err == nil {
		m.machines = machines
	}
	if jobs, err := st.ListJobs(ctx); err == nil {
		m.jobs = jobs
		for _, j := range jobs {
			if lgs, err := st.GetLogs(ctx, j.ID, 50); err == nil {
				m.logs[j.ID] = lgs
			}
		}
	}

	if subCh, err := st.Subscribe(ctx); err == nil {
		m.eventCh = subCh
	}

	return m
}

func waitForEvent(ch <-chan model.Event) tea.Cmd {
	return func() tea.Msg {
		if ch == nil {
			return nil
		}
		ev, ok := <-ch
		if !ok {
			return nil
		}
		return ev
	}
}

func clockTick() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

// Init sets up initial commands
func (m *Model) Init() tea.Cmd {
	return tea.Batch(
		waitForEvent(m.eventCh),
		clockTick(),
	)
}

// Update handles incoming messages and user input
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			m.cancel()
			return m, tea.Quit

		case "tab":
			if m.activePane == PaneJobs {
				m.activePane = PaneLogs
			} else {
				m.activePane = PaneJobs
			}

		case "enter":
			if m.activePane == PaneJobs && len(m.jobs) > 0 {
				m.autoScroll = true
				m.ensureLogsLoadedForSelectedJob()
				m.updateViewportContent()
				m.activePane = PaneLogs
			} else if m.activePane == PaneLogs {
				m.activePane = PaneJobs
			}

		case "space", " ":
			m.paused = !m.paused

		case "a":
			m.autoScroll = !m.autoScroll
			if m.autoScroll {
				m.viewport.GotoBottom()
			}

		case "c":
			if len(m.jobs) > 0 {
				curJob := m.jobs[m.selectedJobIdx]
				m.logs[curJob.ID] = []model.LogEntry{}
				m.updateViewportContent()
			}

		// Job selection navigation
		case "up", "k":
			if m.activePane == PaneJobs && len(m.jobs) > 0 {
				if m.selectedJobIdx > 0 {
					m.selectedJobIdx--
					m.autoScroll = true
					m.ensureLogsLoadedForSelectedJob()
					m.updateViewportContent()
				}
			} else if m.activePane == PaneLogs {
				m.viewport.LineUp(1)
				m.autoScroll = false
			}

		case "down", "j":
			if m.activePane == PaneJobs && len(m.jobs) > 0 {
				if m.selectedJobIdx < len(m.jobs)-1 {
					m.selectedJobIdx++
					m.autoScroll = true
					m.ensureLogsLoadedForSelectedJob()
					m.updateViewportContent()
				}
			} else if m.activePane == PaneLogs {
				m.viewport.LineDown(1)
			}

		case "pgup":
			m.viewport.HalfViewUp()
			m.autoScroll = false

		case "pgdown":
			m.viewport.HalfViewDown()
		}

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height

		// Header is ~4 lines, footer is ~2 lines, borders ~4 lines
		availHeight := m.height - 15
		if availHeight < 8 {
			availHeight = 8
		}

		rightWidth := m.width - 44
		if rightWidth < 30 {
			rightWidth = 30
		}

		if !m.ready {
			m.viewport = viewport.New(rightWidth, availHeight)
			m.viewport.SetContent("")
			m.ready = true
		} else {
			m.viewport.Width = rightWidth
			m.viewport.Height = availHeight
		}
		m.updateViewportContent()

	case model.Event:
		if !m.paused {
			switch msg.Type {
			case model.EventMachineUpdated:
				if msg.Machine != nil && msg.Machine.ID != "" {
					found := false
					for i, existing := range m.machines {
						if existing.ID == msg.Machine.ID {
							m.machines[i] = *msg.Machine
							found = true
							break
						}
					}
					if !found {
						m.machines = append(m.machines, *msg.Machine)
					}
				}

			case model.EventJobUpdated:
				if msg.Job != nil && msg.Job.ID != "" {
					found := false
					for i, existing := range m.jobs {
						if existing.ID == msg.Job.ID {
							m.jobs[i] = *msg.Job
							found = true
							break
						}
					}
					if !found {
						m.jobs = append(m.jobs, *msg.Job)
					}
					if len(m.jobs) > 0 && m.selectedJobIdx < len(m.jobs) && m.jobs[m.selectedJobIdx].ID == msg.Job.ID {
						m.updateViewportContent()
					}
				}

			case model.EventLogAdded:
				if msg.Log != nil {
					m.logs[msg.Log.JobID] = append(m.logs[msg.Log.JobID], *msg.Log)
					if len(m.jobs) > 0 && m.selectedJobIdx < len(m.jobs) && m.jobs[m.selectedJobIdx].ID == msg.Log.JobID {
						m.updateViewportContent()
					}
				}
			}
		}
		// Continue waiting for events
		cmds = append(cmds, waitForEvent(m.eventCh))

	case tickMsg:
		if m.store != nil {
			m.brokerConnected = m.store.IsConnected()
		}
		cmds = append(cmds, clockTick())
	}

	if m.activePane == PaneLogs {
		var vpCmd tea.Cmd
		m.viewport, vpCmd = m.viewport.Update(msg)
		cmds = append(cmds, vpCmd)
	}

	return m, tea.Batch(cmds...)
}

func (m *Model) ensureLogsLoadedForSelectedJob() {
	if len(m.jobs) == 0 || m.selectedJobIdx >= len(m.jobs) {
		return
	}
	curJob := m.jobs[m.selectedJobIdx]
	if _, ok := m.logs[curJob.ID]; !ok && m.store != nil {
		if lgs, err := m.store.GetLogs(m.ctx, curJob.ID, 50); err == nil {
			m.logs[curJob.ID] = lgs
		}
	}
}

func (m *Model) updateViewportContent() {
	if len(m.jobs) == 0 {
		m.viewport.SetContent("No active jobs.")
		return
	}
	curJob := m.jobs[m.selectedJobIdx]
	entries := m.logs[curJob.ID]

	var sb strings.Builder
	if len(entries) == 0 {
		sb.WriteString(lipgloss.NewStyle().Foreground(ColorDimText).Italic(true).Render("No log entries recorded yet for this job."))
	} else {
		for _, e := range entries {
			timeStr := lipgloss.NewStyle().Foreground(ColorDimText).Render(e.Timestamp.Format("15:04:05"))
			var lvlStr string
			switch e.Level {
			case model.LevelInfo:
				lvlStr = lipgloss.NewStyle().Foreground(lipgloss.Color("#38bdf8")).Render("[INFO ]")
			case model.LevelWarn:
				lvlStr = lipgloss.NewStyle().Foreground(ColorAmber).Bold(true).Render("[WARN ]")
			case model.LevelError:
				lvlStr = lipgloss.NewStyle().Foreground(ColorRed).Bold(true).Render("[ERROR]")
			case model.LevelAgent:
				lvlStr = lipgloss.NewStyle().Foreground(ColorGreen).Bold(true).Render("[AGENT]")
			case model.LevelTool:
				lvlStr = lipgloss.NewStyle().Foreground(ColorSecondary).Bold(true).Render("[TOOL ]")
			default:
				lvlStr = fmt.Sprintf("[%s]", e.Level)
			}
			msgStr := lipgloss.NewStyle().Foreground(ColorLightText).Render(e.Message)
			sb.WriteString(fmt.Sprintf("%s %s %s\n", timeStr, lvlStr, msgStr))
		}
	}

	m.viewport.SetContent(sb.String())
	if m.autoScroll {
		m.viewport.GotoBottom()
	}
}

// View renders the TUI layout
func (m *Model) View() string {
	if m.width == 0 {
		return "Initializing AgentPulse..."
	}

	header := m.renderHeader()
	body := m.renderBody()
	footer := m.renderFooter()

	return AppContainerStyle.Render(
		lipgloss.JoinVertical(lipgloss.Left, header, body, footer),
	)
}

// JobStatusGlyph returns a stylized status glyph and label for the given job.
func JobStatusGlyph(j model.Job) string {
	if j.NeedsAttention() {
		if j.Status == model.StatusFailed {
			return lipgloss.NewStyle().Foreground(ColorRed).Bold(true).Render("✖ FAIL")
		}
		return lipgloss.NewStyle().Foreground(ColorAmber).Bold(true).Render("▲ ATTN")
	}
	switch j.Status {
	case model.StatusRunning:
		return lipgloss.NewStyle().Foreground(ColorGreen).Bold(true).Render(fmt.Sprintf("● %2.0f%%", j.Progress))
	case model.StatusDone:
		return lipgloss.NewStyle().Foreground(ColorPrimary).Bold(true).Render("✓ DONE")
	case model.StatusQueued:
		return lipgloss.NewStyle().Foreground(ColorMuted).Render("◌ WAIT")
	default:
		return lipgloss.NewStyle().Foreground(ColorDimText).Render(string(j.Status))
	}
}

func (m *Model) renderHeader() string {
	logo := HeaderLogoStyle.Render("⚡ AgentPulse")
	ver := HeaderVersionStyle.Render(version.Version)
	target := HeaderSubtitleStyle.Render("Cross-Machine Monitor • agy & OpenCode")

	var runningCount, doneCount, failedCount, attentionCount int
	for _, j := range m.jobs {
		if j.NeedsAttention() {
			attentionCount++
		}
		switch j.Status {
		case model.StatusRunning:
			runningCount++
		case model.StatusDone:
			doneCount++
		case model.StatusFailed:
			failedCount++
		}
	}

	onlineMachines := 0
	for _, mach := range m.machines {
		if mach.Online {
			onlineMachines++
		}
	}

	brokerOnline := m.brokerConnected
	if m.store != nil {
		brokerOnline = m.store.IsConnected()
	}

	var brokerBadge string
	if brokerOnline {
		brokerBadge = lipgloss.NewStyle().Foreground(ColorGreen).Bold(true).Render("Broker: ● connected")
	} else {
		brokerBadge = lipgloss.NewStyle().Foreground(ColorRed).Bold(true).Render("Broker: ○ disconnected")
	}

	var attentionText string
	if attentionCount > 0 {
		attentionText = lipgloss.NewStyle().Foreground(ColorRed).Bold(true).Render(fmt.Sprintf("%d needs attention", attentionCount))
	} else {
		attentionText = lipgloss.NewStyle().Foreground(ColorDimText).Render("0 needs attention")
	}

	statsText := fmt.Sprintf(
		"Nodes: %d/%d online  |  Jobs: %s  %s  %s  |  %s",
		onlineMachines, len(m.machines),
		lipgloss.NewStyle().Foreground(ColorGreen).Render(fmt.Sprintf("%d running", runningCount)),
		attentionText,
		lipgloss.NewStyle().Foreground(ColorPrimary).Render(fmt.Sprintf("%d done", doneCount)),
		brokerBadge,
	)
	stats := HeaderStatsStyle.Render(statsText)

	clock := lipgloss.NewStyle().Foreground(ColorDimText).Render(time.Now().Format("2006-01-02 15:04:05"))

	topRow := lipgloss.JoinHorizontal(lipgloss.Center, logo, ver, target)
	statusRow := lipgloss.JoinHorizontal(lipgloss.Center, stats, "     ", clock)

	headerBox := lipgloss.NewStyle().
		Border(lipgloss.NormalBorder(), false, false, true, false).
		BorderForeground(ColorBorder).
		PaddingBottom(1).
		Width(m.width - 2).
		Render(lipgloss.JoinVertical(lipgloss.Left, topRow, statusRow))

	return headerBox
}

func (m *Model) renderBody() string {
	leftColWidth := 44
	rightColWidth := m.width - leftColWidth - 4
	if rightColWidth < 30 {
		rightColWidth = 30
	}

	// Left column: Machines + Job selector
	leftCol := lipgloss.JoinVertical(lipgloss.Left,
		m.renderMachinesBox(leftColWidth),
		m.renderJobsBox(leftColWidth),
	)

	// Right column: Current Job details + Logs viewer
	rightCol := lipgloss.JoinVertical(lipgloss.Left,
		m.renderJobDetailBox(rightColWidth),
		m.renderLogsBox(rightColWidth),
	)

	return lipgloss.JoinHorizontal(lipgloss.Top, leftCol, rightCol)
}

func (m *Model) renderMachinesBox(width int) string {
	var b strings.Builder
	title := BoxTitleStyle.Render("🖥  MACHINES & NODES")
	b.WriteString(title + "\n")

	for _, mach := range m.machines {
		statusBadge := OnlineBadge
		if !mach.Online {
			statusBadge = OfflineBadge
		}

		nameStyle := lipgloss.NewStyle().Bold(true).Foreground(ColorLightText)
		name := nameStyle.Render(fmt.Sprintf("%-10s", mach.Name))
		heartbeat := lipgloss.NewStyle().Foreground(ColorDimText).Render(fmt.Sprintf("%-8s", mach.HeartbeatAgo()))
		jobCount := lipgloss.NewStyle().Foreground(ColorHighlight).Render(fmt.Sprintf("[%d jobs]", mach.JobCount))

		b.WriteString(fmt.Sprintf("%s %s %s %s\n", statusBadge, name, heartbeat, jobCount))
	}

	style := BoxStyle.Width(width)
	return style.Render(b.String())
}

func (m *Model) renderJobsBox(width int) string {
	var b strings.Builder
	title := BoxTitleStyle.Render("⚡ JOBS (j/k 移动, enter 追踪)")
	b.WriteString(title + "\n")

	if len(m.jobs) == 0 {
		b.WriteString(lipgloss.NewStyle().Foreground(ColorDimText).Render("No active jobs"))
	} else {
		for i, j := range m.jobs {
			isSelected := i == m.selectedJobIdx

			var engineBadge string
			if j.Engine == model.EngineAgy {
				engineBadge = AgyBadge
			} else {
				engineBadge = OpenCodeBadge
			}

			statusText := JobStatusGlyph(j)

			idStr := fmt.Sprintf("%-12s", j.ID)
			machStr := lipgloss.NewStyle().Foreground(ColorDimText).Render(fmt.Sprintf("@%-7s", j.MachineID))

			line := fmt.Sprintf("%s %s %s %s", engineBadge, idStr, machStr, statusText)

			if isSelected {
				indicator := lipgloss.NewStyle().Foreground(ColorPrimary).Bold(true).Render("▶ ")
				line = ListItemSelected.Render(indicator + line)
			} else {
				indicator := "  "
				line = ListItemNormal.Render(indicator + line)
			}
			b.WriteString(line + "\n")
		}
	}

	style := BoxStyle.Width(width)
	if m.activePane == PaneJobs {
		style = BoxFocusedStyle.Width(width)
	}
	return style.Render(b.String())
}

func (m *Model) renderJobDetailBox(width int) string {
	if len(m.jobs) == 0 {
		return BoxStyle.Width(width).Render("No job selected")
	}

	curJob := m.jobs[m.selectedJobIdx]
	var b strings.Builder

	// Top row: ID, Engine, Machine, Status
	title := lipgloss.NewStyle().Bold(true).Foreground(ColorLightText).Render(fmt.Sprintf("JOB: %s (%s)", curJob.Title, curJob.ID))
	var engBadge string
	if curJob.Engine == model.EngineAgy {
		engBadge = AgyBadge
	} else {
		engBadge = OpenCodeBadge
	}

	var statusBadge string
	switch curJob.Status {
	case model.StatusRunning:
		statusBadge = StatusRunningBadge
	case model.StatusDone:
		statusBadge = StatusDoneBadge
	case model.StatusFailed:
		statusBadge = StatusFailedBadge
	default:
		statusBadge = JobStatusGlyph(curJob)
	}

	metaRow := fmt.Sprintf("%s  Engine: %s  Node: %s  Status: %s",
		title, engBadge,
		lipgloss.NewStyle().Bold(true).Foreground(ColorHighlight).Render(curJob.MachineID),
		statusBadge,
	)
	b.WriteString(metaRow + "\n")

	// Info row: CWD and Command
	cwdText := lipgloss.NewStyle().Foreground(ColorDimText).Render("Working Dir: ") +
		lipgloss.NewStyle().Foreground(ColorLightText).Render(curJob.Cwd)
	cmdText := lipgloss.NewStyle().Foreground(ColorDimText).Render("Command: ") +
		lipgloss.NewStyle().Foreground(ColorLightText).Render(curJob.Command)
	b.WriteString(cwdText + "\n" + cmdText + "\n")

	// Progress bar
	progressBar := renderProgressBar(curJob.Progress, width-16, curJob.Status)
	durationText := lipgloss.NewStyle().Foreground(ColorDimText).Render(fmt.Sprintf("Elapsed: %s", curJob.DurationStr))
	b.WriteString(fmt.Sprintf("%s   %s\n", progressBar, durationText))

	if curJob.NeedsAttention() {
		msg := "Attention: 该任务需要人工跟进"
		if curJob.Error != "" {
			msg = fmt.Sprintf("Attention: %s", curJob.Error)
		} else if curJob.Status == model.StatusFailed {
			msg = "Attention: 任务执行失败"
		}
		attentionBanner := lipgloss.NewStyle().
			Bold(true).
			Foreground(ColorRed).
			Render("⚠ " + msg)
		b.WriteString(attentionBanner + "\n")
	} else if curJob.Error != "" {
		errText := lipgloss.NewStyle().Foreground(ColorRed).Render("Error: " + curJob.Error)
		b.WriteString(errText + "\n")
	}

	return BoxStyle.Width(width).Render(b.String())
}

func (m *Model) renderLogsBox(width int) string {
	var b strings.Builder

	curJobID := "None"
	if len(m.jobs) > 0 {
		curJobID = m.jobs[m.selectedJobIdx].ID
	}

	autoScrollStatus := "[FOLLOWING]"
	if !m.autoScroll {
		autoScrollStatus = "[FOLLOW: OFF]"
	}

	header := fmt.Sprintf("📜 LIVE LOG STREAM (%s)  %s", curJobID,
		lipgloss.NewStyle().Foreground(ColorDimText).Render(autoScrollStatus))
	b.WriteString(BoxTitleStyle.Render(header) + "\n")
	b.WriteString(m.viewport.View())

	style := BoxStyle.Width(width)
	if m.activePane == PaneLogs {
		style = BoxFocusedStyle.Width(width)
	}
	return style.Render(b.String())
}

func (m *Model) renderFooter() string {
	pauseText := "暂停"
	if m.paused {
		pauseText = "继续 (PAUSED)"
	}

	keys := []string{
		fmt.Sprintf("%s %s", FooterKeyStyle.Render("q"), FooterDescStyle.Render("退出")),
		fmt.Sprintf("%s %s", FooterKeyStyle.Render("tab"), FooterDescStyle.Render("切换面板")),
		fmt.Sprintf("%s %s", FooterKeyStyle.Render("j/k"), FooterDescStyle.Render("选择任务")),
		fmt.Sprintf("%s %s", FooterKeyStyle.Render("enter"), FooterDescStyle.Render("追踪/聚焦")),
		fmt.Sprintf("%s %s", FooterKeyStyle.Render("space"), FooterDescStyle.Render(pauseText)),
		fmt.Sprintf("%s %s", FooterKeyStyle.Render("a"), FooterDescStyle.Render("自动滚动")),
		fmt.Sprintf("%s %s", FooterKeyStyle.Render("c"), FooterDescStyle.Render("清空日志")),
	}

	bar := strings.Join(keys, "  •  ")
	return FooterContainerStyle.Render(bar)
}

// renderProgressBar draws a colored bar like [████████░░░░░░░░] 50%
func renderProgressBar(pct float64, totalWidth int, status model.JobStatus) string {
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	if totalWidth < 15 {
		totalWidth = 15
	}

	barWidth := totalWidth - 10
	if barWidth < 5 {
		barWidth = 5
	}

	filledLen := int((pct / 100.0) * float64(barWidth))
	if filledLen > barWidth {
		filledLen = barWidth
	}
	emptyLen := barWidth - filledLen

	var fillChar = "█"
	var emptyChar = "░"

	var barColor lipgloss.Color = ColorGreen
	if status == model.StatusFailed {
		barColor = ColorRed
	} else if status == model.StatusDone {
		barColor = ColorPrimary
	}

	filledStr := lipgloss.NewStyle().Foreground(barColor).Render(strings.Repeat(fillChar, filledLen))
	emptyStr := lipgloss.NewStyle().Foreground(ColorBorder).Render(strings.Repeat(emptyChar, emptyLen))
	pctStr := lipgloss.NewStyle().Bold(true).Foreground(ColorLightText).Render(fmt.Sprintf("%5.1f%%", pct))

	return fmt.Sprintf("[%s%s] %s", filledStr, emptyStr, pctStr)
}
