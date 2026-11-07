package safety

import (
	"context"
	"fmt"
	"io"
)

// Options configures a QA harness. Roots are created by this process and are
// not derived from the mutable HOME environment variable.
type Options struct {
	BaseDir      string
	RegistryDir  string
	AuditPath    string
	RealHome     string
	SentinelPath string
	Backend      Backend
}

// Harness is a confined launcher for acceptance checks.
// Setup failure returns an error and does not run the child on the real home.
type Harness struct {
	Root    string
	Work    string
	Home    string
	RunID   string
	Options Options
}

// NewHarness checks the isolation backend and allocates an owned run root.
// It does not execute a user command and it does not fall back when the
// backend is unavailable.
func NewHarness(opts Options) (*Harness, error) {
	backend := opts.Backend
	if backend == nil {
		backend = platformBackend()
	}
	if err := backend.Available(); err != nil {
		return nil, fmt.Errorf("%w: isolation setup failed: %v", ErrRefused, err)
	}
	if opts.SentinelPath == "" {
		return nil, fmt.Errorf("%w: harness requires an existing sentinel outside the run root", ErrSentinelUnusable)
	}
	exe, err := exeSHA256()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrRefused, err)
	}
	rec, err := createRunRoot(createOpts{
		BaseDir:     opts.BaseDir,
		RegistryDir: opts.RegistryDir,
		RealHome:    opts.RealHome,
		Sentinel:    opts.SentinelPath,
		ExeHash:     exe,
	})
	if err != nil {
		return nil, fmt.Errorf("%w: isolation setup failed: %v", ErrRefused, err)
	}
	return &Harness{
		Root:    rec.Root,
		Work:    rec.Work,
		Home:    rec.Home,
		RunID:   rec.RunID,
		Options: opts,
	}, nil
}

// Environ is the confined environment overlay. It is not sufficient isolation
// by itself; Run still applies the OS backend.
func (h *Harness) Environ() []string {
	return cleanChildEnv(nil, nil, h.Home, h.Home, h.Home, h.Home, h.Home, h.Work)
}

// Run executes argv inside a fresh confined child. Failure does not retry on
// the host environment.
func (h *Harness) Run(ctx context.Context, argv []string, stdout, stderr io.Writer) (Outcome, error) {
	prepared, err := Prepare(ctx, Request{
		Argv:         argv,
		Confine:      true,
		Stdout:       stdout,
		Stderr:       stderr,
		AuditPath:    h.Options.AuditPath,
		BaseDir:      h.Options.BaseDir,
		RegistryDir:  h.Options.RegistryDir,
		RealHome:     h.Options.RealHome,
		SentinelPath: h.Options.SentinelPath,
		Backend:      h.Options.Backend,
	})
	if err != nil {
		return Outcome{ControllerResult: "refused"}, err
	}
	if err := prepared.Start(); err != nil {
		return Outcome{ControllerResult: "refused", AuditPath: h.Options.AuditPath}, err
	}
	return prepared.Wait()
}

// Cleanup removes this harness root when the ownership record still matches.
func (h *Harness) Cleanup() (CleanupOutcome, error) {
	return CleanupRun(h.RunID, CleanupOptions{
		RegistryDir: h.Options.RegistryDir,
		AuditPath:   h.Options.AuditPath,
		RealHome:    h.Options.RealHome,
	})
}
