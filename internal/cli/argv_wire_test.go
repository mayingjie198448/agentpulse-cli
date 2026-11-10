package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"
	"github.com/mayingjie198448/agentpulse-cli/internal/dispatch"
	"github.com/mayingjie198448/agentpulse-cli/internal/model"
	"github.com/mayingjie198448/agentpulse-cli/internal/store/mqtt"
	"github.com/mayingjie198448/agentpulse-cli/internal/wrapper"
)

// These tests close the PR45 argv gap on the combined tree.
//
// In-memory: cobra parses the dispatch command, dispatch.Dispatch builds the
// payload, and a mock client records the bytes that would be published.
// That capture is not an MQTT network test and does not open a broker.
//
// Real child: the listener consumes those exact bytes (or an injected
// historical payload) with DisableBareAttach and starts the reviewed helper.
// The actual argv is the helper's own os.Args file, not a copy of the expectation.

func TestDispatchWireChildArgv_PositionalSeparator(t *testing.T) {
	beginArgvWire(t)
	prompt := "修复 数据库连接池"
	quoted := `say "你好 世界"`
	opaque := "ghp_FAKE_NOT_A_TOKEN"
	args := []string{
		"record-argv", "0",
		"--qa-duration", "4",
		"--qa-exit", "0",
		"-p", prompt,
		"--qa-opaque", opaque,
		quoted,
	}
	wire, got, err := runPositionalDispatch(t, "qa-wire-positional", "0", args[2:])
	assertExitCode(t, err, 0)
	assertNoLeadingSeparator(t, wire.Command, wire.Args, got)
	assertArgvExact(t, wire, got, args)
}

func TestDispatchWireChildArgv_CommandArgFlags(t *testing.T) {
	beginArgvWire(t)
	args := []string{
		"record-argv", "0",
		"git", "checkout", "--", "README 中文.md",
		"sk-FAKE-NOT-A-SECRET",
	}
	wire, got, err := runCommandArgDispatch(t, "qa-wire-flags", args[2:])
	assertExitCode(t, err, 0)
	assertNoLeadingSeparator(t, wire.Command, wire.Args, got)
	if !containsExactSequence(wire.Args, []string{"git", "checkout", "--", "README 中文.md"}) {
		t.Fatalf("in-memory wire lost internal separator: %#v", wire.Args)
	}
	assertArgvExact(t, wire, got, args)
}

func TestDispatchWireChildArgv_HistoricalLeadingSeparator(t *testing.T) {
	beginArgvWire(t)
	helper := testHelperPath(t)
	injected := []string{"--", "record-argv", "0", "git", "checkout", "--", "README 中文.md"}
	payload := model.DispatchCommand{
		CommandID: "hist-leading",
		JobID:     "hist-leading",
		Engine:    model.EngineAgy,
		Cwd:       t.TempDir(),
		Command:   helper,
		Args:      injected,
	}
	if payload.Args[0] != "--" {
		t.Fatal("fixture must still carry the historical leading separator")
	}
	got, err := runInjectedWire(t, payload)
	wantArgs := []string{"record-argv", "0", "git", "checkout", "--", "README 中文.md"}
	finishEvidence(t, helper, wantArgs)
	assertExitCode(t, err, 0)
	assertChildArgs(t, helper, got, wantArgs)
	if !containsExactSequence(got[1:], []string{"checkout", "--", "README 中文.md"}) {
		t.Fatalf("child lost internal --: %#v", got)
	}
}

func TestDispatchWireChildArgv_HistoricalEmbeddedCommand(t *testing.T) {
	beginArgvWire(t)
	helper := testHelperPath(t)
	if strings.ContainsAny(helper, " \t") {
		t.Fatalf("helper path %q contains whitespace; Fields() would split the command token", helper)
	}
	wantArgs := []string{"record-argv", "0", "git", "checkout", "--", "README 中文.md", "sk-FAKE-NOT-A-SECRET"}
	payload := model.DispatchCommand{
		CommandID: "hist-embedded",
		JobID:     "hist-embedded",
		Engine:    model.EngineAgy,
		Cwd:       t.TempDir(),
		Command:   helper + " --",
		Args:      wantArgs,
	}
	got, err := runInjectedWire(t, payload)
	finishEvidence(t, helper, wantArgs)
	assertExitCode(t, err, 0)
	// The injected command string still contains the separator.
	// The real child must see the helper path and must keep the later "--".
	if !strings.Contains(payload.Command, " --") {
		t.Fatal("fixture lost the embedded separator")
	}
	assertChildArgs(t, helper, got, wantArgs)
}

