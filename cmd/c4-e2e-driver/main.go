// Command c4-e2e-driver prepares and, only with --execute, runs one
// loopback MQTT chain: real agentpulse listen --no-attach, real
// agentpulse dispatch, and the stream helper. The broker listens on
// 127.0.0.1 only. Nothing is installed as a service.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"
	"github.com/mayingjie198448/agentpulse-cli/internal/loopbackmqtt"
	"github.com/mayingjie198448/agentpulse-cli/internal/model"
	"github.com/mayingjie198448/agentpulse-cli/internal/store/mqtt"
	"github.com/mayingjie198448/agentpulse-cli/internal/wrapper"
)

// coarseRootOK refuses "/" and drive roots, which can never act as a scratch
// boundary.
func coarseRootOK(path string) bool {
	abs, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	abs = filepath.Clean(abs)
	if abs == "/" {
		return false
	}
	vol := filepath.VolumeName(abs)
	if vol != "" && (abs == vol || abs == vol+string(os.PathSeparator) || abs == vol+"/") {
		return false
	}
	return true
}

// chainLinksOK refuses unless every component this walk observes is a real
// directory. Lstat reports the entry itself and never follows a hop; symlinks,
// irregular files, and Windows reparse points/junctions are refused before
// anything is created. The walk starts at the leaf and steps outwards to the
// volume prefix.
func chainLinksOK(path string) bool {
	prefix := filepath.VolumeName(path)
	if prefix == "" {
		prefix = string(os.PathSeparator)
	}
	for seg := path; ; seg = filepath.Dir(seg) {
		lst, lerr := os.Lstat(seg)
		if lerr == nil {
			if !lst.Mode().IsDir() || isLinkOrReparse(lst, seg) {
				return false
			}
		}
		if strings.EqualFold(seg, prefix) || filepath.Dir(seg) == seg {
			break
		}
	}
	return true
}

