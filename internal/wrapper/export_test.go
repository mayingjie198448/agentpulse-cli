package wrapper

import (
	"context"
	"os"
	"os/exec"
)

// IsolationHooks replaces discovery and log IO during tests.
// A nil field keeps the current function. The restore callback puts the
// previous functions back.
type IsolationHooks struct {
	ReadFile       func(string) ([]byte, error)
	Readlink       func(string) (string, error)
	Open           func(string) (*os.File, error)
	ReadDir        func(string) ([]os.DirEntry, error)
	CommandContext func(context.Context, string, ...string) *exec.Cmd
}

// SetIsolationHooksForTest installs hooks and returns a restore function.
func SetIsolationHooksForTest(h IsolationHooks) func() {
	prev := IsolationHooks{
		ReadFile:       readProcFile,
		Readlink:       readProcLink,
		Open:           openTailFile,
		ReadDir:        readProcDir,
		CommandContext: commandContext,
	}
	if h.ReadFile != nil {
		readProcFile = h.ReadFile
	}
	if h.Readlink != nil {
		readProcLink = h.Readlink
	}
	if h.Open != nil {
		openTailFile = h.Open
	}
	if h.ReadDir != nil {
		readProcDir = h.ReadDir
	}
	if h.CommandContext != nil {
		commandContext = h.CommandContext
	}
	return func() {
		readProcFile = prev.ReadFile
		readProcLink = prev.Readlink
		openTailFile = prev.Open
		readProcDir = prev.ReadDir
		commandContext = prev.CommandContext
	}
}

// PSListArgsForTest exposes the ps argument builder.
func PSListArgsForTest(pids []int) []string {
	return psListArgs(normalizePIDs(pids))
}
