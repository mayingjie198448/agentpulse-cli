package safety

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"
)

// evidenceRetention is the pre-cleanup copy of one run's probe evidence.
// ProfileHash on the launch audit is the run identity. SeatbeltProfileSHA256
// is the sha256 of the exact sandbox-exec -p text.
type evidenceRetention struct {
	Parent      string
	RunRoot     string
	AuditPath   string
	WorkerProbe string
	FinalArgv   []string
	Profile     string
	RunID       string
}

// argvAfterSeparator returns the user arguments after the first "--".
// Those are the arguments after fixture code appends --escape. The launch
// audit snapshot taken in Prepare does not include that later append.
func argvAfterSeparator(args []string) []string {
	for i, arg := range args {
		if arg == "--" {
			out := make([]string, len(args)-i-1)
			copy(out, args[i+1:])
			return out
		}
	}
	return nil
}

// evidencePathAllowed reports whether auditPath is inside the exclusive parent
// and outside the run root. A ".." segment is refused before Clean can walk out.
func evidencePathAllowed(parent, runRoot, auditPath string) error {
	for _, p := range []string{parent, runRoot, auditPath} {
		if strings.TrimSpace(p) == "" {
			return fmt.Errorf("%w: evidence path is empty", ErrRefused)
		}
		for _, part := range strings.Split(filepath.Clean(p), string(filepath.Separator)) {
			if part == ".." {
				return fmt.Errorf("%w: evidence path escapes with ..", ErrRefused)
			}
		}
		raw := strings.Split(p, string(filepath.Separator))
		for _, part := range raw {
			if part == ".." {
				return fmt.Errorf("%w: evidence path escapes with ..", ErrRefused)
			}
		}
		if !filepath.IsAbs(p) {
			return fmt.Errorf("%w: evidence path is not absolute", ErrRefused)
		}
	}
	parent = filepath.Clean(parent)
	runRoot = filepath.Clean(runRoot)
	auditPath = filepath.Clean(auditPath)
	if !withinPath(parent, runRoot) || runRoot == parent {
		return fmt.Errorf("%w: run root is not inside the exclusive parent", ErrRefused)
	}
	if !withinPath(parent, auditPath) || auditPath == parent {
		return fmt.Errorf("%w: audit path is outside the exclusive parent", ErrRefused)
	}
	if withinPath(runRoot, auditPath) {
		return fmt.Errorf("%w: audit path is inside the run root", ErrRefused)
	}
	return nil
}

// retainRunEvidence builds the audit line written before run-root cleanup.
// A missing worker probe, final argv, or -p text is BLOCKED and is not filled
// with invented bytes. The returned event is what AppendAudit will store.
func retainRunEvidence(in evidenceRetention) (AuditEvent, error) {
	if err := evidencePathAllowed(in.Parent, in.RunRoot, in.AuditPath); err != nil {
		return AuditEvent{}, err
	}
	var missing []string
	if strings.TrimSpace(in.WorkerProbe) == "" {
		missing = append(missing, "worker_probe")
	}
	if len(in.FinalArgv) == 0 {
		missing = append(missing, "final_argv")
	}
	if strings.TrimSpace(in.Profile) == "" {
		missing = append(missing, "seatbelt_profile")
	}
	ev := AuditEvent{
		Phase:            "retention",
		RunID:            in.RunID,
		RunRoot:          in.RunRoot,
		Argv:             append([]string{}, in.FinalArgv...),
		FinalArgv:        append([]string{}, in.FinalArgv...),
		WorkerProbe:      in.WorkerProbe,
		SeatbeltProfile:  in.Profile,
		FSWrite:          "not_measured",
		CredentialIPC:    "BLOCKED",
		Network:          "BLOCKED_NOT_CONFINED",
		BlockedSurfaces:  BlockedSurfaceList(),
		IsolationBackend: "darwin-sandbox-exec",
		IsolationApplied: false,
	}
	if len(missing) > 0 {
		ev.ControllerResult = "BLOCKED"
		ev.CleanupReason = "retention missing " + strings.Join(missing, ",")
		ev.WorkerProbe = ""
		ev.FinalArgv = nil
		ev.Argv = nil
		ev.SeatbeltProfile = ""
		return ev, nil
	}
	sum := sha256.Sum256([]byte(in.Profile))
	ev.SeatbeltProfileSHA256 = hex.EncodeToString(sum[:])
	ev.ControllerResult = "retained"
	ev.WorkerProbe = redactDiagnostic(in.WorkerProbe)
	ev.FinalArgv = RedactArgv(in.FinalArgv)
	ev.Argv = append([]string{}, ev.FinalArgv...)
	ev.SeatbeltProfile = redactDiagnostic(in.Profile)
	if strings.Contains(strings.ToLower(ev.WorkerProbe+ev.SeatbeltProfile+strings.Join(ev.FinalArgv, "\n")), "password=") {
		return AuditEvent{}, fmt.Errorf("%w: retention kept a password assignment", ErrRefused)
	}
	return ev, nil
}
