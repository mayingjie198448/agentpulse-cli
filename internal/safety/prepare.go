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
	// Ownership is the creation record for a fixture directory. A path string
	// or a name prefix is not accepted in its place.
	Ownership *CreationRecord
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

	ChildLocalAppDataIsOwnedRoot   bool
	ChildLocalAppDataFromOwnedRoot bool

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
	// startFn, when set, replaces Cmd.Start. Windows AppContainer uses it
	// because os/exec cannot pass PROC_THREAD_ATTRIBUTE_SECURITY_CAPABILITIES.
	startFn func() error
	waitFn  func() error
	killFn  func() error
	// removeFn, when set, replaces deleteOwned. Tests use it to observe cleanup.
	removeFn       func() (CleanupOutcome, error)
	blockedCleanup error
	// seatbeltProfile is the exact profile string handed to sandbox-exec.
	// It is kept on Prepared so a refused launch can audit it after the run
	// root is deleted. It is not a verification result.
	seatbeltProfile string
	launchStage     string
	childStderr     string
	stderrW         *os.File
	stderrDrain     chan struct{}
}

const childStderrLimit = 2048

// rememberSeatbeltProfile stores the profile that configureConfinedLaunch
// will pass to sandbox-exec. The run-root cleanup must not be the only copy.
func rememberSeatbeltProfile(p *Prepared, profile string) {
	if p == nil {
		return
	}
	p.seatbeltProfile = profile
	if p.launchStage == "" {
		p.launchStage = "profile_ready"
	}
}

// beginChildStderrCapture tees the child stderr into a capped buffer.
// The parent write end is closed by the caller after Start so the drain
// finishes when the child exits. It does not read the system log.
func (p *Prepared) beginChildStderrCapture() {
	if p == nil || p.Cmd == nil || p.stderrDrain != nil {
		return
	}
	r, w, err := os.Pipe()
	if err != nil {
		return
	}
	prev := p.Cmd.Stderr
	p.Cmd.Stderr = w
	p.stderrW = w
	done := make(chan struct{})
	p.stderrDrain = done
	go func() {
		defer close(done)
		defer r.Close()
		buf := make([]byte, 512)
		for {
			n, rerr := r.Read(buf)
			if n > 0 {
				p.noteChildStderr(buf[:n])
				if prev != nil {
					_, _ = prev.Write(buf[:n])
				}
			}
			if rerr != nil {
				return
			}
		}
	}()
}

// finishChildStderr closes the parent write end and waits for the drain.
// It is safe to call more than once. It does not wait for a live child:
// the caller must already have closed the child or not started it.
func (p *Prepared) finishChildStderr() {
	if p == nil {
		return
	}
	if p.stderrW != nil {
		_ = p.stderrW.Close()
		p.stderrW = nil
	}
	if p.stderrDrain != nil {
		<-p.stderrDrain
		p.stderrDrain = nil
	}
}

// noteChildStderr keeps a bounded prefix of the child stderr for the refusal
// audit. Bytes past the cap are dropped. This does not read system logs.
func (p *Prepared) noteChildStderr(b []byte) {
	if p == nil || len(b) == 0 || len(p.childStderr) >= childStderrLimit {
		return
	}
	room := childStderrLimit - len(p.childStderr)
	if len(b) > room {
		b = b[:room]
	}
	p.childStderr += string(b)
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
		ExePath:     self,
		Ownership:   req.Ownership,
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
	if rec.ExeSHA256 != "" {
		claimedExe = rec.ExeSHA256
	}
	if req.ExeHashOverride != "" {
		claimedExe = req.ExeHashOverride
	}
	auditPath := req.AuditPath
	if auditPath == "" {
		auditPath = DefaultAuditPath()
	}
	if pathsConflate(rec.Root, auditPath) {
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
	}
	if rec.ProfileName != "" {
		args = append(args, "--profile-name", rec.ProfileName, "--package-sid", rec.PackageSID, "--cap-count", "0")
	}
	args = append(args, "--")
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
	prepared := &Prepared{
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
			ExeSHA256:        claimedExe,
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
	}
	if err := configureConfinedLaunch(prepared); err != nil {
		_, _ = deleteOwned(rec, protectedHomes(req.RealHome))
		return nil, fmt.Errorf("%w: %v", ErrRefused, err)
	}
	return prepared, nil
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

// composeStartError wraps a start failure with ErrRefused while preserving
// underlying causes and diagnostics.
func composeStartError(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%w: %w", ErrRefused, err)
}

// Start starts the process. For a confined command it waits until the helper
// confirms the fresh probe, and it does not treat a failed probe as a running
// user command.
func (p *Prepared) Start() error {
	if p.Cmd == nil && p.startFn == nil {
		return fmt.Errorf("%w: nothing to start", ErrRefused)
	}
	var err error
	if p.startFn != nil {
		err = p.startFn()
	} else {
		p.beginChildStderrCapture()
		err = p.Cmd.Start()
		if p.stderrW != nil {
			_ = p.stderrW.Close()
			p.stderrW = nil
		}
	}
	if err != nil {
		if p.launchStage == "" || p.launchStage == "profile_ready" {
			p.launchStage = "child_start_error"
		}
		p.finishChildStderr()
		p.keepRoot(err)
		p.finishRefused(err.Error(), nil)
		return composeStartError(err)
	}
	if p.launchStage == "" || p.launchStage == "profile_ready" {
		p.launchStage = "child_started"
	}
	p.started = true
	if p.pid == 0 && p.Cmd != nil && p.Cmd.Process != nil {
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
			p.killChild()
			werr := p.waitChild()
			p.keepRoot(werr)
			p.reaped = true
			p.finishRefused("verification canceled", helperPtr(HelperExitVerify))
			if werr != nil {
				return werr
			}
			return fmt.Errorf("verification canceled")
		case g := <-ch:
			n, readErr = g.n, g.err
		}
	} else {
		g := <-ch
		n, readErr = g.n, g.err
	}
	accepted, msg := classifyDarwinConfirmRead(n, readErr, buf)
	if accepted {
		return nil
	}
	var werr error
	if !p.reaped {
		werr = p.waitChild()
		p.keepRoot(werr)
		p.reaped = true
	}
	// Record the status Wait actually returned. HelperExitVerify (78) is what
	// the helper uses when it reaches failVerify; stamping it here hid exits
	// that happened before any confirm byte was written.
	switch {
	case msg == "EOF" || msg == "helper closed confirm pipe before OK":
		p.launchStage = "confirm_pipe_eof"
	default:
		p.launchStage = "confirm_rejected"
	}
	p.finishChildStderr()
	p.finishRefused(msg, observedHelperExit(werr))
	return fmt.Errorf("verification failed before user command: %s", msg)
}

