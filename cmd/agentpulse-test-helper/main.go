// Command agentpulse-test-helper is the reviewed stand-in for POSIX shell
// fixtures. It never starts a child process and never launches an agent.
package main

import (
	"fmt"
	"os"
	"time"
)

func main() {
	mode := "true"
	if len(os.Args) > 1 {
		mode = os.Args[1]
	}
	switch mode {
	case "true":
		os.Exit(0)
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
