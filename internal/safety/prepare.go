package safety

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// Request is one launch through the shared trust boundary.
type Request struct {
	Argv           []string
	ExplicitDanger bool
	// Confine isolates even when the danger flag is not requested.
	// It does not add a danger flag. QA harnesses use this.
	Confine bool
	// Environ is the non-confined child environment (already filtered by the caller).
	Environ []string
	// ExtraEnv is merged into a confined child only when the key is not a credential.
	ExtraEnv     map[string]string
	Dir          string
	Stdout       io.Writer
	Stderr       io.Writer
	AuditPath    string
	BaseDir      string
	RegistryDir  string
	RealHome     string
	SentinelPath string
	// PersistedPassJSON / PersistedPassPath are rejected. They never skip a probe.
	PersistedPassJSON []byte
	PersistedPassPath string
	// RequireVerifiedCredentialIPC fails closed. This build cannot prove that surface.
	RequireVerifiedCredentialIPC bool
	Backend                      Backend
	// ProfileHashOverride and ExeHashOverride replace the hash sent to the
	// child. They do not disable the child's recomputation. A mismatch
	// refuses exec. Tests use them to prove stale bindings fail.
	ProfileHashOverride string
	ExeHashOverride     string
	// RetainRoot keeps a verified run root so a test can read project files
	// before CleanupRun. Production leaves this false.
	RetainRoot bool
}

// Prepared is a command that has not been started.
// For a confined launch, Cmd is the isolate helper, not the user binary.
type Prepared struct {
	Cmd      *exec.Cmd
	UserArgv []string
	Danger   bool
	Confined bool
	Work     string
	Root     string
	RunID    string

	ctx        context.Context
	confirmR   *os.File
	confirmW   *os.File
	rec        *RunRecord
	audit      AuditEvent
	auditFile  string
	retainRoot bool
	started    bool
	reaped     bool
	pid        int
}

// Outcome separates the user child from the controller.
type Outcome struct {
	ChildExit        *int
	ControllerResult string
	HelperExit       *int
	Cleanup          CleanupOutcome
	AuditPath        string
}