// classifyDarwinConfirmRead decides whether the parent may treat the confirm
// pipe as success. An empty read, including io.EOF, is never success.
func classifyDarwinConfirmRead(n int, readErr error, buf []byte) (accepted bool, detail string) {
	if n < 0 {
		n = 0
	}
	if n > len(buf) {
		n = len(buf)
	}
	msg := strings.TrimSpace(string(buf[:n]))
	if strings.HasPrefix(msg, "OK") {
		return true, ""
	}
	if msg == "" {
		msg = "helper closed confirm pipe before OK"
		if readErr != nil {
			msg = readErr.Error()
		}
	}
	return false, msg
}

// observedHelperExit returns the child status from Wait. A nil error is exit 0.
// Errors without an exit code leave the field unset instead of inventing 78.
func observedHelperExit(waitErr error) *int {
	if code, ok := exitCodeOf(waitErr); ok {
		return helperPtr(code)
	}
	return nil
}

// Pid is the running process id after a successful Start.
func (p *Prepared) Pid() int { return p.pid }

// Wait waits for the user command, writes the audit line, and deletes a
// confined root only when ownership still matches.
func (p *Prepared) Wait() (Outcome, error) {
	var waitErr error
	if p.started && !p.reaped && (p.Cmd != nil || p.waitFn != nil) {
		waitErr = p.waitChild()
		p.reaped = true
	}
	p.finishChildStderr()
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
	if errors.Is(waitErr, ErrDescendantsActive) {
		p.keepRoot(waitErr)
	}
	if p.blockedCleanup != nil {
		p.audit.ControllerResult = "BLOCKED"
		p.audit.CleanupResult = "BLOCKED"
		p.audit.CleanupReason = p.blockedCleanup.Error()
		_ = AppendAudit(p.auditPath(), p.audit)
		out.ControllerResult = p.audit.ControllerResult
		if waitErr == nil {
			waitErr = p.blockedCleanup
		}
		return out, waitErr
	}
	if p.rec != nil && !p.retainRoot {
		cout, cerr := p.removeRoot()
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
	p.audit.LaunchStage = p.launchStage
	p.audit.SeatbeltProfile = p.seatbeltProfile
	p.audit.ChildStderr = p.childStderr
	if p.rec != nil || p.removeFn != nil {
		cout, cerr := p.removeRoot()
		p.audit.CleanupResult = cout.Result
		p.audit.CleanupReason = cout.Reason
		p.audit.CleanupTargets = cout.Targets
		if cerr != nil && p.audit.CleanupReason == "" {
			p.audit.CleanupReason = cerr.Error()
		}
		if cout.Result != "deleted" {
			p.audit.CleanupResult = "BLOCKED"
		}
	}
	_ = AppendAudit(p.auditPath(), p.audit)
	if p.blockedCleanup == nil {
		p.rec = nil
	}
}

func (p *Prepared) keepRoot(err error) {
	if errors.Is(err, ErrDescendantsActive) && p.blockedCleanup == nil {
		p.blockedCleanup = err
	}
}

func (p *Prepared) removeRoot() (CleanupOutcome, error) {
	if p.blockedCleanup != nil {
		return CleanupOutcome{Result: "BLOCKED", Reason: p.blockedCleanup.Error()}, p.blockedCleanup
	}
	if p.removeFn != nil {
		return p.removeFn()
	}
	if p.rec == nil {
		return CleanupOutcome{}, nil
	}
	return deleteOwned(*p.rec, protectedHomes(p.audit.RealHome))
}

func (p *Prepared) auditPath() string { return p.auditFile }

func (p *Prepared) waitChild() error {
	if p.waitFn != nil {
		return p.waitFn()
	}
	if p.Cmd == nil {
		return nil
	}
	return p.Cmd.Wait()
}

func (p *Prepared) killChild() {
	if p.killFn != nil {
		p.killFn()
		return
	}
	if p.Cmd != nil && p.Cmd.Process != nil {
		_ = p.Cmd.Process.Kill()
	}
}

func helperPtr(v int) *int { return &v }

func exitCodeOf(err error) (int, bool) {
	if err == nil {
		return 0, true
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode(), true
	}
	var coded interface{ ExitCode() int }
	if errors.As(err, &coded) {
		return coded.ExitCode(), true
	}
	return 0, false
}
