// Command c4-argv-wire-driver prints the reviewed argv-wire entry.
// It does not launch the test PE or the helper. --execute exits 2.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

func main() {
	for _, arg := range os.Args[1:] {
		if arg == "--execute" || arg == "-execute" {
			fmt.Fprintln(os.Stderr, "c4-argv-wire-driver refuses to launch tests")
			os.Exit(2)
		}
	}
	tests := []string{
		"TestDispatchWireChildArgv_PositionalSeparator",
		"TestDispatchWireChildArgv_CommandArgFlags",
		"TestDispatchWireChildArgv_HistoricalLeadingSeparator",
		"TestDispatchWireChildArgv_HistoricalEmbeddedCommand",
		"TestDispatchWireChildArgv_ExitCode",
	}
	rx := "^(" + strings.Join(tests, "|") + ")$"
	plan := map[string]any{
		"protocol":           "ProcessStartInfo.ArgumentList",
		"shell":              "none",
		"execute_authorized": false,
		"import_path":        "./internal/cli",
		"tests":              tests,
		"regexp":             rx,
		"list_args":          []string{"-test.list=" + rx},
		"run_args":           []string{"-test.v", "-test.run=" + rx, "-test.count=1", "-test.parallel=1", "-test.failfast", "-test.timeout=120s"},
		"helper_env":         "AGENTPULSE_TEST_HELPER=absolute path of agentpulse-test-helper.exe from this same SHA",
		"not_mqtt_network":   true,
		"not_offline21":      true,
		"notes": []string{
			"Do not run the rest of ./internal/cli.",
			"Do not re-run the offline21 names.",
			"Redirect USERPROFILE, HOME, APPDATA, LOCALAPPDATA, TEMP, and TMP to this run's directory before the PE starts.",
			"In-memory mock publish is not an MQTT broker test. The helper process is the real child.",
			"These tests replace only the UDP IP probe and the git lookup. Production defaults still dial and still run git.",
			"Child environment is an explicit synthetic list, not the parent credential environment.",
			"Config reads use the synthetic --config / AgentConfigFile, not the user profile.",
		},
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(plan); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
