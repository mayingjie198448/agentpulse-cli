package model

import (
	"fmt"
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
	StartedAt   time.Time  `json:"started_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	DurationStr string     `json:"duration_str,omitempty"`
	Error       string     `json:"error,omitempty"`
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
	EventMachineUpdated EventType = "machine_updated"
	EventJobUpdated     EventType = "job_updated"
	EventLogAdded       EventType = "log_added"
)

// Event carries state changes emitted across machines/jobs.
type Event struct {
	Type      EventType `json:"type"`
	Machine   *Machine  `json:"machine,omitempty"`
	Job       *Job      `json:"job,omitempty"`
	Log       *LogEntry `json:"log,omitempty"`
	Timestamp time.Time `json:"timestamp"`
}