// Prepare builds a child command. A confined or dangerous request returns
// ErrRefused before process start when isolation, the sentinel, or a fresh
// profile binding is not available. It does not fall back to a bare exec.
func Prepare(ctx context.Context, req Request) (*Prepared, error) {
	if len(req.PersistedPassJSON) > 0 || req.PersistedPassPath != "" {
		return nil, fmt.Errorf("%w: %v", ErrRefused, Authorize(Authorization{
			PersistedPassJSON: req.PersistedPassJSON,
			PersistedPassPath: req.PersistedPassPath,
		}))
	}
	if req.RequireVerifiedCredentialIPC {
		return nil, fmt.Errorf("%w: credential-agent IPC verification is BLOCKED", ErrRefused)
	}
	if len(req.Argv) == 0 || strings.TrimSpace(req.Argv[0]) == "" {
		return nil, fmt.Errorf("%w: empty command", ErrRefused)
	}
	userArgv, explicitShape := Decide(req.Argv, req.ExplicitDanger)
	need := explicitShape || req.Confine || containsDangerFlag(req.Argv)
	auth := boolAuthorization(req.ExplicitDanger, !req.ExplicitDanger && containsDangerFlag(req.Argv))
	if !need {
		cmd := exec.CommandContext(ctx, userArgv[0], userArgv[1:]...)
		cmd.Dir = req.Dir
		if len(req.Environ) > 0 {
			cmd.Env = append([]string{}, req.Environ...)
		}
		cmd.Stdout = req.Stdout
		cmd.Stderr = req.Stderr
		return &Prepared{
			Cmd:      cmd,
			UserArgv: userArgv,
			ctx:      ctx,
			audit: AuditEvent{
				Phase:            "launch",
				Authorization:    auth,
				Argv:             userArgv,
				DangerMode:       false,
				RealHome:         firstNonEmpty(req.RealHome, RealHome()),
				IsolationBackend: "not_requested",
				FSWrite:          "not_requested",
				CredentialIPC:    "not_requested",
				Network:          "not_requested",
				ControllerResult: "not_isolated",
			},
			auditFile: req.AuditPath,
		}, nil
	}

	backend := req.Backend
	if backend == nil {
		backend = platformBackend()
	}
	if err := backend.Available(); err != nil {
		ev := refusedAudit(userArgv, auth, req, backend.Name(), "isolation backend unavailable")
		_ = AppendAudit(req.AuditPath, ev)
		return nil, fmt.Errorf("%w: %v", ErrRefused, err)
	}
	sentinel := req.SentinelPath
	if strings.TrimSpace(sentinel) == "" {
		created, serr := createSyntheticSentinel(req.BaseDir)
		if serr != nil {
			ev := refusedAudit(userArgv, auth, req, backend.Name(), "sentinel create failed")
			_ = AppendAudit(req.AuditPath, ev)
			return nil, fmt.Errorf("%w: %v", ErrRefused, serr)
		}
		sentinel = created
	}
	req.SentinelPath = sentinel
	exeHash, err := exeSHA256()
	if err != nil {
		return nil, fmt.Errorf("%w: executable identity: %v", ErrRefused, err)
	}
	self, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrRefused, err)
	}
	rec, err := createRunRoot(createOpts{
		BaseDir:     req.BaseDir,
		RegistryDir: req.RegistryDir,
		RealHome:    req.RealHome,
		Sentinel:    req.SentinelPath,
		ExeHash:     exeHash,
	})
	if err != nil {
		ev := refusedAudit(userArgv, auth, req, backend.Name(), err.Error())
		_ = AppendAudit(req.AuditPath, ev)
		return nil, fmt.Errorf("%w: %v", ErrRefused, err)
	}
	claimedProfile := rec.ProfileHash
	if req.ProfileHashOverride != "" {
		claimedProfile = req.ProfileHashOverride
	}
	claimedExe := exeHash
	if req.ExeHashOverride != "" {
		claimedExe = req.ExeHashOverride
	}
	auditPath := req.AuditPath
	if auditPath == "" {
		auditPath = DefaultAuditPath()
	}
	if withinPath(rec.Root, auditPath) {
		_, _ = deleteOwned(rec, protectedHomes(req.RealHome))
		return nil, fmt.Errorf("%w: audit path is inside the run root", ErrRefused)
	}

	args := []string{
		ReexecArg,
		"--run-root", rec.Root,
		"--work-root", rec.Work,
		"--run-id", rec.RunID,
		"--root-dev", strconv.FormatUint(rec.Dev, 10),
		"--root-ino", strconv.FormatUint(rec.Ino, 10),
		"--profile-hash", claimedProfile,
		"--exe-hash", claimedExe,
		"--sentinel", rec.Sentinel,
		"--sentinel-dev", strconv.FormatUint(rec.SentinelDev, 10),
		"--sentinel-ino", strconv.FormatUint(rec.SentinelIno, 10),
		"--",
	}
	args = append(args, userArgv...)
	r, w, err := os.Pipe()
	if err != nil {
		_, _ = deleteOwned(rec, protectedHomes(req.RealHome))
		return nil, err
	}
	cmd := exec.CommandContext(ctx, self, args...)
	cmd.Dir = rec.Work
	cmd.Env = cleanChildEnv(os.Environ(), req.ExtraEnv, rec.Home, rec.XDGConfigHome, rec.XDGDataHome, rec.XDGCacheHome, rec.XDGStateHome, rec.Tmp)
	cmd.Stdout = req.Stdout
	cmd.Stderr = req.Stderr
	cmd.ExtraFiles = []*os.File{w}
	detail := "linux landlock write/refer/truncate confined to the run root; abstract unix sockets and signals scoped; credential-agent IPC and network confinement are BLOCKED and not claimed"
	if backend.Name() != "linux-landlock" {
		detail = backend.Name() + " selected; fresh probe still required"
	}
	return &Prepared{
		Cmd:      cmd,
		UserArgv: userArgv,
		Danger:   explicitShape,
		Confined: true,
		Work:     rec.Work,
		Root:     rec.Root,
		RunID:    rec.RunID,
		ctx:      ctx,
		confirmR: r,
		confirmW: w,
		rec:      &rec,
		audit: AuditEvent{
			Phase:            "launch",
			Authorization:    auth,
			Argv:             userArgv,
			DangerMode:       explicitShape,
			RealHome:         firstNonEmpty(req.RealHome, RealHome()),
			TestHome:         rec.Home,
			RunRoot:          rec.Root,
			ExeSHA256:        exeHash,
			ProfileHash:      rec.ProfileHash,
			IsolationBackend: backend.Name(),
			IsolationDetail:  detail,
			FSWrite:          "pending",
			CredentialIPC:    "BLOCKED",
			Network:          "BLOCKED_NOT_CONFINED",
			BlockedSurfaces:  BlockedSurfaceList(),
			ControllerResult: "pending",
		},
		auditFile:  auditPath,
		retainRoot: req.RetainRoot,
	}, nil
}

func refusedAudit(argv []string, auth string, req Request, backend, detail string) AuditEvent {
	code := ExitRefused
	return AuditEvent{
		Phase:            "launch",
		Authorization:    auth,
		Argv:             argv,
		DangerMode:       true,
		RealHome:         firstNonEmpty(req.RealHome, RealHome()),
		IsolationBackend: backend,
		IsolationApplied: false,
		IsolationDetail:  detail,
		FSWrite:          "failed",
		CredentialIPC:    "BLOCKED",
		Network:          "BLOCKED_NOT_CONFINED",
		BlockedSurfaces:  BlockedSurfaceList(),
		ControllerResult: "refused",
		ExitCode:         code,
	}
}

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

