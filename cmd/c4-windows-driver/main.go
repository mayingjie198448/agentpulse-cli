package main

import (
	"encoding/json"
	"fmt"
	"os"
)

// planEntry is one ProcessStartInfo launch. Arguments are already split.
type planEntry struct {
	ID         string   `json:"id"`
	ImportPath string   `json:"import_path"`
	Group      string   `json:"group"`
	Tests      []string `json:"tests"`
	Regexp     string   `json:"regexp"`
	ListArgs   []string `json:"list_args"`
	RunArgs    []string `json:"run_args"`
	CWDNote    string   `json:"cwd_note,omitempty"`
}

type plan struct {
	Protocol          string      `json:"protocol"`
	Shell             string      `json:"shell"`
	ExecuteAuthorized bool        `json:"execute_authorized"`
	CWD               string      `json:"cwd"`
	Packages          []planEntry `json:"packages"`
	NotInThisDriver   []string    `json:"not_in_this_driver"`
}

func main() {
	for _, arg := range os.Args[1:] {
		if arg == "--execute" || arg == "-execute" {
			fmt.Fprintln(os.Stderr, "c4-windows-driver refuses to launch tests; use the printed ArgumentList from a reviewed runner")
			os.Exit(2)
		}
	}

	out := plan{
		Protocol:          "ProcessStartInfo.ArgumentList",
		Shell:             "none",
		ExecuteAuthorized: false,
		CWD:               "C4 checkout root that contains docs/bare-cli-attach.md",
		NotInThisDriver: []string{
			"TestListener_EmptyAllowlistDoesNotStartDefaultDiscovery",
			"TestListener_ConcurrentExecution",
			"TestWrapper_AuthFailure_FastFails",
			"TestWrapper_StorageNotice_DoesNotFail",
			"TestUnscopedDiscoveryStillRequestsGlobalPS",
			"TestScanLocalAgents_Hermetic",
			"TestScanLocalAgents_WindowsSyntheticExe",
			"43+45 joint argv regression",
		},
	}
	for _, pkg := range offlinePackages() {
		rx := anchoredRegexp(pkg.Tests)
		entry := planEntry{
			ID:         pkg.ID,
			ImportPath: pkg.Import,
			Group:      pkg.Group,
			Tests:      pkg.Tests,
			Regexp:     rx,
			ListArgs:   listArgs(rx),
			RunArgs:    runArgs(rx),
		}
		if pkg.ID == "wrapper" {
			entry.CWDNote = "working directory must be the C4 module root so TestBareAttachDocExitIsNotAlwaysDone reads docs/bare-cli-attach.md"
		}
		out.Packages = append(out.Packages, entry)
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
