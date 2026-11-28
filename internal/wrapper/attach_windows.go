//go:build windows

package wrapper

import (
	"context"
	"fmt"
	"strings"

	"golang.org/x/sys/windows"
)

// windowsProcessQuery builds the Win32_Process CIM query. A non-empty PID
// list restricts the query to those PIDs so allowlisted discovery never
// reads other processes' command lines.
func windowsProcessQuery(pids []int) string {
	source := "Get-CimInstance Win32_Process"
	if len(pids) > 0 {
		terms := make([]string, len(pids))
		for i, pid := range pids {
			terms[i] = fmt.Sprintf("ProcessId=%d", pid)
		}
		source += " -Filter \"" + strings.Join(terms, " OR ") + "\""
	}
	return source + " | Select-Object @{n='Pid';e={[string]$_.ProcessId}}, @{n='Ppid';e={[string]$_.ParentProcessId}}, @{n='Created';e={if ($_.CreationDate) { $_.CreationDate.ToString('yyyy-MM-dd HH:mm:ss') } else { '' }}}, @{n='Cmd';e={[string]$_.CommandLine}} | ConvertTo-Csv -NoTypeInformation"
}

// windowsProcessList discovers processes via CIM because Windows has no ps
// in the default PATH. The command goes through commandContext (safety
// boundary); CreationDate supplies the start time, so no ps lstart probe is
// needed on the default attach path.
func windowsProcessList(ctx context.Context, pids []int) ([]ProcessInfo, error) {
	cmd := commandContext(ctx, "powershell", "-NoProfile", "-NonInteractive", "-Command", windowsProcessQuery(pids))
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to run Windows process query: %w", err)
	}
	return parseWindowsProcessCSV(out)
}

func windowsProcessAlive(pid int) bool {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		// Access denied means the process exists but is protected.
		return err == windows.ERROR_ACCESS_DENIED
	}
	defer windows.CloseHandle(h)
	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		return false
	}
	return code == stillActive
}
