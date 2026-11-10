package safety

import (
	"fmt"
	"time"
)

// CleanupOutcome is the controller result for one delete attempt.
// Result is "deleted" or "BLOCKED". BLOCKED always means the root was kept.
type CleanupOutcome struct {
	Result  string
	Targets []string
	Reason  string
}

// CleanupOptions selects the ownership registry. It has no path taken from HOME.
type CleanupOptions struct {
	RegistryDir string
	AuditPath   string
	RealHome    string
}

// CleanupRun deletes the root recorded for runID, or keeps it.
// The target path is read from the registry this process wrote at creation.
// HOME and USERPROFILE are not consulted.
func CleanupRun(runID string, opts CleanupOptions) (CleanupOutcome, error) {
	if !runIDRe.MatchString(runID) {
		out := CleanupOutcome{Result: "BLOCKED", Reason: "run id is not a recorded identifier"}
		_ = AppendAudit(opts.AuditPath, cleanupAudit(RunRecord{}, out, opts.RealHome))
		return out, fmt.Errorf("%w: %s", ErrCleanupBlocked, out.Reason)
	}
	rec, err := loadRegistry(opts.RegistryDir, runID)
	if err != nil {
		if man, merr := loadManifest(opts.RegistryDir, runID); merr == nil {
			out, rerr := manifestRecovery(man)
			_ = AppendAudit(opts.AuditPath, cleanupAudit(RunRecord{RunID: man.RunID, Root: man.Root, ProfileName: man.ProfileName, Home: ""}, out, opts.RealHome))
			return out, rerr
		}
		out := CleanupOutcome{Result: "BLOCKED", Reason: err.Error()}
		_ = AppendAudit(opts.AuditPath, cleanupAudit(rec, out, opts.RealHome))
		return out, err
	}
	out, err := deleteOwned(rec, protectedHomes(opts.RealHome))
	_ = AppendAudit(opts.AuditPath, cleanupAudit(rec, out, opts.RealHome))
	return out, err
}

func cleanupAudit(rec RunRecord, out CleanupOutcome, realHome string) AuditEvent {
	if realHome == "" {
		realHome = RealHome()
	}
	ev := AuditEvent{
		Phase:            "cleanup",
		Authorization:    "ownership_record",
		RunID:            rec.RunID,
		RunRoot:          rec.Root,
		RealHome:         realHome,
		TestHome:         rec.Home,
		IsolationBackend: "not_applicable",
		FSWrite:          "not_applicable",
		CredentialIPC:    "BLOCKED",
		Network:          "BLOCKED_NOT_CONFINED",
		CleanupResult:    out.Result,
		CleanupReason:    out.Reason,
		CleanupTargets:   out.Targets,
		ControllerResult: out.Result,
		ExitCode:         0,
	}
	if out.Result != "deleted" {
		ev.ExitCode = ExitRefused
		ev.ControllerResult = "BLOCKED"
	}
	ev.TimestampUTC, ev.TimestampShanghai = formatTimes(time.Now())
	return ev
}
