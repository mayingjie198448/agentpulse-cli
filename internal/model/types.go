package model

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// EngineType represents the AI agent engine driving the job.
type EngineType string

const (
	EngineAgy      EngineType = "agy"
	EngineOpenCode EngineType = "opencode"
)

// JobStatus represents the execution state of a job.
type JobStatus string

const (
	StatusQueued  JobStatus = "queued"
	StatusRunning JobStatus = "running"
	StatusDone    JobStatus = "done"
	StatusFailed  JobStatus = "failed"
)

// Machine represents a remote node running agy or OpenCode instances.
type Machine struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	Online        bool      `json:"online"`
	LastHeartbeat time.Time `json:"last_heartbeat"`
	Platform      string    `json:"platform"`
	IP            string    `json:"ip,omitempty"`
	JobCount      int       `json:"job_count"`
}

// HeartbeatAgo returns a human-readable duration since the last heartbeat.
func (m Machine) HeartbeatAgo() string {
	diff := time.Since(m.LastHeartbeat)
	if diff < 0 {
		diff = 0
	}
	if diff < time.Minute {
		return fmt.Sprintf("%ds ago", int(diff.Seconds()))
	}
	if diff < time.Hour {
		return fmt.Sprintf("%dm ago", int(diff.Minutes()))
	}
	return fmt.Sprintf("%dh ago", int(diff.Hours()))
}

