// Command agentpulse-test-helper is the reviewed stand-in for POSIX shell
// fixtures. It never starts a child process and never launches an agent.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"time"
)

func main() {
	// Optional self-recording. The path is explicit; this does not read HOME
	// or credentials. The file is this process's os.Args, including argv0.
	if out := os.Getenv("AGENTPULSE_TEST_ARGV_OUT"); out != "" {
		if b, err := json.Marshal(os.Args); err == nil {
			_ = os.WriteFile(out, append(b, '\n'), 0o600)
		}
	}

	mode := "true"
	if len(os.Args) > 1 {
		mode = os.Args[1]
	}
	// A leading engine separator is a product failure. Exit 2 and do not
	// interpret the rest as a mode. Later "--" tokens are ordinary args.
	if mode == "--" {
		os.Exit(2)
	}
	switch mode {
	case "true":
		os.Exit(0)
	case "record-argv":
		// record-argv <exitDecimal> [payload args...]
		// Records os.Args above, then exits with the requested code.
		// Does not spawn a process.
		code := 0
		if len(os.Args) > 2 {
			n, err := strconv.Atoi(os.Args[2])
			if err != nil || n < 0 || n > 255 {
				os.Exit(2)
			}
			code = n
		}
		os.Exit(code)
	case "auth-failure":
		// Stay alive long enough that a missing fail-fast would be visible.
		// This process itself is the only process; nothing is spawned.
		fmt.Println("error getting token source: You are not logged into Antigravity.")
		time.Sleep(5 * time.Second)
		os.Exit(0)
	case "storage-notice":
		fmt.Println("Using file-based token storage because SSH session detected")
		fmt.Println("Task completed")
		os.Exit(0)
	default:
		fmt.Fprintf(os.Stderr, "unknown helper mode %q\n", mode)
		os.Exit(2)
	}
}
