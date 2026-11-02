package store

import (
	"context"

	"github.com/mayingjie198448/agentpulse-cli/internal/model"
)

// Store defines the unified abstraction for retrieving cross-machine agent states
// and subscribing to real-time events.
//
// In Phase 1, this interface is satisfied by internal/mock.MockStore.
// In future phases, an MQTT-backed implementation (e.g. internal/mqtt.Store)
// or gRPC/HTTP adapters will implement this interface to connect to distributed
// agy daemons and OpenCode runners across different machines without changing
// the CLI commands or TUI view layer.
type Store interface {
	// ListMachines returns all registered machines and their current statuses.
	ListMachines(ctx context.Context) ([]model.Machine, error)

	// GetMachine returns a single machine by ID.
	GetMachine(ctx context.Context, id string) (*model.Machine, error)

	// ListJobs returns all active and historical jobs, optionally filtered.
	ListJobs(ctx context.Context) ([]model.Job, error)

	// GetJob returns a specific job by its ID.
	GetJob(ctx context.Context, id string) (*model.Job, error)

	// GetLogs returns the most recent logs for a specific job.
	GetLogs(ctx context.Context, jobID string, limit int) ([]model.LogEntry, error)

	// Subscribe returns an event channel receiving real-time state changes and logs.
	Subscribe(ctx context.Context) (<-chan model.Event, error)

	// Close shuts down background tickers or network connections cleanly.
	Close() error
}