// Start starts the process. For a confined command it waits until the helper
// confirms the fresh probe, and it does not treat a failed probe as a running
// user command.
func (p *Prepared) Start() error {
	if p.Cmd == nil {
		return fmt.Errorf("%w: nothing to start", ErrRefused)
	}
	if err := p.Cmd.Start(); err != nil {
		p.finishRefused(err.Error(), nil)
		return fmt.Errorf("%w: %v", ErrRefused, err)
	}
	p.started = true
	if p.Cmd.Process != nil {
		p.pid = p.Cmd.Process.Pid
	}
	if p.confirmW != nil {
		_ = p.confirmW.Close()
		p.confirmW = nil
	}
	if p.confirmR == nil {
		return nil
	}
	if err := p.readConfirm(); err != nil {
		return fmt.Errorf("%w: %v", ErrRefused, err)
	}
	p.audit.IsolationApplied = true
	p.audit.FSWrite = "verified"
	return nil
}

func (p *Prepared) readConfirm() error {
	buf := make([]byte, 512)
	type got struct {
		n   int
		err error
	}
	ch := make(chan got, 1)
	go func() {
		n, err := p.confirmR.Read(buf)
		ch <- got{n, err}
	}()
	var n int
	var readErr error
	if p.ctx != nil {
		select {
		case <-p.ctx.Done():
			if p.Cmd.Process != nil {
				_ = p.Cmd.Process.Kill()
			}
			_ = p.Cmd.Wait()
			p.reaped = true
			p.finishRefused("verification canceled", helperPtr(HelperExitVerify))
			return fmt.Errorf("verification canceled")
		case g := <-ch:
			n, readErr = g.n, g.err
		}
	} else {
		g := <-ch
		n, readErr = g.n, g.err
	}
	msg := strings.TrimSpace(string(buf[:n]))
	if strings.HasPrefix(msg, "OK") {
		return nil
	}
	if !p.reaped {
		_ = p.Cmd.Wait()
		p.reaped = true
	}
	if msg == "" {
		msg = "helper closed confirm pipe before OK"
		if readErr != nil {
			msg = readErr.Error()
		}
	}
	p.finishRefused(msg, helperPtr(HelperExitVerify))
	return fmt.Errorf("verification failed before user command: %s", msg)
}

// Pid is the running process id after a successful Start.
func (p *Prepared) Pid() int { return p.pid }

// Wait waits for the user command, writes the audit line, and deletes a
// confined root only when ownership still matches.
func (p *Prepared) Wait() (Outcome, error) {
	var waitErr error
	if p.started && !p.reaped && p.Cmd != nil {
		waitErr = p.Cmd.Wait()
		p.reaped = true
	}
	out := Outcome{AuditPath: p.auditPath()}
	if !p.Confined {
		code, ok := exitCodeOf(waitErr)
		if ok {
			out.ChildExit = &code
			p.audit.ChildExit = &code
			p.audit.ExitCode = code
		}
		p.audit.ControllerResult = "not_isolated"
		_ = AppendAudit(p.auditPath(), p.audit)
		out.ControllerResult = p.audit.ControllerResult
		return out, waitErr
	}
	code, ok := exitCodeOf(waitErr)
	if ok {
		out.ChildExit = &code
		p.audit.ChildExit = &code
		p.audit.ExitCode = code
	} else {
		p.audit.ExitCode = ExitRefused
	}
	p.audit.ControllerResult = "isolated"
	p.audit.IsolationApplied = true
	p.audit.FSWrite = "verified"
	if p.rec != nil && !p.retainRoot {
		cout, cerr := deleteOwned(*p.rec, protectedHomes(p.audit.RealHome))
		out.Cleanup = cout
		p.audit.CleanupResult = cout.Result
		p.audit.CleanupReason = cout.Reason
		p.audit.CleanupTargets = cout.Targets
		if cerr != nil && cout.Result != "deleted" {
			p.audit.CleanupResult = "BLOCKED"
		}
	} else if p.retainRoot && p.rec != nil {
		p.audit.CleanupResult = "retained"
		p.audit.CleanupReason = "caller retained the owned root for inspection"
	}
	_ = AppendAudit(p.auditPath(), p.audit)
	out.ControllerResult = p.audit.ControllerResult
	return out, waitErr
}

func (p *Prepared) finishRefused(detail string, helper *int) {
	p.audit.ControllerResult = "refused"
	p.audit.IsolationApplied = false
	p.audit.FSWrite = "failed"
	p.audit.IsolationDetail = detail
	p.audit.HelperExit = helper
	p.audit.ExitCode = ExitRefused
	p.audit.ChildExit = nil
	if p.rec != nil {
		cout, _ := deleteOwned(*p.rec, protectedHomes(p.audit.RealHome))
		p.audit.CleanupResult = cout.Result
		p.audit.CleanupReason = cout.Reason
		p.audit.CleanupTargets = cout.Targets
		if cout.Result != "deleted" {
			p.audit.CleanupResult = "BLOCKED"
		}
	}
	_ = AppendAudit(p.auditPath(), p.audit)
	p.rec = nil
}

func (p *Prepared) auditPath() string { return p.auditFile }

func helperPtr(v int) *int { return &v }

func exitCodeOf(err error) (int, bool) {
	if err == nil {
		return 0, true
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode(), true
	}
	return 0, false
}