// Job represents an agentic task executing on a machine.
type Job struct {
	ID          string     `json:"id"`
	MachineID   string     `json:"machine_id"`
	Engine      EngineType `json:"engine"`
	Status      JobStatus  `json:"status"`
	Progress    float64    `json:"progress"` // 0.0 to 100.0
	Cwd         string     `json:"cwd"`
	Command     string     `json:"command"`
	Title       string     `json:"title"`
	Repo        string     `json:"repo,omitempty"`
	Project     string     `json:"project,omitempty"`
	Prompt      string     `json:"prompt,omitempty"` // Sanitized prompt summary (max length bounded, secrets redacted)
	StartedAt   time.Time  `json:"started_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	EndedAt     time.Time  `json:"ended_at,omitempty"`
	DurationStr string     `json:"duration_str,omitempty"`
	RequestedBy string     `json:"requested_by,omitempty"`
	Error       string     `json:"error,omitempty"`
}

// RepoOrProject returns the primary repository or project name for the job.
func (j Job) RepoOrProject() string {
	if r := strings.TrimSpace(j.Repo); r != "" {
		return r
	}
	if p := strings.TrimSpace(j.Project); p != "" {
		return p
	}
	return ""
}

// DisplayTitle returns the human-readable title or falls back to Job ID.
func (j Job) DisplayTitle() string {
	if t := strings.TrimSpace(j.Title); t != "" {
		return t
	}
	return j.ID
}

// MarshalJSON customizes Job JSON encoding to omit ended_at when zero (for running/queued jobs).
func (j Job) MarshalJSON() ([]byte, error) {
	type Alias Job
	if j.EndedAt.IsZero() {
		return json.Marshal(&struct {
			EndedAt *time.Time `json:"ended_at,omitempty"`
			Alias
		}{
			EndedAt: nil,
			Alias:   (Alias)(j),
		})
	}
	return json.Marshal((Alias)(j))
}

// UnmarshalJSON customizes Job JSON decoding to safely handle missing, null, empty string, or zero RFC3339 ended_at.
func (j *Job) UnmarshalJSON(data []byte) error {
	type Alias Job
	aux := struct {
		EndedAtRaw *json.RawMessage `json:"ended_at,omitempty"`
		*Alias
	}{
		Alias: (*Alias)(j),
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	if aux.EndedAtRaw != nil {
		var s string
		if err := json.Unmarshal(*aux.EndedAtRaw, &s); err == nil {
			s = strings.TrimSpace(s)
			if s == "" || s == "0001-01-01T00:00:00Z" {
				j.EndedAt = time.Time{}
			} else {
				t, err := time.Parse(time.RFC3339Nano, s)
				if err != nil {
					t, err = time.Parse(time.RFC3339, s)
				}
				if err == nil {
					j.EndedAt = t
				}
			}
		}
	}
	return nil
}

// LocalStatus represents the on-disk JSON snapshot of local wrapper state.
type LocalStatus struct {
	Machine   Machine   `json:"machine"`
	Job       Job       `json:"job"`
	PID       int       `json:"pid,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

// NeedsAttention reports whether the job requires human attention or follow-up.
// Job states that need follow-up include failed executions, jobs waiting for user input,
// and stuck/error states mapped from existing job status and error fields.
func (j Job) NeedsAttention() bool {
	switch strings.ToLower(string(j.Status)) {
	case string(StatusFailed), "error", "waiting", "waiting_input", "stuck":
		return true
	}
	return j.Error != "" && j.Status != StatusDone
}

// LogLevel denotes the severity or category of a log line.
type LogLevel string

const (
	LevelInfo  LogLevel = "INFO"
	LevelWarn  LogLevel = "WARN"
	LevelError LogLevel = "ERROR"
	LevelAgent LogLevel = "AGENT"
	LevelTool  LogLevel = "TOOL"
)

// LogEntry represents an individual log line emitted by a job.
type LogEntry struct {
	Timestamp time.Time  `json:"timestamp"`
	JobID     string     `json:"job_id"`
	MachineID string     `json:"machine_id"`
	Engine    EngineType `json:"engine"`
	Level     LogLevel   `json:"level"`
	Message   string     `json:"message"`
}

// EventType defines the type of event in real-time streams.
type EventType string

const (
	EventMachineUpdated  EventType = "machine_updated"
	EventJobUpdated      EventType = "job_updated"
	EventJobDeleted      EventType = "job_deleted"
	EventLogAdded        EventType = "log_added"
	EventAnnounceUpdated EventType = "announce_updated"
)

// Event carries state changes emitted across machines/jobs.
type Event struct {
	Type      EventType        `json:"type"`
	Machine   *Machine         `json:"machine,omitempty"`
	Job       *Job             `json:"job,omitempty"`
	Log       *LogEntry        `json:"log,omitempty"`
	Announce  *MachineAnnounce `json:"announce,omitempty"`
	Timestamp time.Time        `json:"timestamp"`
}

// AgentAnnounce represents a single agent announced by a machine.
type AgentAnnounce struct {
	ID     string   `json:"id"`
	Alias  []string `json:"alias"`
	Ok     bool     `json:"ok"`
	Detail string   `json:"detail"`
}

// MachineAnnounce represents an announcement payload published to agentpulse/v1/{machine_id}/announce.
type MachineAnnounce struct {
	V         int             `json:"v"`
	MachineID string          `json:"machine_id"`
	TS        time.Time       `json:"ts"`
	Rev       int             `json:"rev"`
	Hostname  string          `json:"hostname"`
	OS        string          `json:"os"`
	Agents    []AgentAnnounce `json:"agents"`
}

// FindAgent looks up an agent in the announcement by ID or alias.
func (m *MachineAnnounce) FindAgent(nameOrAlias string) (AgentAnnounce, bool) {
	if m == nil {
		return AgentAnnounce{}, false
	}
	nameLower := strings.ToLower(strings.TrimSpace(nameOrAlias))
	for _, ag := range m.Agents {
		if strings.ToLower(ag.ID) == nameLower {
			return ag, true
		}
		for _, a := range ag.Alias {
			if strings.ToLower(a) == nameLower {
				return ag, true
			}
		}
	}
	return AgentAnnounce{}, false
}

// DispatchCommand represents a remote job dispatch instruction (docs/mqtt-protocol.md §4.4).
type DispatchCommand struct {
	CommandID      string            `json:"command_id"`
	JobID          string            `json:"job_id"`
	Engine         EngineType        `json:"engine"`
	Cwd            string            `json:"cwd"`
	Command        string            `json:"command"`
	Args           []string          `json:"args"`
	Title          string            `json:"title"`
	Repo           string            `json:"repo,omitempty"`
	Project        string            `json:"project,omitempty"`
	Prompt         string            `json:"prompt,omitempty"` // Sanitized prompt summary
	TimeoutSeconds int               `json:"timeout_seconds"`
	Env            map[string]string `json:"env,omitempty"`
	RequestedBy    string            `json:"requested_by"`
	Timestamp      time.Time         `json:"timestamp"`
}
