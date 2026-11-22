// Package main prints the reviewed Windows C4 offline driver plan.
// It does not launch test binaries. Ops starts each PE with
// ProcessStartInfo.ArgumentList, one argument per slice element, and no shell.
package main

import "strings"

// offlinePackage is one test PE and the exact tests allowed in the first batch.
type offlinePackage struct {
	ID     string   `json:"id"`
	Import string   `json:"import_path"`
	Group  string   `json:"group"`
	Tests  []string `json:"tests"`
}

// offlinePackages is the fixed 21-name list in acceptance order.
// Wrapper is one package: the 48 names come before the 43 names.
func offlinePackages() []offlinePackage {
	return []offlinePackage{
		{
			ID:     "autostart",
			Import: "./internal/autostart",
			Group:  "47",
			Tests: []string{
				"TestPublishOffline_EmptyInputs",
				"TestServiceLabelDefaultsAndOverrides",
				"TestAutostartSupportMatchesPlatform",
			},
		},
		{
			ID:     "mqtt",
			Import: "./internal/store/mqtt",
			Group:  "47",
			Tests: []string{
				"TestMQTTStore_Refresh_MaintainerOverlap_DoesNotCommitEmptyOrPartialCache",
				"TestMQTTStore_Refresh_MaintainerOverlap_RejectedRefreshKeepsRetry",
			},
		},
		{
			ID:     "wrapper",
			Import: "./internal/wrapper",
			Group:  "48+43",
			Tests: []string{
				"TestTokenizeCommandLine",
				"TestMatchAgentProcess",
				"TestGenerateAttachJobID",
				"TestParsePIDAllowlist",
				"TestPSListArgsAllowlistIsTargeted",
				"TestBareAttachDocExitIsNotAlwaysDone",
				"TestBuildCommandArgs",
				"TestIsUnauthenticatedOutput",
			},
		},
		{
			ID:     "model",
			Import: "./internal/model",
			Group:  "45",
			Tests: []string{
				"TestJob_ExtendedFieldsAndJSON",
				"TestJob_BackwardCompatibility",
				"TestJob_EndedAtSerialization",
			},
		},
		{
			ID:     "termutil",
			Import: "./internal/termutil",
			Group:  "45",
			Tests: []string{
				"TestTruncateString",
				"TestTruncateLeft",
				"TestPadRight",
			},
		},
		{
			ID:     "config",
			Import: "./internal/config",
			Group:  "44",
			Tests: []string{
				"TestBuiltinEngineDefs",
				"TestAgentConfig_CustomAndBuiltin",
			},
		},
	}
}

func anchoredRegexp(tests []string) string {
	return "^(" + strings.Join(tests, "|") + ")$"
}

func listArgs(rx string) []string {
	return []string{"-test.list=" + rx}
}

func runArgs(rx string) []string {
	return []string{
		"-test.v",
		"-test.run=" + rx,
		"-test.count=1",
		"-test.parallel=1",
		"-test.failfast",
		"-test.timeout=120s",
	}
}