func TestDispatchWireChildArgv_ExitCode(t *testing.T) {
	beginArgvWire(t)
	opaque := "ghp_FAKE_NOT_A_TOKEN"
	rest := []string{"--qa-duration", "1", "--qa-exit", "7", "-p", "失败 码", "--qa-opaque", opaque}
	args := append([]string{"record-argv", "7"}, rest...)
	wire, got, err := runPositionalDispatch(t, "qa-wire-exit7", "7", rest)
	assertExitCode(t, err, 7)
	assertNoLeadingSeparator(t, wire.Command, wire.Args, got)
	assertArgvExact(t, wire, got, args)
}

func runPositionalDispatch(t *testing.T, jobID, exitCode string, rest []string) (model.DispatchCommand, []string, error) {
	t.Helper()
	helper := testHelperPath(t)
	args := append([]string{helper, "--", "record-argv", exitCode}, rest...)
	return runCLIDispatch(t, jobID, args, nil)
}

func runCommandArgDispatch(t *testing.T, jobID string, rest []string) (model.DispatchCommand, []string, error) {
	t.Helper()
	helper := testHelperPath(t)
	flags := []string{"--command", helper, "--arg", "record-argv", "--arg", "0"}
	for _, a := range rest {
		if a == "--" {
			flags = append(flags, "--arg=--")
			continue
		}
		flags = append(flags, "--arg", a)
	}
	return runCLIDispatch(t, jobID, nil, flags)
}

func runCLIDispatch(t *testing.T, jobID string, positional []string, extra []string) (model.DispatchCommand, []string, error) {
	t.Helper()
	profile := syntheticProfile(t)
	installArgvWireSeams(t)
	wireMock := newArgvMock()
	prev := dispatchInvoker
	dispatchInvoker = func(ctx context.Context, cfg dispatch.Config) (*model.DispatchCommand, error) {
		if !cfg.Force {
			t.Fatalf("dispatch command did not keep --force")
		}
		if cfg.MQTTClient != nil {
			t.Fatalf("CLI constructed a real MQTT client before the in-memory hook")
		}
		cfg.MQTTClient = wireMock
		return dispatch.Dispatch(ctx, cfg)
	}
	t.Cleanup(func() { dispatchInvoker = prev })

	cmd := NewRootCmd()
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	argv := []string{
		"dispatch",
		"--machine", "qa-box",
		"--force",
		"--quiet",
		"--engine", "agy",
		"--job-id", jobID,
		"--cwd", profile,
		"--config", syntheticConfigPath(profile),
		"--mqtt-url", "tcp://203.0.113.9:1883",
	}
	argv = append(argv, extra...)
	argv = append(argv, positional...)
	cmd.SetArgs(argv)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("dispatch command failed: %v\nstderr: %s", err, stderr.String())
	}

	published := wireMock.payloads(mqtt.MachineDispatchTopic("qa-box"))
	if len(published) != 1 {
		t.Fatalf("in-memory dispatch publishes = %d, want 1", len(published))
	}
	var wire model.DispatchCommand
	if err := json.Unmarshal(published[0], &wire); err != nil {
		t.Fatalf("wire json: %v", err)
	}
	got, runErr := runListenerOnPayload(t, profile, published[0], jobID, "cobra-dispatch-mock-listener-helper")
	finishEvidence(t, wire.Command, wire.Args)
	return wire, got, runErr
}

