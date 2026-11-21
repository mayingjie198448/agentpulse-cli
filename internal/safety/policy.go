// Package safety is the shared execution trust boundary for agent launches.
//
// MQTT dispatch, the listener, wrap, and any direct helper must reach a child
// process only through Prepare. A persisted JSON PASS file, a previous run's
// profile, or a host-language assert is not a gate: every dangerous or
// confined launch re-checks the run id, directory identity, profile hash, and
// executable hash inside the child before exec.
//
// Historical note: some revisions of the dual-role daemon set
// DangerouslySkipPermissions true, and some CLI flag defaults were already
// false. This package does not treat that as one universal default. It only
// refuses to add a danger flag unless this call explicitly opts in, and it
// refuses to start that child unless fresh OS write isolation verifies.
// Isolation failure does not retry the command without the sandbox and does
// not add the flag after an error.
//
// Filesystem confinement is not a proof that Keychain, securityd, or another
// credential agent can be prevented from writing on the caller's behalf.
// Those surfaces stay BLOCKED. Network confinement is not claimed.
package safety

import "errors"

const (
	// ReexecArg is the internal child entry. It is not a user-facing command.
	ReexecArg = "--agentpulse-internal-isolate"

	markerName = ".agentpulse-run-marker"

	// HelperExitVerify is the isolate helper's status when the user command
	// was not executed.
	HelperExitVerify = 78

	// ExitRefused is recorded when the controller stops before the user child.
	ExitRefused = 126
)

var (
	// ErrBackendUnavailable means this platform has no verified write-isolation backend.
	ErrBackendUnavailable = errors.New("os write isolation backend unavailable")
	// ErrRefused means the user command was not started.
	ErrRefused = errors.New("refusing to start child")
	// ErrCleanupBlocked means the recorded root was kept.
	ErrCleanupBlocked = errors.New("cleanup blocked; root retained")
	// ErrStaleAuthorization means a stored PASS marker was offered as a gate.
	ErrStaleAuthorization = errors.New("persisted PASS marker is not a security gate")
	// ErrSentinelUnusable means the probe target cannot prove a denial.
	ErrSentinelUnusable = errors.New("sentinel cannot prove denied access")
)

// Authorization is the rejected offline-gate input.
// PersistedPassJSON and PersistedPassPath never authorize a run.
type Authorization struct {
	PersistedPassJSON []byte
	PersistedPassPath string
}

// Authorize rejects stored PASS markers. Success is only a fresh child probe.
func Authorize(a Authorization) error {
	if len(a.PersistedPassJSON) > 0 || a.PersistedPassPath != "" {
		return ErrStaleAuthorization
	}
	return ErrStaleAuthorization
}

// NativeVerification lists surfaces this process must not claim as proven.
// linux_landlock_fs_write is the only surface exercised by the Linux tests.
func NativeVerification() map[string]string {
	return map[string]string{
		"linux_landlock_fs_write":             "exercised on linux when the landlock probe passes",
		"darwin_sandbox_exec":                 "BLOCKED",
		"darwin_mach_securityd_keychain":      "BLOCKED",
		"windows_write_isolation":             "BLOCKED",
		"windows_reparse_junction":            "BLOCKED",
		"windows_appcontainer_native_fixture": "BLOCKED_UNTIL_OPS_EXECUTES",
		"credential_agent_ipc_proxy":          "BLOCKED",
		"network_confinement":                 "BLOCKED",
	}
}

// BlockedSurfaceList is copied into audit records for a confined launch.
func BlockedSurfaceList() []string {
	return []string{
		"darwin_sandbox_exec",
		"darwin_mach_securityd_keychain",
		"windows_write_isolation",
		"windows_reparse_junction",
		"credential_agent_ipc_proxy",
		"network_confinement",
	}
}