// isStrictDescendant checks whether child is strictly within parent workspace,
// taking into account volume names and case insensitivity on Windows.
func isStrictDescendant(workspace, root string) bool {
	absW, errW := filepath.Abs(workspace)
	absR, errR := filepath.Abs(root)
	if errW != nil || errR != nil {
		return false
	}
	absW = filepath.Clean(absW)
	absR = filepath.Clean(absR)

	volW := filepath.VolumeName(absW)
	volR := filepath.VolumeName(absR)
	if !strings.EqualFold(volW, volR) {
		return false
	}

	rel, err := filepath.Rel(absW, absR)
	if err != nil {
		return false
	}
	rel = filepath.Clean(rel)
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || strings.HasPrefix(rel, "../") || strings.HasPrefix(rel, `..\`) {
		return false
	}
	if runtime.GOOS == "windows" {
		lowerW := strings.ToLower(absW)
		lowerR := strings.ToLower(absR)
		prefix := strings.TrimRight(lowerW, "\\/") + string(os.PathSeparator)
		if !strings.HasPrefix(lowerR, prefix) {
			return false
		}
	}
	return true
}

// validWorkspace combines the guards for one requested root.
func validWorkspace(workspace string, root string) bool {
	if workspace == "" {
		abs, err := filepath.Abs(root)
		if err != nil {
			return false
		}
		return coarseRootOK(root) && chainLinksOK(filepath.Clean(abs))
	}
	absW, errW := filepath.Abs(workspace)
	if errW != nil {
		return false
	}
	absW = filepath.Clean(absW)
	absR, errR := filepath.Abs(root)
	if errR != nil {
		return false
	}
	absR = filepath.Clean(absR)

	if !coarseRootOK(workspace) {
		return false
	}
	homeDir, _ := os.UserHomeDir()
	if homeDir != "" {
		absHome, errH := filepath.Abs(homeDir)
		if errH == nil && strings.EqualFold(absW, filepath.Clean(absHome)) {
			return false
		}
	}
	if !isStrictDescendant(absW, absR) {
		return false
	}
	return coarseRootOK(absR) && chainLinksOK(absW) && chainLinksOK(absR)
}

func main() {
	execute := flag.Bool("execute", false, "run the chain in an exclusive root")
	cliPath := flag.String("cli", "", "path to the agentpulse binary")
	helperPath := flag.String("helper", "", "path to the agentpulse-test-helper binary")
	evidencePath := flag.String("evidence", "", "path for the JSON evidence file")
	rootPath := flag.String("root", "", "exclusive root directory for e2e run (must be under exclusive workspace)")
	workspace := flag.String("workspace", "", "exclusive workspace root; root must be strictly inside it (required with --execute)")
	flag.Parse()

	if !*execute {
		fmt.Print(plan)
		os.Exit(0)
	}
	if *cliPath == "" || *helperPath == "" || *evidencePath == "" || *rootPath == "" || *workspace == "" {
		fmt.Fprintln(os.Stderr, "--execute requires --cli, --helper, --evidence, --root, and --workspace (explicit exclusive workspace root)")
		os.Exit(2)
	}
	report, err := run(*cliPath, *helperPath, *rootPath, *workspace)
	raw, mErr := json.MarshalIndent(report, "", "  ")
	if mErr == nil {
		_ = os.WriteFile(*evidencePath, append(raw, '\n'), 0o600)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2e: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("e2e evidence %s broker %s\n", *evidencePath, report.BrokerURL)
}

const plan = `loopback e2e (not executed)
broker: in-process MQTT 3.1.1, bind 127.0.0.1:0, no username, no service, no firewall
listen: agentpulse listen --config <root> --mqtt-url tcp://127.0.0.1:<port> --machine e2e-node --no-attach --max-concurrent-jobs 1 --status-file <root>/status.json
dispatch: agentpulse dispatch --config <root> --mqtt-url <url> --machine e2e-node --job-id <id> --cwd <work> --repo synth/repo --project synth --force --wait --wait-timeout 20 --follow --env AGENTPULSE_TEST_ARGV_OUT=<file> -- <helper> stream <0|7>
helper: stdout helper-stdout-1 / progress 25 / progress 80 / password=synthetic-secret; stderr helper-stderr-1; exit 0 then 7
stop: clean signal/interrupt to the listen process this driver started
isolation: no --dangerously-skip-permissions, no AppContainer loopback exemption; non-danger child stays not_isolated
`

type evidence struct {
	BrokerURL             string     `json:"broker_url"`
	BrokerAddr            string     `json:"broker_addr"`
	BrokerPort            int        `json:"broker_port"`
	Root                  string     `json:"root"`
	RootRemoved           bool       `json:"root_removed"`
	ListenerPID           int        `json:"listener_pid"`
	ListenerExit          int        `json:"listener_exit"`
	ListenerExitRaw       int        `json:"listener_exit_raw"`
	ListenerExitClean     bool       `json:"listener_exit_clean"`
	ListenerPIDStampKnown bool       `json:"listener_pid_stamp_known"`
	ListenerReclaimProof  string     `json:"listener_reclaim_proof"`
	ListenerPIDStamp      string     `json:"listener_pid_stamp"`
	ListenerReclaimOK     bool       `json:"listener_reclaim_ok"`
	ReclaimNotes          []string   `json:"reclaim_notes"`
	Workspace             string     `json:"workspace"`
	WorkspaceChecked      bool       `json:"workspace_checked"`
	ObserveFailures       []string   `json:"observe_failures"`
	LeakFailures          []string   `json:"leak_failures"`
	RecheckArtifacts      string     `json:"recheck_artifacts"`
	ListenerExitClass     string     `json:"listener_exit_class"`
	ListenerGraceEvents   []string   `json:"listener_grace_events"`
	AuditHelperExits      []auditRec `json:"audit_helper_exits"`
	Scrubs                []string   `json:"scrubs"`
	RootChecks            []string   `json:"root_checks"`
	ExitObserved          bool       `json:"exit_observed"`
	PIDs                  []int      `json:"pids"`
	Stdout                string     `json:"stdout"`
	Stderr                string     `json:"stderr"`
	Commands              []string   `json:"commands"`
	OK                    jobProof   `json:"exit_0"`
	Fail                  jobProof   `json:"exit_7"`
	Checks                []string   `json:"checks"`
	Failures              []string   `json:"failures"`
	Permissions           []string   `json:"permissions"`
}

type auditRec struct {
	Job   string `json:"job_id"`
	Child int    `json:"child_exit"`
	Help  int    `json:"helper_exit"`
}

type jobProof struct {
	JobID                  string    `json:"job_id"`
	DispatchRC             int       `json:"dispatch_rc"`
	Argv                   []string  `json:"argv"`
	Statuses               []string  `json:"statuses"`
	Progress               []float64 `json:"progress"`
	EndedAt                string    `json:"ended_at"`
	Error                  string    `json:"error"`
	Command                string    `json:"command"`
	Logs                   []string  `json:"logs"`
	Wire                   string    `json:"wire"`
	CapturedPID            int       `json:"captured_pid"`
	PIDBoundDuringLifetime bool      `json:"pid_bound_during_lifetime"`
	JobReclaimProof        string    `json:"job_reclaim_proof"`
	HelperExitKnown        bool      `json:"helper_exit_known"`
	HelperExitVia          string    `json:"helper_exit_via"`
	HelperExit             int       `json:"helper_exit"`
	ChildExitKnown         bool      `json:"child_exit_known"`
	ChildExit              int       `json:"child_exit"`
}

type collector struct {
	mu      sync.Mutex
	status  map[string][]model.Job
	logs    map[string][]model.LogEntry
	wire    map[string]string
	machine []model.Machine
}

func run(cliPath, helperPath, root, workspace string) (*evidence, error) {
	cliPath, err := filepath.Abs(cliPath)
	if err != nil {
		return nil, err
	}
	helperPath, err = filepath.Abs(helperPath)
	if err != nil {
		return nil, err
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	workspaceOK := validWorkspace(workspace, root)

	ev := &evidence{
		Root:      root,
		Workspace: workspace,
		RootChecks: []string{
			"abs: root resolved to an absolute path before any directory creation",
			"workspace: explicit --workspace required; root must be a strict descendant (resolved, drive-root and home rejected)",
			"links: no path component may be a symlink or Windows reparse point/junction",
		},
		RootRemoved: false,
		Permissions: []string{
			"broker bind 127.0.0.1 ephemeral only",
			"no service install, firewall, ACL, or route change",
			"no credential, real home, or agy binary",
			"listen --no-attach; no --dangerously-skip-permissions",
			"non-danger Prepare is not_isolated; no loopback exemption added",
			"processes and config exist only under this root",
		},
	}

	if !workspaceOK {
		ev.Failures = append(ev.Failures, "--root containment: the resolved root must be strictly inside the explicitly specified --workspace, must not be the workspace itself, a drive root, the user home, or reached through a symlink/junction (checked before any directory creation)")
		return ev, fmt.Errorf("--root rejected: not strictly inside the explicit workspace")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return ev, err
	}

	// Post-creation re-verification: verify newly created tree still obeys containment
	// and no link or junction was swapped in during creation.
	if !validWorkspace(workspace, root) {
		ev.RootChecks = append(ev.RootChecks, "post-creation: a component of the created chain is not a real directory, or the created root escapes the explicit workspace")
		ev.Failures = append(ev.Failures, "post-creation containment: created root chain re-walked after MkdirAll and rejected")
		return ev, fmt.Errorf("post-creation containment: created root not strictly inside the explicit workspace")
	}
	rootFi, rErr := os.Lstat(root)
	if rErr != nil || !rootFi.Mode().IsDir() || isLinkOrReparse(rootFi, root) {
		ev.RootChecks = append(ev.RootChecks, "post-creation: created root is not a plain directory or is a link/reparse point")
		ev.Failures = append(ev.Failures, "post-creation containment: created root is not a plain directory")
		return ev, fmt.Errorf("post-creation containment: created root is not a plain directory")
	}
	ev.RootChecks = append(ev.RootChecks, "post-creation: created root chain re-walked, every existing component a real directory inside the workspace")

	var listener *exec.Cmd
	var broker *loopbackmqtt.Broker
	defer func() {
		if listener != nil && listener.Process != nil {
			_ = terminateListenerIfRunning(listener)
			_ = releaseListenerHandle()
		}
		if broker != nil {
			_ = broker.Close()
		}
	}()

	home := filepath.Join(root, "home")
	work := filepath.Join(root, "work")
	tmp := filepath.Join(root, "tmp")
	for _, dir := range []string{
		filepath.Join(home, ".config", "agentpulse"),
		filepath.Join(home, "AppData", "Roaming", "agentpulse"),
		filepath.Join(home, "AppData", "Local"),
		work, tmp,
	} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return ev, err
		}
	}

	broker, err = loopbackmqtt.Listen(0)
	if err != nil {
		return ev, err
	}
	ev.BrokerAddr = broker.Addr()
	ev.BrokerURL = broker.URL()
	if !strings.HasPrefix(ev.BrokerAddr, "127.0.0.1:") {
		ev.Failures = append(ev.Failures, "broker is not 127.0.0.1")
		return ev, fmt.Errorf("broker addr %s", ev.BrokerAddr)
	}
	if _, portStr, pErr := net.SplitHostPort(ev.BrokerAddr); pErr == nil {
		if p, pErr2 := strconv.Atoi(portStr); pErr2 == nil {
			ev.BrokerPort = p
		}
	}

	cfgPath := filepath.Join(home, ".config", "agentpulse", "config.yaml")
	cfgText := fmt.Sprintf("version: 1\nrev: 1\nmachine_id: e2e-node\nmax_concurrent_jobs: 1\nbroker:\n  url: %s\nagents:\n  - id: synth\n    path: %q\n    alias: [synth]\n    custom: true\n", ev.BrokerURL, helperPath)
	if err := os.WriteFile(cfgPath, []byte(cfgText), 0o600); err != nil {
		return ev, err
	}
	_ = os.WriteFile(filepath.Join(home, "AppData", "Roaming", "agentpulse", "config.yaml"), []byte(cfgText), 0o600)

	col := &collector{
		status: map[string][]model.Job{},
		logs:   map[string][]model.LogEntry{},
		wire:   map[string]string{},
	}
	obs := observe(ev.BrokerURL, col, ev)
	if len(ev.ObserveFailures) > 0 {
		ev.Failures = append(ev.Failures, "mqtt observe failure(s): "+strings.Join(ev.ObserveFailures, "; "))
		return ev, fmt.Errorf("mqtt observe failure: %s", strings.Join(ev.ObserveFailures, "; "))
	}
	defer obs.Disconnect(200)

	env := []string{
		"HOME=" + home,
		"USERPROFILE=" + home,
		"XDG_CONFIG_HOME=" + filepath.Join(home, ".config"),
		"APPDATA=" + filepath.Join(home, "AppData", "Roaming"),
		"LOCALAPPDATA=" + filepath.Join(home, "AppData", "Local"),
		"TMPDIR=" + tmp,
		"TMP=" + tmp,
		"TEMP=" + tmp,
	}
	if v := os.Getenv("SystemRoot"); v != "" {
		env = append(env, "SystemRoot="+v)
	}
	if v := os.Getenv("PATH"); v != "" {
		env = append(env, "PATH="+v)
	}

	statusFile := filepath.Join(root, "status.json")
	listenArgs := []string{
		"listen",
		"--config", cfgPath,
		"--mqtt-url", ev.BrokerURL,
		"--machine", "e2e-node",
		"--no-attach",
		"--max-concurrent-jobs", "1",
		"--status-file", statusFile,
	}
	ev.Commands = append(ev.Commands, cliPath+" "+strings.Join(listenArgs, " "))
	stdout := &safeBuf{}
	stderr := &safeBuf{}
	listener = exec.Command(cliPath, listenArgs...)
	listener.Env = env
	listener.Dir = work
	listener.Stdout = stdout
	listener.Stderr = stderr
	setupListenerSysProcAttr(listener)
	if err := listener.Start(); err != nil {
		return ev, err
	}
	attachStamp, attachErr := attachListenerHandle(listener)
	if attachErr != nil {
		ev.ReclaimNotes = append(ev.ReclaimNotes, "listener identity attach failed: "+attachErr.Error()+"; direct-child management only")
	} else {
		ev.ListenerPIDStamp = attachStamp
		ev.ListenerPIDStampKnown = true
	}
	ev.ListenerPID = listener.Process.Pid
	ev.PIDs = append(ev.PIDs, ev.ListenerPID)
	ev.WorkspaceChecked = workspaceOK

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(stdout.String(), "Waiting for incoming dispatch commands") {
			break
		}
		if listener.ProcessState != nil && listener.ProcessState.Exited() {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(stdout.String(), "Waiting for incoming dispatch commands") {
		ev.Failures = append(ev.Failures, "listener did not reach subscribe: "+stdout.String()+" "+stderr.String())
		return ev, fmt.Errorf("listener did not subscribe")
	}

	okArgv := filepath.Join(root, "argv-ok.json")
	failArgv := filepath.Join(root, "argv-fail.json")
	ev.OK = dispatchJob(cliPath, cfgPath, ev.BrokerURL, helperPath, work, env, "job-e2e-ok", "0", okArgv, col, &ev.Commands)
	auditHelperExit(root, &ev.OK, 0, ev)
	ev.Fail = dispatchJob(cliPath, cfgPath, ev.BrokerURL, helperPath, work, env, "job-e2e-fail", "7", failArgv, col, &ev.Commands)
	auditHelperExit(root, &ev.Fail, 7, ev)
	checkJob(ev, "exit_0", ev.OK, 0, "done")
	checkJob(ev, "exit_7", ev.Fail, 7, "failed")

	_ = stopListener(listener)
	waitDone := make(chan error, 1)
	go func() { waitDone <- listener.Wait() }()
	select {
	case err := <-waitDone:
		ev.ExitObserved = true
		if err == nil {
			ev.ListenerExit = 0
			ev.ListenerExitRaw = 0
			ev.ListenerExitClean = true
			ev.ListenerExitClass = listenerExitClass(0)
		} else if ee, ok := err.(*exec.ExitError); ok {
			raw := ee.ExitCode()
			ev.ListenerExit = raw
			ev.ListenerExitRaw = raw
			ev.ListenerExitClean = isCleanListenerExit(raw)
			ev.ListenerExitClass = listenerExitClass(raw)
		} else {
			ev.ListenerExit = 1
			ev.ListenerExitRaw = 1
			ev.ListenerExitClean = false
			ev.ListenerExitClass = "unknown"
			ev.Failures = append(ev.Failures, "listener wait: "+err.Error())
		}
	case <-time.After(5 * time.Second):
		_ = terminateListenerIfRunning(listener)
		ev.ReclaimNotes = append(ev.ReclaimNotes, "timeout: terminate path recorded by "+listenerTerminationVia())
		ev.ExitObserved = true
		ev.ListenerExitRaw = -1
		ev.ListenerExitClean = false
		ev.ListenerExitClass = "unknown"
		ev.ListenerExit = -1
		ev.Failures = append(ev.Failures, "listener did not exit after stop signal")
	}

	proof, praw, pok := listenerReclaimProof(listener)
	ev.ListenerReclaimOK = pok
	ev.ListenerReclaimProof = proof
	if pok {
		ev.ListenerExitRaw = praw
		ev.ListenerExitClean = isCleanListenerExit(praw)
		ev.ListenerExitClass = listenerExitClass(praw)
	}
	ev.ReclaimNotes = append(ev.ReclaimNotes, "reclaim proof recorded by "+listenerTerminationVia())
	ev.ListenerGraceEvents = listenerGraceEvents()
	ev.ReclaimNotes = append(ev.ReclaimNotes, "dispatch and helper PIDs: listener spawns helper as child; status.json audit recorded; historical PIDs not killed")
	listener = nil

	// Archive stdout/stderr logs into root
	stdoutFile := filepath.Join(root, "listener.stdout.log")
	stderrFile := filepath.Join(root, "listener.stderr.log")
	stdoutRaw := stdout.String()
	stderrRaw := stderr.String()

	// Probe raw outputs BEFORE any scrub
	probeLeakedSecrets(ev, "listener stdout", stdoutRaw)
	probeLeakedSecrets(ev, "listener stderr", stderrRaw)
	probeRawArtifactLeak(ev, "status.json", statusFile)
	probeRawArtifactLeak(ev, "audit.jsonl", filepath.Join(root, "tmp", "agentpulse-audit", "audit.jsonl"))

	stdoutClean, stdoutHit := scrubString(stdoutRaw)
	stderrClean, stderrHit := scrubString(stderrRaw)
	if stdoutHit || stderrHit {
		ev.Scrubs = append(ev.Scrubs, "driver listener stdout/stderr artifacts: synthetic secret scrubbed with wrapper.RedactSecrets before persisting")
	}
	_ = os.WriteFile(stdoutFile, []byte(stdoutClean), 0o600)
	_ = os.WriteFile(stderrFile, []byte(stderrClean), 0o600)
	ev.RecheckArtifacts = rescanArtifactsForSecret(root)
	if ev.RecheckArtifacts != "clean" {
		ev.Failures = append(ev.Failures, "raw secret still on disk after scrubbing: "+ev.RecheckArtifacts)
	}
	ev.Stdout = stdoutFile
	ev.Stderr = stderrFile

	if !ev.ListenerReclaimOK {
		ev.Failures = append(ev.Failures, "listener reclamation not identity-proven: "+ev.ListenerReclaimProof)
	}

	if len(ev.Failures) > 0 {
		return ev, fmt.Errorf("%s", strings.Join(ev.Failures, "; "))
	}
	ev.Checks = append(ev.Checks,
		"broker 127.0.0.1",
		"listen subscribed",
		"exit 0 done",
		"exit 7 failed",
		"redaction",
		"ended_at",
		"argv",
		"listener reclamation identity-bound",
		"helper exit audit-confirmed",
		"raw-secret probe on all channels BEFORE scrub, failure names channel",
		"listener raw exit code kept and classified (normal/control-event/force-terminate/unknown/abnormal)",
		"graceful stop sequence recorded step by step (Windows: CTRL_BREAK, bounded wait, bound-handle force, re-wait)",
		"helper/child exits attributed to the job from the product audit originals, unknown fails",
		"mqtt connect/subscribe failures fail the overall result",
		"workspace chain guards run before creation and are re-run after creation",
	)
	return ev, nil
}

func dispatchJob(cliPath, cfgPath, brokerURL, helperPath, work string, env []string, jobID, code, argvPath string, col *collector, commands *[]string) jobProof {
	args := []string{
		"dispatch",
		"--config", cfgPath,
		"--mqtt-url", brokerURL,
		"--machine", "e2e-node",
		"--job-id", jobID,
		"--cwd", work,
		"--repo", "synth/repo",
		"--project", "synth",
		"--requested-by", "e2e",
		"--force",
		"--wait",
		"--wait-timeout", "20",
		"--follow",
		"--env", "AGENTPULSE_TEST_ARGV_OUT=" + argvPath,
		"--",
		helperPath, "stream", code,
	}
	*commands = append(*commands, cliPath+" "+strings.Join(args, " "))
	cmd := exec.Command(cliPath, args...)
	cmd.Env = env
	cmd.Dir = work
	out, err := cmd.CombinedOutput()
	rc := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			rc = ee.ExitCode()
		} else {
			rc = 1
		}
	}
	proof := jobProof{JobID: jobID, DispatchRC: rc, Command: string(out)}
	if b, rerr := os.ReadFile(argvPath); rerr == nil {
		_ = json.Unmarshal(b, &proof.Argv)
	}
	col.mu.Lock()
	for _, j := range col.status[jobID] {
		proof.Statuses = append(proof.Statuses, string(j.Status))
		proof.Progress = append(proof.Progress, j.Progress)
		if !j.EndedAt.IsZero() {
			proof.EndedAt = j.EndedAt.UTC().Format(time.RFC3339Nano)
			proof.Error = j.Error
			proof.Command = j.Command
		}
	}
	for _, l := range col.logs[jobID] {
		proof.Logs = append(proof.Logs, l.Message)
	}
	proof.Wire = col.wire[jobID]
	col.mu.Unlock()
	return proof
}

func checkJob(ev *evidence, name string, job jobProof, wantExit int, wantStatus string) {
	if len(job.Argv) < 3 || job.Argv[len(job.Argv)-2] != "stream" || job.Argv[len(job.Argv)-1] != fmt.Sprint(wantExit) {
		ev.Failures = append(ev.Failures, name+" argv")
	}
	if !contains(job.Statuses, "queued") || !contains(job.Statuses, "running") || !contains(job.Statuses, wantStatus) {
		ev.Failures = append(ev.Failures, name+" status sequence "+strings.Join(job.Statuses, ","))
	}
	if job.EndedAt == "" {
		ev.Failures = append(ev.Failures, name+" missing ended_at")
	}
	if !hasFloat(job.Progress, 25) || !hasFloat(job.Progress, 80) {
		ev.Failures = append(ev.Failures, name+" progress")
	}
	logs := strings.Join(job.Logs, "\n")
	for _, line := range []string{"helper-stdout-1", "progress: 25%", "progress: 80%", "helper-stderr-1", "password=[REDACTED]"} {
		if !strings.Contains(logs, line) {
			ev.Failures = append(ev.Failures, name+" missing log "+line)
		}
	}
	if strings.Contains(logs, "synthetic-secret") || strings.Contains(job.Command, "synthetic-secret") || strings.Contains(job.Wire, "synthetic-secret") {
		ev.Failures = append(ev.Failures, name+" secret leaked")
	}
	if wantExit == 0 && job.DispatchRC != 0 {
		ev.Failures = append(ev.Failures, name+" dispatch rc: exit 0 expected but got "+fmt.Sprint(job.DispatchRC))
	}
	if wantExit == 7 && job.DispatchRC == 0 {
		ev.Failures = append(ev.Failures, name+" exit 7 with dispatch rc 0")
	}
	if !job.HelperExitKnown {
		ev.Failures = append(ev.Failures, name+" helper exit unknown from the product audit originals")
	}
	if !job.ChildExitKnown {
		ev.Failures = append(ev.Failures, name+" child exit unknown from the product audit originals")
	}
	if job.HelperExitKnown && job.HelperExit != wantExit {
		ev.Failures = append(ev.Failures, name+" recorded helper exit "+fmt.Sprint(job.HelperExit)+" != wanted "+fmt.Sprint(wantExit))
	}
	if !strings.Contains(job.Wire, job.JobID) || !strings.Contains(job.Wire, "stream") {
		ev.Failures = append(ev.Failures, name+" wire")
	}
	if !strings.Contains(job.Command, "agentpulse-test-helper") || !strings.Contains(job.Command, "stream "+fmt.Sprint(wantExit)) {
		ev.Failures = append(ev.Failures, name+" command is not the helper")
	}
}

func observe(url string, col *collector, ev *evidence) paho.Client {
	opts := paho.NewClientOptions().AddBroker(url).SetClientID("e2e-observer").SetAutoReconnect(false)
	handler := func(_ paho.Client, msg paho.Message) {
		body := string(msg.Payload())
		var job model.Job
		if json.Unmarshal(msg.Payload(), &job) == nil && job.ID != "" && job.Status != "" {
			col.mu.Lock()
			col.status[job.ID] = append(col.status[job.ID], job)
			col.mu.Unlock()
			return
		}
		var entry model.LogEntry
		if json.Unmarshal(msg.Payload(), &entry) == nil && entry.JobID != "" && entry.Message != "" {
			col.mu.Lock()
			col.logs[entry.JobID] = append(col.logs[entry.JobID], entry)
			col.mu.Unlock()
			return
		}
		var wire struct {
			JobID   string   `json:"job_id"`
			Command string   `json:"command"`
			Args    []string `json:"args"`
		}
		if json.Unmarshal(msg.Payload(), &wire) == nil && wire.JobID != "" {
			col.mu.Lock()
			col.wire[wire.JobID] = body
			col.mu.Unlock()
		}
	}
	opts.SetDefaultPublishHandler(handler)
	c := paho.NewClient(opts)
	token := c.Connect()
	if !token.WaitTimeout(5 * time.Second) || token.Error() != nil {
		ev.ObserveFailures = append(ev.ObserveFailures, "mqtt connect token timed out or errored: "+fmt.Sprint(token.Error()))
		return c
	}
	topics := []string{
		mqtt.MachineStatusTopic("e2e-node"),
		mqtt.MachineDispatchTopic("e2e-node"),
		mqtt.JobStatusTopic("e2e-node", "job-e2e-ok"),
		mqtt.JobLogsTopic("e2e-node", "job-e2e-ok"),
		mqtt.JobStatusTopic("e2e-node", "job-e2e-fail"),
		mqtt.JobLogsTopic("e2e-node", "job-e2e-fail"),
	}
	for _, topic := range topics {
		sub := c.Subscribe(topic, 1, handler)
		if !sub.WaitTimeout(3 * time.Second) || sub.Error() != nil {
			ev.ObserveFailures = append(ev.ObserveFailures, "mqtt subscribe topic "+topic+": "+fmt.Sprint(sub.Error()))
		}
	}
	return c
}

// probeLeakedSecrets records raw-secret findings on the named channel BEFORE any
// scrub runs; the finding is pushed to ev.Failures and ev.LeakFailures.
func probeLeakedSecrets(ev *evidence, channel, text string) {
	if text == "" {
		return
	}
	if strings.Contains(text, "synthetic-secret") || wrapper.RedactSecrets(text) != text {
		ev.LeakFailures = append(ev.LeakFailures, channel+": raw secret before scrub")
		ev.Failures = append(ev.Failures, "raw secret observed on "+channel+" before scrub")
	}
}

// probeRawArtifactLeak inspects artifact files before/during run to verify no unredacted secret leaks.
func probeRawArtifactLeak(ev *evidence, name, path string) {
	b, err := os.ReadFile(path)
	if err != nil || len(b) == 0 {
		return
	}
	text := string(b)
	if strings.Contains(text, "synthetic-secret") || wrapper.RedactSecrets(text) != text {
		ev.LeakFailures = append(ev.LeakFailures, name+": raw secret in artifact")
		ev.Failures = append(ev.Failures, "raw secret observed in artifact "+name)
	}
}

// listenerExitClass classifies a raw listener exit code: normal (0), force-terminate
// (0xC000013A), control-event (<=128), unknown (<0), abnormal (>128). A non-zero
// code is never success.
func listenerExitClass(raw int) string {
	if raw == 0 {
		return "normal"
	}
	if uint32(raw) == 0xC000013A || raw == -1073741510 || raw == 3221225786 {
		return "force-terminate"
	}
	if raw < 0 {
		return "unknown"
	}
	if raw <= 128 {
		return "control-event"
	}
	return "abnormal"
}

// auditHelperExit reads product audit originals (audit.jsonl) and status.json
// to attribute child/helper exit codes and verify process identities.
func auditHelperExit(root string, job *jobProof, wantExit int, ev *evidence) {
	auditPath := filepath.Join(root, "tmp", "agentpulse-audit", "audit.jsonl")
	raw, rerr := os.ReadFile(auditPath)
	if rerr != nil || len(raw) == 0 {
		job.HelperExitKnown = false
		job.HelperExitVia = "unknown"
		job.ChildExitKnown = false
		job.ChildExit = 0
		job.JobReclaimProof = "unproven: audit file unreadable or empty"
		ev.Failures = append(ev.Failures, "job "+job.JobID+" helper/child exit unknown from product audit: "+fmt.Sprint(rerr)+" "+auditPath)
		return
	}
	type auditEntry struct {
		Argv       []string `json:"argv"`
		ChildExit  *int     `json:"child_exit"`
		HelperExit *int     `json:"helper_exit"`
		ExitCode   int      `json:"exit_code"`
	}
	found := false
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var entry auditEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}
		match := false
		for i, arg := range entry.Argv {
			if arg == "stream" && i+1 < len(entry.Argv) && entry.Argv[i+1] == fmt.Sprint(wantExit) {
				match = true
				break
			}
		}
		if !match {
			continue
		}
		job.ChildExitKnown = (entry.ChildExit != nil)
		if entry.ChildExit != nil {
			job.ChildExit = *entry.ChildExit
		}
		if entry.HelperExit != nil {
			job.HelperExitKnown = true
			job.HelperExit = *entry.HelperExit
			job.HelperExitVia = "product audit helper_exit"
		} else if entry.ChildExit != nil {
			job.HelperExitKnown = true
			job.HelperExit = *entry.ChildExit
			job.HelperExitVia = "product audit child_exit"
		} else {
			job.HelperExitKnown = true
			job.HelperExit = entry.ExitCode
			job.HelperExitVia = "product audit exit_code"
		}
		ev.AuditHelperExits = append(ev.AuditHelperExits, auditRec{Job: job.JobID, Child: job.ChildExit, Help: job.HelperExit})
		found = true
		break
	}
	if !found {
		job.HelperExitKnown = false
		job.HelperExitVia = "unknown"
		job.ChildExitKnown = false
		ev.Failures = append(ev.Failures, fmt.Sprintf("job %s helper/child exit unknown: no audit record found for stream %d", job.JobID, wantExit))
	}

	// Read status.json to check job PID attribution (read-only audit, never kill by historical PID)
	statusPath := filepath.Join(root, "status.json")
	if statusBytes, sErr := os.ReadFile(statusPath); sErr == nil && len(statusBytes) > 0 {
		var st struct {
			Job struct {
				ID string `json:"id"`
			} `json:"job"`
			PID int `json:"pid"`
		}
		if json.Unmarshal(statusBytes, &st) == nil && st.Job.ID == job.JobID {
			job.CapturedPID = st.PID
			job.PIDBoundDuringLifetime = false
			job.JobReclaimProof = fmt.Sprintf("historical PID %d from status.json (read-only verification; handle not bound during lifetime; historical PID kill forbidden)", st.PID)
		}
	}
	if job.JobReclaimProof == "" {
		job.JobReclaimProof = "unproven: helper is ephemeral child of listener; PID/handle not bound during process lifetime"
	}
}

func contains(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

func hasFloat(list []float64, want float64) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

type safeBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *safeBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *safeBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// scrubString scrubs driver-owned artifact text with the product redactor
// (internal/wrapper RedactSecrets) plus an explicit scrub of the synthetic
// fixture secret, and reports whether anything changed.
func scrubString(s string) (clean string, changed bool) {
	if s == "" {
		return s, false
	}
	t := strings.ReplaceAll(s, "synthetic-secret", "[SCRUBBED_SYNTHETIC]")
	hits := (t != s)
	r := wrapper.RedactSecrets(t)
	return r, hits || (r != s)
}

// rescanArtifactsForSecret reads persisted driver artifacts and reports any that
// still contain the synthetic secret.
func rescanArtifactsForSecret(root string) string {
	statusFile := filepath.Join(root, "status.json")
	auditFile := filepath.Join(root, "tmp", "agentpulse-audit", "audit.jsonl")
	var hits []string
	paths := []string{
		filepath.Join(root, "listener.stdout.log"),
		filepath.Join(root, "listener.stderr.log"),
		filepath.Join(root, "argv-ok.json"),
		filepath.Join(root, "argv-fail.json"),
		statusFile,
		auditFile,
	}
	for _, p := range paths {
		b, rerr := os.ReadFile(p)
		if rerr == nil && len(b) > 0 && strings.Contains(string(b), "synthetic-secret") {
			hits = append(hits, p)
		}
	}
	if len(hits) == 0 {
		return "clean"
	}
	return "hits: " + fmt.Sprint(hits)
}
