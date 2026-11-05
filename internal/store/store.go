package store

import (
	"context"

	"github.com/mayingjie198448/agentpulse-cli/internal/model"
)

// Store defines the unified abstraction for retrieving cross-machine agent states
// and subscribing to real-time events.
//
// The product implementation is internal/store/mqtt.Store. Configuration comes
// from config.yaml or explicit CLI flags; there is no in-memory simulation mode.
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

	// ListAnnounces returns all discovered machine announcements across the network.
	ListAnnounces(ctx context.Context) ([]model.MachineAnnounce, error)

	// GetAnnounce returns the announcement for a specific machine ID.
	GetAnnounce(ctx context.Context, machineID string) (*model.MachineAnnounce, error)

	// Subscribe returns an event channel receiving real-time state changes and logs.
	Subscribe(ctx context.Context) (<-chan model.Event, error)

	// IsConnected reports whether the underlying store connection is currently active.
	IsConnected() bool

	// Close shuts down background tickers or network connections cleanly.
	Close() error
}
