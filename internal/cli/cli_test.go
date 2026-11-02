package cli_test

import (
	"bytes"
	"testing"

	"github.com/mayingjie198448/agentpulse-cli/internal/cli"
)

func TestRootCommands(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"version", []string{"version"}},
		{"doctor", []string{"doctor"}},
		{"list", []string{"list"}},
		{"list_json", []string{"list", "--json"}},
		{"logs", []string{"logs", "job-agy-01", "--tail", "5"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := cli.NewRootCmd()
			buf := new(bytes.Buffer)
			cmd.SetOut(buf)
			cmd.SetErr(buf)
			cmd.SetArgs(tt.args)

			if err := cmd.Execute(); err != nil {
				t.Fatalf("unexpected error executing %s: %v", tt.name, err)
			}
		})
	}
}
