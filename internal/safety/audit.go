package safety

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// AuditEvent is one JSON line stored outside the run root.
// ChildExit stays nil when the user command was not executed.
// ControllerResult is never flattened into that exit code.
type AuditEvent struct {
	TimestampUTC      string   `json:"timestamp_utc"`
	TimestampShanghai string   `json:"timestamp_asia_shanghai"`
	RunID             string   `json:"run_id,omitempty"`
	Phase             string   `json:"phase,omitempty"`
	Authorization     string   `json:"authorization"`
	Argv              []string `json:"argv"`
	DangerMode        bool     `json:"danger_mode"`
	RealHome          string   `json:"real_home"`
	TestHome          string   `json:"test_home,omitempty"`
	RunRoot           string   `json:"run_root,omitempty"`
	ExeSHA256         string   `json:"exe_sha256,omitempty"`
	ProfileHash       string   `json:"profile_hash,omitempty"`
	IsolationBackend  string   `json:"isolation_backend"`
	IsolationApplied  bool     `json:"isolation_applied"`
	IsolationDetail   string   `json:"isolation_detail,omitempty"`
	// SeatbeltProfile is the exact profile passed to sandbox-exec. It survives
	// run-root cleanup because the audit file lives outside that root.
	SeatbeltProfile string `json:"seatbelt_profile,omitempty"`
	// LaunchStage is the controller stage that refused. It is not a Seatbelt result.
	LaunchStage string `json:"launch_stage,omitempty"`
	// ChildStderr is a capped, redacted prefix of the child stderr.
	ChildStderr           string                 `json:"child_stderr,omitempty"`
	FSWrite               string                 `json:"fs_write"`
	CredentialIPC         string                 `json:"credential_ipc"`
	Network               string                 `json:"network"`
	BlockedSurfaces       []string               `json:"blocked_surfaces,omitempty"`
	ChildExit             *int                   `json:"child_exit"`
	ControllerResult      string                 `json:"controller_result"`
	HelperExit            *int                   `json:"helper_exit,omitempty"`
	CleanupTargets        []string               `json:"cleanup_targets,omitempty"`
	CleanupResult         string                 `json:"cleanup_result,omitempty"`
	CleanupReason         string                 `json:"cleanup_reason,omitempty"`
	ExitCode              int                    `json:"exit_code"`
	WindowsLaunchMetadata *WindowsLaunchMetadata `json:"windows_launch_metadata,omitempty"`
}

// WindowsPathCategory is a closed classifier for Windows path shapes.
type WindowsPathCategory string

const (
	PathCategoryEmpty         WindowsPathCategory = "empty"
	PathCategoryRelative      WindowsPathCategory = "relative"
	PathCategoryDriveAbsolute WindowsPathCategory = "drive_absolute"
	PathCategoryDriveRelative WindowsPathCategory = "drive_relative"
	PathCategoryRootRelative  WindowsPathCategory = "root_relative"
	PathCategoryUNC           WindowsPathCategory = "unc"
	PathCategoryNTDevice      WindowsPathCategory = "nt_device"
	PathCategoryUnknown       WindowsPathCategory = "unknown"
)

// WindowsLaunchMetadata is a strictly closed-typed, redacted snapshot of
// parameters and observations at the Windows CreateProcess launch boundary.
type WindowsLaunchMetadata struct {
	CreationFlags                uint32              `json:"creation_flags"`
	StartupInfoCb                uint32              `json:"startup_info_cb"`
	StartupInfoExSize            uint32              `json:"startup_info_ex_size"`
	SecurityCapabilitiesSize     uint32              `json:"security_capabilities_size"`
	PopulatedAttributeCount      int                 `json:"populated_attribute_count"`
	SecurityCapabilitiesAttrSize uint32              `json:"security_capabilities_attr_size"`
	HandleListAttrSize           uint32              `json:"handle_list_attr_size"`
	HandleListCount              int                 `json:"handle_list_count"`
	CapabilityCount              uint32              `json:"capability_count"`
	CapabilityReserved           uint32              `json:"capability_reserved"`
	SIDPresent                   bool                `json:"sid_present"`
	EnvPointerProvided           bool                `json:"env_pointer_provided"`
	EnvUTF16UnitCount            int                 `json:"env_utf16_unit_count"`
	EnvDoubleNULValid            bool                `json:"env_double_nul_valid"`
	EnvStructureValid            bool                `json:"env_structure_valid"`
	EnvSorted                    bool                `json:"env_sorted"`
	ParentSystemRootPresent      bool                `json:"parent_systemroot_present"`
	ParentSystemRootNonEmpty     bool                `json:"parent_systemroot_nonempty"`
	ParentLocalAppDataPresent    bool                `json:"parent_localappdata_present"`
	ParentLocalAppDataNonEmpty   bool                `json:"parent_localappdata_nonempty"`
	ChildSystemRootAnyPresent    bool                `json:"child_systemroot_any_present"`
	ChildSystemRootAnyNonEmpty   bool                `json:"child_systemroot_any_nonempty"`
	ChildLocalAppDataAnyPresent  bool                `json:"child_localappdata_any_present"`
	ChildLocalAppDataAnyNonEmpty bool                `json:"child_localappdata_any_nonempty"`
	AppPathCategory              WindowsPathCategory `json:"app_path_category"`
	AppPathUTF16Length           int                 `json:"app_path_utf16_length"`
	CurrentDirCategory           WindowsPathCategory `json:"current_dir_category"`
	CurrentDirUTF16Length        int                 `json:"current_dir_utf16_length"`
}

