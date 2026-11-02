package termutil

import (
	"os"

	"golang.org/x/term"
)

// IsTerminal returns true if the provided file descriptor is connected to a terminal.
func IsTerminal(f *os.File) bool {
	return term.IsTerminal(int(f.Fd()))
}

// ShouldUsePlain checks if plain text output should be forced.
// If forcePlain flag is set, or if stdout is not a TTY, or if TERM=dumb, return true.
func ShouldUsePlain(forcePlain bool) bool {
	if forcePlain {
		return true
	}
	if os.Getenv("TERM") == "dumb" {
		return true
	}
	return !IsTerminal(os.Stdout)
}