func runInjectedWire(t *testing.T, payload model.DispatchCommand) ([]string, error) {
	t.Helper()
	profile := syntheticProfile(t)
	installArgvWireSeams(t)
	if payload.Cwd == "" {
		payload.Cwd = profile
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal injected wire: %v", err)
	}
	t.Log("wire=injected historical payload in memory; not a CLI parse and not an MQTT network publish")
	return runListenerOnPayload(t, profile, raw, payload.JobID, "injected-historical-payload")
}

func runListenerOnPayload(t *testing.T, profile string, payload []byte, jobID, chain string) ([]string, error) {
	t.Helper()
	argvPath := filepath.Join(profile, "argv-"+jobID+".json")
	_ = os.Remove(argvPath)
	installArgvWireSeams(t)

	var stdout, stderr bytes.Buffer
	obs := &argvObs{
		chain:    chain,
		payload:  append([]byte(nil), payload...),
		profile:  profile,
		argvPath: argvPath,
	}
	done := make(chan error, 1)
	listening := make(chan struct{})
	mock := newArgvMock()
	l, err := wrapper.NewListener(wrapper.ListenerConfig{
		MachineID:          "qa-box",
		StatusFile:         filepath.Join(profile, "status-"+jobID+".json"),
		MQTTClient:         mock,
		Stub:               false,
		Quiet:              true,
		DisableBareAttach:  true,
		MaxConcurrentJobs:  1,
		UseAgentConfigFile: true,
		AgentConfigFile:    syntheticConfigPath(profile),
		BaseEnviron:        childBaseEnviron(profile, argvPath),
		AuditPath:          filepath.Join(profile, "audit-"+jobID+".jsonl"),
		RealHome:           profile,
		Stdout:             &stdout,
		Stderr:             &stderr,
		OnListening: func(string) {
			close(listening)
		},
		OnChildStart: func(pid int) {
			obs.mu.Lock()
			obs.pid = pid
			obs.started = time.Now().UTC()
			obs.mu.Unlock()
		},
		OnJobComplete: func(job model.Job, runErr error) {
			obs.mu.Lock()
			obs.waitSeen = true
			obs.stdout = append([]byte(nil), stdout.Bytes()...)
			obs.stderr = append([]byte(nil), stderr.Bytes()...)
			obs.mu.Unlock()
			if job.ID == jobID || jobID == "" {
				done <- runErr
			}
		},
	})
	if err != nil {
		t.Fatalf("NewListener: %v", err)
	}
	if l.AttachManager() != nil {
		t.Fatal("listener started default discovery")
	}

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan struct{})
	go func() {
		_ = l.Run(ctx)
		close(runDone)
	}()
	// Registered after the seam install, so this runs before seam restore and
	// before t.TempDir removal. Run's return is after workerWg.Wait.
	t.Cleanup(func() {
		l.Stop()
		cancel()
		<-runDone
		obs.mu.Lock()
		obs.runJoined = true
		obs.mu.Unlock()
		writeLifecycleReceipt(t, obs)
	})

	select {
	case <-listening:
	case <-time.After(10 * time.Second):
		t.Fatal("listener did not reach subscribe")
	}

	topic := mqtt.MachineDispatchTopic("qa-box")
	mock.Publish(topic, 1, false, append([]byte(nil), payload...))

	var runErr error
	select {
	case runErr = <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("timed out waiting for the real helper process")
	}
	got := readArgvFile(t, argvPath)
	obs.mu.Lock()
	obs.child = append([]string(nil), got...)
	obs.runErr = runErr
	if jobCwd := payloadCwd(payload); jobCwd != "" {
		obs.helperCwd = jobCwd
	}
	obs.mu.Unlock()
	obsMu.Lock()
	latestObs = obs
	obsMu.Unlock()
	t.Logf("wire=in-memory payload bytes; child=real helper process; udp and git seams are test-only; argv file %s", argvPath)
	return got, runErr
}

// argvWireStop is the same-process stop contract for the five argv-wire tests.
// -test.failfast does not stop on Skip and does not stop the current test at Errorf.
// beginArgvWire holds argvWireMu until cleanup, so the next test cannot pass the
// gate until this test has joined Listener.Run, restored seams, and dropped temp.
var (
	argvWireMu   sync.Mutex
	argvWireStop string
	obsMu        sync.Mutex
	latestObs    *argvObs
)

