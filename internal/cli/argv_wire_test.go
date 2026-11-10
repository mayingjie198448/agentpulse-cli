package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
	assertExitCode(t, err, 0)
	wantArgs := []string{"record-argv", "0", "git", "checkout", "--", "README 中文.md"}
	assertChildArgs(t, helper, got, wantArgs)
	if !containsExactSequence(got[1:], []string{"checkout", "--", "README 中文.md"}) {
		t.Fatalf("child lost internal --: %#v", got)
	}
}

func TestDispatchWireChildArgv_HistoricalEmbeddedCommand(t *testing.T) {
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
	assertExitCode(t, err, 0)
	// The injected command string still contains the separator.
	// The real child must see the helper path and must keep the later "--".
	if !strings.Contains(payload.Command, " --") {
		t.Fatal("fixture lost the embedded separator")
	}
	assertChildArgs(t, helper, got, wantArgs)
}

func TestDispatchWireChildArgv_ExitCode(t *testing.T) {
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
	wireMock := newArgvMock()
	prev := dispatchInvoker
	dispatchInvoker = func(ctx context.Context, cfg dispatch.Config) (*model.DispatchCommand, error) {
		if !cfg.Force {
			t.Errorf("dispatch command did not keep --force")
		}
		if cfg.MQTTClient != nil {
			t.Errorf("CLI constructed a real MQTT client before the in-memory hook")
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
	got, runErr := runListenerOnPayload(t, profile, published[0], jobID)
	return wire, got, runErr
}

func runInjectedWire(t *testing.T, payload model.DispatchCommand) ([]string, error) {
	t.Helper()
	profile := syntheticProfile(t)
	if payload.Cwd == "" {
		payload.Cwd = profile
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal injected wire: %v", err)
	}
	t.Log("wire=injected historical payload in memory; not a CLI parse and not an MQTT network publish")
	return runListenerOnPayload(t, profile, raw, payload.JobID)
}

func runListenerOnPayload(t *testing.T, profile string, payload []byte, jobID string) ([]string, error) {
	t.Helper()
	argvPath := filepath.Join(profile, "argv-"+jobID+".json")
	t.Setenv("AGENTPULSE_TEST_ARGV_OUT", argvPath)
	_ = os.Remove(argvPath)

	mock := newArgvMock()
	done := make(chan error, 1)
	listening := make(chan struct{})
	l, err := wrapper.NewListener(wrapper.ListenerConfig{
		MachineID:         "qa-box",
		StatusFile:        filepath.Join(profile, "status-"+jobID+".json"),
		MQTTClient:        mock,
		Stub:              false,
		Quiet:             true,
		DisableBareAttach: true,
		MaxConcurrentJobs: 1,
		AuditPath:         filepath.Join(profile, "audit-"+jobID+".jsonl"),
		RealHome:          profile,
		Stdout:            bytes.NewBuffer(nil),
		Stderr:            bytes.NewBuffer(nil),
		OnListening: func(string) {
			close(listening)
		},
		OnJobComplete: func(job model.Job, runErr error) {
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
	defer cancel()
	go func() {
		_ = l.Run(ctx)
	}()
	t.Cleanup(func() {
		l.Stop()
		cancel()
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
	t.Logf("wire=in-memory payload bytes; child=real process argv file %s", argvPath)
	return got, runErr
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
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	t.Setenv("APPDATA", roaming)
	t.Setenv("LOCALAPPDATA", local)
	t.Setenv("TMP", dir)
	t.Setenv("TEMP", dir)
	return dir
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
