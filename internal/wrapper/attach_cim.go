package wrapper

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// cimCreatedLayout matches the CreationDate string rendered by the
// Win32_Process CIM query. CIM datetimes without a zone are local time.
const cimCreatedLayout = "2006-01-02 15:04:05"

// parseWindowsProcessCSV parses the CSV emitted by the Win32_Process query
// (columns Pid,Ppid,Created,Cmd).
func parseWindowsProcessCSV(out []byte) ([]ProcessInfo, error) {
	r := csv.NewReader(bytes.NewReader(out))
	r.FieldsPerRecord = -1
	records, err := r.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("parse process query output: %w", err)
	}
	var procs []ProcessInfo
	for i, rec := range records {
		if i == 0 || len(rec) < 4 {
			continue
		}
		pid, err := strconv.Atoi(strings.TrimSpace(rec[0]))
		if err != nil || pid <= 0 {
			continue
		}
		ppid, _ := strconv.Atoi(strings.TrimSpace(rec[1]))
		info := ProcessInfo{PID: pid, PPID: ppid}
		if created := strings.TrimSpace(rec[2]); created != "" {
			if t, err := time.ParseInLocation(cimCreatedLayout, created, time.Local); err == nil {
				info.StartTime = t.UTC()
			}
		}
		if cmd := rec[3]; strings.TrimSpace(cmd) != "" {
			info.Args = tokenizeWindowsCommandLine(cmd)
		}
		procs = append(procs, info)
	}
	return procs, nil
}

// tokenizeWindowsCommandLine splits a Windows command line: double quotes
// group arguments and backslashes are literal path separators, not escapes.
func tokenizeWindowsCommandLine(s string) []string {
	var args []string
	var cur strings.Builder
	inQuote := false
	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch {
		case ch == '"':
			inQuote = !inQuote
		case (ch == ' ' || ch == '\t') && !inQuote:
			if cur.Len() > 0 {
				args = append(args, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteByte(ch)
		}
	}
	if cur.Len() > 0 {
		args = append(args, cur.String())
	}
	return args
}

// ParseWindowsProcessCSVForTest exposes parseWindowsProcessCSV for testing.
func ParseWindowsProcessCSVForTest(out []byte) ([]ProcessInfo, error) {
	return parseWindowsProcessCSV(out)
}

// TokenizeWindowsCommandLineForTest exposes tokenizeWindowsCommandLine for testing.
func TokenizeWindowsCommandLineForTest(s string) []string {
	return tokenizeWindowsCommandLine(s)
}