type argvObs struct {
	mu        sync.Mutex
	chain     string
	payload   []byte
	profile   string
	argvPath  string
	helperCwd string
	child     []string
	pid       int
	started   time.Time
	waitSeen  bool
	runJoined bool
	runErr    error
	stdout    []byte
	stderr    []byte
}

func beginArgvWire(t *testing.T) {
	t.Helper()
	argvWireMu.Lock()
	t.Cleanup(func() {
		if (t.Failed() || t.Skipped()) && argvWireStop == "" {
			argvWireStop = t.Name()
		}
		argvWireMu.Unlock()
	})
	if argvWireStop != "" {
		t.Skip("argv-wire stop-contract: side effects not started after " + argvWireStop)
	}
	dir := os.Getenv("AGENTPULSE_ARGV_WIRE_EVIDENCE")
	if !filepath.IsAbs(dir) {
		t.Fatal("AGENTPULSE_ARGV_WIRE_EVIDENCE must be an absolute directory this test does not delete")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
}

func argvWireAllows() (bool, string) {
	argvWireMu.Lock()
	defer argvWireMu.Unlock()
	if argvWireStop != "" {
		return false, argvWireStop
	}
	return true, ""
}

func noteArgvWireStop(reason string) {
	argvWireMu.Lock()
	if argvWireStop == "" {
		argvWireStop = reason
	}
	argvWireMu.Unlock()
}

func TestArgvWireStopContractPure(t *testing.T) {
	// Not one of the five authorized runs. Memory only: no listener, helper, UDP, or git.
	argvWireMu.Lock()
	prev := argvWireStop
	argvWireStop = ""
	argvWireMu.Unlock()
	t.Cleanup(func() {
		argvWireMu.Lock()
		argvWireStop = prev
		argvWireMu.Unlock()
	})
	if ok, why := argvWireAllows(); !ok {
		t.Fatalf("gate closed before the pure check: %s", why)
	}
	noteArgvWireStop("pure-test-stop")
	ok, why := argvWireAllows()
	if ok || why != "pure-test-stop" {
		t.Fatalf("gate after stop = %v %q", ok, why)
	}
}

func finishEvidence(t *testing.T, wantCommand string, wantArgs []string) {
	t.Helper()
	obsMu.Lock()
	obs := latestObs
	obsMu.Unlock()
	if obs == nil {
		t.Fatal("missing argv observation")
	}
	obs.mu.Lock()
	snap := argvSnap{
		chain: obs.chain, payload: append([]byte(nil), obs.payload...),
		helperCwd: obs.helperCwd, child: append([]string(nil), obs.child...),
		pid: obs.pid, started: obs.started, waitSeen: obs.waitSeen,
		runErr: obs.runErr, stdout: append([]byte(nil), obs.stdout...),
		stderr: append([]byte(nil), obs.stderr...),
	}
	obs.mu.Unlock()
	writeArgvEvidence(t, snap, wantCommand, wantArgs)
}

type argvSnap struct {
	chain     string
	payload   []byte
	helperCwd string
	child     []string
	pid       int
	started   time.Time
	waitSeen  bool
	runErr    error
	stdout    []byte
	stderr    []byte
}

func writeArgvEvidence(t *testing.T, obs argvSnap, wantCommand string, wantArgs []string) {
	t.Helper()
	dir := os.Getenv("AGENTPULSE_ARGV_WIRE_EVIDENCE")
	sum := sha256.Sum256(obs.payload)
	var wire model.DispatchCommand
	_ = json.Unmarshal(obs.payload, &wire)
	want := append([]string{wantCommand}, wantArgs...)
	elements := make([]map[string]any, 0, len(want))
	match := len(obs.child) == len(want)
	n := len(want)
	if len(obs.child) > n {
		n = len(obs.child)
	}
	for i := 0; i < n; i++ {
		var g, w string
		var gm, wm bool
		if i < len(obs.child) {
			g, gm = obs.child[i], true
		}
		if i < len(want) {
			w, wm = want[i], true
		}
		eq := gm && wm && (g == w || (i == 0 && samePath(g, w)))
		if !eq {
			match = false
		}
		elements = append(elements, map[string]any{
			"index": i, "want": w, "got": g, "match": eq,
		})
	}
	code := 0
	if obs.runErr != nil {
		var ee *exec.ExitError
		if errors.As(obs.runErr, &ee) {
			code = ee.ExitCode()
		} else {
			code = -1
		}
	}
	body := map[string]any{
		"test":                        t.Name(),
		"chain":                       obs.chain,
		"parent_env_persisted":        false,
		"wire_payload_sha256":         fmt.Sprintf("%x", sum),
		"wire_payload":                json.RawMessage(obs.payload),
		"wire_command":                wire.Command,
		"wire_args":                   wire.Args,
		"helper_cwd":                  obs.helperCwd,
		"profile_dir_deleted_by_test": true,
		"actual_os_args":              obs.child,
		"element_compare":             elements,
		"element_match":               match,
		"helper": map[string]any{
			"pid":                               obs.pid,
			"start_utc":                         obs.started.Format(time.RFC3339Nano),
			"wait_returned_via_on_job_complete": obs.waitSeen,
			"exit_code":                         code,
			"stdout":                            string(obs.stdout),
			"stderr":                            string(obs.stderr),
		},
	}
	writeEvidenceJSON(t, dir, t.Name()+".json", body)
}

func writeLifecycleReceipt(t *testing.T, obs *argvObs) {
	t.Helper()
	dir := os.Getenv("AGENTPULSE_ARGV_WIRE_EVIDENCE")
	if !filepath.IsAbs(dir) {
		return
	}
	obs.mu.Lock()
	defer obs.mu.Unlock()
	body := map[string]any{
		"test":                            t.Name(),
		"listener_run_joined":             obs.runJoined,
		"helper_pid":                      obs.pid,
		"helper_wait_observed":            obs.waitSeen,
		"hard_timeout_kills_test_pe_only": true,
		"helper_exit_claimed":             obs.runJoined && obs.waitSeen,
		"boundary":                        "listener_run_joined is true only after Listener.Run returns, which is after workerWg.Wait. OnJobComplete runs after prepared.Wait. An outside kill of the test PE does not prove the helper exited. This test does not scan the machine, change ACLs, create an OS Job, or kill arbitrary processes.",
	}
	writeEvidenceJSON(t, dir, t.Name()+".lifecycle.json", body)
}

func writeEvidenceJSON(t *testing.T, dir, name string, body any) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	raw, err := json.MarshalIndent(body, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw, '\n')
	if bytes.Contains(bytes.ToLower(raw), []byte("password=")) {
		t.Fatal("refusing to persist an evidence file that contains a password assignment")
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func payloadCwd(raw []byte) string {
	var cmd model.DispatchCommand
	if err := json.Unmarshal(raw, &cmd); err != nil {
		return ""
	}
	return cmd.Cwd
}

func testHelperPath(t *testing.T) string {
	t.Helper()
	p := strings.TrimSpace(os.Getenv("AGENTPULSE_TEST_HELPER"))
	if p == "" {
		t.Fatal("AGENTPULSE_TEST_HELPER is unset; refusing a shell or a real agent")
	}
	if !filepath.IsAbs(p) {
		t.Fatalf("AGENTPULSE_TEST_HELPER must be absolute, got %q", p)
	}
	info, err := os.Stat(p)
	if err != nil {
		t.Fatalf("helper stat: %v", err)
	}
	if info.IsDir() {
		t.Fatalf("helper path is a directory: %s", p)
	}
	return p
}

func syntheticProfile(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	roaming := filepath.Join(dir, "AppData", "Roaming")
	local := filepath.Join(dir, "AppData", "Local")
	if err := os.MkdirAll(roaming, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(local, 0o755); err != nil {
		t.Fatal(err)
	}
	// No broker password and no agent paths. CLI --config and the listener
	// both read this file instead of a user profile.
	body := []byte("version: 1\nmachine_id: qa-box\nbroker:\n  url: tcp://203.0.113.9:1883\n")
	if err := os.WriteFile(syntheticConfigPath(dir), body, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	t.Setenv("APPDATA", roaming)
	t.Setenv("LOCALAPPDATA", local)
	t.Setenv("TMP", dir)
	t.Setenv("TEMP", dir)
	return dir
}

func syntheticConfigPath(profile string) string {
	return filepath.Join(profile, "config.yaml")
}

func installArgvWireSeams(t *testing.T) {
	t.Helper()
	restoreIP := wrapper.SetLocalIPDetectorForTest(func() string { return "203.0.113.10" })
	restoreGit := wrapper.SetRepoGitLookupForTest(func(context.Context, string, ...string) ([]byte, error) {
		return nil, errors.New("argv-wire test does not run git")
	})
	t.Cleanup(restoreIP)
	t.Cleanup(restoreGit)
}

func childBaseEnviron(profile, argvPath string) []string {
	roaming := filepath.Join(profile, "AppData", "Roaming")
	local := filepath.Join(profile, "AppData", "Local")
	env := []string{
		"AGENTPULSE_TEST_ARGV_OUT=" + argvPath,
		"HOME=" + profile,
		"USERPROFILE=" + profile,
		"APPDATA=" + roaming,
		"LOCALAPPDATA=" + local,
		"TEMP=" + profile,
		"TMP=" + profile,
	}
	// Windows needs the system directory to start a process. It is not a credential.
	if runtime.GOOS == "windows" {
		if v := os.Getenv("SYSTEMROOT"); v != "" {
			env = append(env, "SYSTEMROOT="+v)
		}
		if v := os.Getenv("WINDIR"); v != "" {
			env = append(env, "WINDIR="+v)
		}
	}
	return env
}

func readArgvFile(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("helper did not record its argv at %s: %v", path, err)
	}
	var argv []string
	if err := json.Unmarshal(b, &argv); err != nil {
		t.Fatalf("argv json: %v\n%s", err, b)
	}
	if len(argv) == 0 {
		t.Fatal("helper argv file is empty")
	}
	return argv
}

func assertExitCode(t *testing.T, err error, want int) {
	t.Helper()
	if want == 0 {
		if err != nil {
			t.Fatalf("child error = %v, want exit 0", err)
		}
		return
	}
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		t.Fatalf("child error = %v, want exit %d", err, want)
	}
	if ee.ExitCode() == 2 && want != 2 {
		t.Fatalf("child exit 2: a leading -- reached the helper (product separator leak)")
	}
	if ee.ExitCode() != want {
		t.Fatalf("child exit = %d, want %d", ee.ExitCode(), want)
	}
}

func assertNoLeadingSeparator(t *testing.T, command string, args []string, child []string) {
	t.Helper()
	if strings.Contains(command, " --") {
		t.Fatalf("in-memory command still embeds a separator: %q", command)
	}
	if len(args) > 0 && args[0] == "--" {
		t.Fatalf("in-memory args still start with --: %#v", args)
	}
	if len(child) > 1 && child[1] == "--" {
		t.Fatalf("real child argv starts with --: %#v", child)
	}
}

func assertArgvExact(t *testing.T, wire model.DispatchCommand, child []string, wantArgs []string) {
	t.Helper()
	if len(wire.Args) != len(wantArgs) {
		t.Fatalf("wire args = %#v, want %#v", wire.Args, wantArgs)
	}
	for i := range wantArgs {
		if wire.Args[i] != wantArgs[i] {
			t.Fatalf("wire args[%d] = %q, want %q", i, wire.Args[i], wantArgs[i])
		}
	}
	assertChildArgs(t, wire.Command, child, wantArgs)
}

func assertChildArgs(t *testing.T, command string, child []string, wantArgs []string) {
	t.Helper()
	if len(child) != len(wantArgs)+1 {
		t.Fatalf("child argv = %#v, want command %q plus %#v", child, command, wantArgs)
	}
	if !samePath(child[0], command) {
		t.Fatalf("child argv0 = %q, want %q", child[0], command)
	}
	for i := range wantArgs {
		if child[i+1] != wantArgs[i] {
			t.Fatalf("child argv[%d] = %q, want %q (full %#v)", i+1, child[i+1], wantArgs[i], child)
		}
	}
}

func samePath(got, want string) bool {
	if got == want {
		return true
	}
	if filepath.Clean(got) == filepath.Clean(want) {
		return true
	}
	if runtime.GOOS == "windows" && strings.EqualFold(got, want) {
		return true
	}
	return false
}

func containsExactSequence(hay, needle []string) bool {
	if len(needle) == 0 || len(hay) < len(needle) {
		return false
	}
	for i := 0; i+len(needle) <= len(hay); i++ {
		ok := true
		for j := range needle {
			if hay[i+j] != needle[j] {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

type argvMock struct {
	mu        sync.Mutex
	published []argvPublish
	subs      map[string]paho.MessageHandler
}

type argvPublish struct {
	Topic    string
	QoS      byte
	Retained bool
	Payload  []byte
}

func newArgvMock() *argvMock {
	return &argvMock{subs: map[string]paho.MessageHandler{}}
}

func (m *argvMock) payloads(topic string) [][]byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out [][]byte
	for _, p := range m.published {
		if p.Topic == topic && !p.Retained {
			out = append(out, append([]byte(nil), p.Payload...))
		}
	}
	return out
}

func (m *argvMock) IsConnected() bool      { return true }
func (m *argvMock) IsConnectionOpen() bool { return true }
func (m *argvMock) Connect() paho.Token    { return argvToken{} }
func (m *argvMock) Disconnect(uint)        {}

func (m *argvMock) Publish(topic string, qos byte, retained bool, payload interface{}) paho.Token {
	var b []byte
	switch p := payload.(type) {
	case []byte:
		b = append([]byte(nil), p...)
	case string:
		b = []byte(p)
	default:
		b = []byte{}
	}
	m.mu.Lock()
	m.published = append(m.published, argvPublish{Topic: topic, QoS: qos, Retained: retained, Payload: b})
	var handlers []paho.MessageHandler
	for filter, h := range m.subs {
		if filter == topic {
			handlers = append(handlers, h)
		}
	}
	m.mu.Unlock()
	msg := &argvMessage{topic: topic, payload: b, retained: retained, qos: qos}
	for _, h := range handlers {
		h(m, msg)
	}
	return argvToken{}
}

func (m *argvMock) Subscribe(topic string, qos byte, callback paho.MessageHandler) paho.Token {
	m.mu.Lock()
	m.subs[topic] = callback
	m.mu.Unlock()
	return argvToken{}
}

func (m *argvMock) SubscribeMultiple(filters map[string]byte, callback paho.MessageHandler) paho.Token {
	m.mu.Lock()
	for topic := range filters {
		m.subs[topic] = callback
	}
	m.mu.Unlock()
	return argvToken{}
}

func (m *argvMock) Unsubscribe(topics ...string) paho.Token {
	m.mu.Lock()
	for _, topic := range topics {
		delete(m.subs, topic)
	}
	m.mu.Unlock()
	return argvToken{}
}

func (m *argvMock) AddRoute(string, paho.MessageHandler) {}

func (m *argvMock) OptionsReader() paho.ClientOptionsReader {
	return paho.NewOptionsReader(paho.NewClientOptions())
}

type argvToken struct{}

func (argvToken) Wait() bool                     { return true }
func (argvToken) WaitTimeout(time.Duration) bool { return true }
func (argvToken) Done() <-chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}
func (argvToken) Error() error { return nil }

type argvMessage struct {
	topic    string
	payload  []byte
	retained bool
	qos      byte
}

func (m *argvMessage) Duplicate() bool   { return false }
func (m *argvMessage) Qos() byte         { return m.qos }
func (m *argvMessage) Retained() bool    { return m.retained }
func (m *argvMessage) Topic() string     { return m.topic }
func (m *argvMessage) MessageID() uint16 { return 0 }
func (m *argvMessage) Payload() []byte   { return m.payload }
func (m *argvMessage) Ack()              {}
