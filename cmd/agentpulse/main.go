package main

import (
	"os"

	"github.com/mayingjie198448/agentpulse-cli/internal/cli"
	"github.com/mayingjie198448/agentpulse-cli/internal/safety"
)

func main() {
	// Confined launches re-exec this binary. The helper exits before cobra.
	safety.MaybeHandleInternal(os.Args)
	cli.Execute()
}