var auditMu sync.Mutex

var urlUserinfo = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://)([^/\s:@]*):([^@\s/]+)@`)

// RedactArgv strips credential-shaped values. Flag names are kept.
func RedactArgv(argv []string) []string {
	out := make([]string, len(argv))
	for i := 0; i < len(argv); i++ {
		a := argv[i]
		if eq := strings.IndexByte(a, '='); eq > 0 && sensitiveKey(a[:eq]) {
			out[i] = a[:eq+1] + "[REDACTED]"
			continue
		}
		out[i] = redactString(a)
		if sensitiveKey(a) && i+1 < len(argv) {
			i++
			out[i] = "[REDACTED]"
		}
	}
	return out
}

func sensitiveKey(k string) bool {
	k = strings.ToLower(strings.TrimLeft(k, "-"))
	switch k {
	case "password", "mqtt-password", "token", "secret", "api-key", "api-token",
		"access-token", "refresh-token":
		return true
	}
	return strings.Contains(k, "password") || strings.Contains(k, "secret") || strings.HasSuffix(k, "token")
}

func redactString(s string) string {
	s = urlUserinfo.ReplaceAllString(s, "${1}${2}:[REDACTED]@")
	for _, key := range []string{"password", "token", "secret", "mqtt_password", "api_key"} {
		s = redactAssignment(s, key)
	}
	return s
}

// redactDiagnostic removes credential assignments entirely. redactString leaves
// "mqtt-password=[REDACTED]", which still contains "password=" and would make
// AppendAudit refuse to store the refusal line.
func redactDiagnostic(s string) string {
	s = redactString(s)
	low := strings.ToLower(s)
	for _, needle := range []string{"password=", "token=", "secret="} {
		for {
			i := strings.Index(low, needle)
			if i < 0 {
				break
			}
			start := i
			for start > 0 && s[start-1] != ' ' && s[start-1] != '\n' && s[start-1] != '"' {
				start--
			}
			end := i + len(needle)
			for end < len(s) && s[end] != ' ' && s[end] != '\n' && s[end] != '"' {
				end++
			}
			s = s[:start] + "[REDACTED]" + s[end:]
			low = strings.ToLower(s)
		}
	}
	return s
}

func redactAssignment(s, key string) string {
	low := strings.ToLower(s)
	needle := key + "="
	from := 0
	for from < len(low) {
		rel := strings.Index(low[from:], needle)
		if rel < 0 {
			return s
		}
		i := from + rel
		start := i + len(needle)
		end := start
		for end < len(s) && s[end] != '&' && s[end] != ' ' && s[end] != '\n' {
			end++
		}
		s = s[:start] + "[REDACTED]" + s[end:]
		low = strings.ToLower(s)
		// The key itself still contains needle ("password=[REDACTED]").
		// Continue after this replacement instead of replacing it forever.
		from = start + len("[REDACTED]")
		if from > len(low) {
			from = len(low)
		}
	}
	return s
}

// AppendAudit writes one redacted JSON line. The path must sit outside the run root.
func AppendAudit(path string, ev AuditEvent) error {
	if strings.TrimSpace(path) == "" {
		path = DefaultAuditPath()
	}
	ev.Argv = RedactArgv(ev.Argv)
	ev.IsolationDetail = redactString(ev.IsolationDetail)
	ev.CleanupReason = redactString(ev.CleanupReason)
	ev.SeatbeltProfile = redactDiagnostic(ev.SeatbeltProfile)
	ev.LaunchStage = redactDiagnostic(ev.LaunchStage)
	ev.ChildStderr = redactDiagnostic(ev.ChildStderr)
	if ev.TimestampUTC == "" {
		now := time.Now()
		ev.TimestampUTC, ev.TimestampShanghai = formatTimes(now)
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(ev); err != nil {
		return err
	}
	line := bytes.TrimRight(buf.Bytes(), "\n")
	if bytes.Contains(bytes.ToLower(line), []byte("password=")) {
		return fmt.Errorf("audit line kept a password assignment")
	}
	auditMu.Lock()
	defer auditMu.Unlock()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(line, '\n'))
	return err
}

func formatTimes(t time.Time) (utc, shanghai string) {
	utc = t.UTC().Format(time.RFC3339Nano)
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		loc = time.FixedZone("Asia/Shanghai", 8*3600)
	}
	shanghai = t.In(loc).Format(time.RFC3339Nano)
	return utc, shanghai
}

func boolAuthorization(explicit, fromArgv bool) string {
	switch {
	case explicit:
		return "explicit_opt_in"
	case fromArgv:
		return "argv_danger_flag"
	default:
		return "none"
	}
}
