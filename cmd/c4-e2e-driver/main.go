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
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"
	"github.com/mayingjie198448/agentpulse-cli/internal/loopbackmqtt"
	"github.com/mayingjie198448/agentpulse-cli/internal/model"
	"github.com/mayingjie198448/agentpulse-cli/internal/store/mqtt"
)

func main() {
	execute := flag.Bool("execute", false, "run the chain in a private temp root")
	cliPath := flag.String("cli", "", "path to the agentpulse binary")
	helperPath := flag.String("helper", "", "path to the agentpulse-test-helper binary")
	evidencePath := flag.String("evidence", "", "path for the JSON evidence file")
	flag.Parse()

	if !*execute {
		fmt.Println(plan)
		os.Exit(0)
	}
	if *cliPath == "" || *helperPath == "" || *evidencePath == "" {
		fmt.Fprintln(os.Stderr, "--execute requires --cli, --helper, and --evidence")
		os.Exit(2)
	}
	report, err := run(*cliPath, *helperPath)
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
stop: SIGINT to the listen process this driver started
isolation: no --dangerously-skip-permissions, no AppContainer loopback exemption; non-danger child stays not_isolated
`

type evidence struct {
	BrokerURL    string   `json:"broker_url"`
	BrokerAddr   string   `json:"broker_addr"`
	Root         string   `json:"root"`
	RootRemoved  bool     `json:"root_removed"`
	ListenerPID  int      `json:"listener_pid"`
	ListenerExit int      `json:"listener_exit"`
	Commands     []string `json:"commands"`
	OK           jobProof `json:"exit_0"`
	Fail         jobProof `json:"exit_7"`
	Checks       []string `json:"checks"`
	Failures     []string `json:"failures"`
	Permissions  []string `json:"permissions"`
}

type jobProof struct {
	JobID      string    `json:"job_id"`
	DispatchRC int       `json:"dispatch_rc"`
	Argv       []string  `json:"argv"`
	Statuses   []string  `json:"statuses"`
	Progress   []float64 `json:"progress"`
	EndedAt    string    `json:"ended_at"`
	Error      string    `json:"error"`
	Command    string    `json:"command"`
	Logs       []string  `json:"logs"`
	Wire       string    `json:"wire"`
}

type collector struct {
	mu      sync.Mutex
	status  map[string][]model.Job
	logs    map[string][]model.LogEntry
	wire    map[string]string
	machine []model.Machine
}

func run(cliPath, helperPath string) (*evidence, error) {
	cliPath, err := filepath.Abs(cliPath)
	if err != nil {
		return nil, err
	}
	helperPath, err = filepath.Abs(helperPath)
	if err != nil {
		return nil, err
	}
	root, err := os.MkdirTemp("", "agentpulse-e2e-")
	if err != nil {
		return nil, err
	}
	ev := &evidence{
		Root: root,
		Permissions: []string{
			"broker bind 127.0.0.1 ephemeral only",
			"no service install, firewall, ACL, or route change",
			"no credential, real home, or agy binary",
			"listen --no-attach; no --dangerously-skip-permissions",
			"non-danger Prepare is not_isolated; no loopback exemption added",
			"processes and config exist only under this root",
		},
	}
	var listener *exec.Cmd
	var broker *loopbackmqtt.Broker
	defer func() {
		if listener != nil && listener.Process != nil {
			_ = listener.Process.Kill()
		}
		if broker != nil {
			_ = broker.Close()
		}
		if ev.Failures == nil && ev.ListenerExit == 0 {
			if rmErr := os.RemoveAll(root); rmErr == nil {
				ev.RootRemoved = true
			}
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
	obs := observe(ev.BrokerURL, col)
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
		"PATH=",
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
	if err := listener.Start(); err != nil {
		return ev, err
	}
	ev.ListenerPID = listener.Process.Pid

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
	ev.Fail = dispatchJob(cliPath, cfgPath, ev.BrokerURL, helperPath, work, env, "job-e2e-fail", "7", failArgv, col, &ev.Commands)
	checkJob(ev, "exit_0", ev.OK, 0, "done")
	checkJob(ev, "exit_7", ev.Fail, 7, "failed")

	_ = listener.Process.Signal(os.Interrupt)
	waitDone := make(chan error, 1)
	go func() { waitDone <- listener.Wait() }()
	select {
	case err := <-waitDone:
		if err == nil {
			ev.ListenerExit = 0
		} else if ee, ok := err.(*exec.ExitError); ok {
			ev.ListenerExit = ee.ExitCode()
		} else {
			ev.ListenerExit = 1
			ev.Failures = append(ev.Failures, "listener wait: "+err.Error())
		}
	case <-time.After(5 * time.Second):
		_ = listener.Process.Kill()
		ev.ListenerExit = -1
		ev.Failures = append(ev.Failures, "listener did not exit after SIGINT")
	}
	listener = nil

	if len(ev.Failures) > 0 {
		return ev, fmt.Errorf("%s", strings.Join(ev.Failures, "; "))
	}
	ev.Checks = append(ev.Checks, "broker 127.0.0.1", "listen subscribed", "exit 0 done", "exit 7 failed", "redaction", "ended_at", "argv")
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
		ev.Failures = append(ev.Failures, name+" dispatch rc")
	}
	if wantExit == 7 {
		if job.DispatchRC == 0 || !strings.Contains(job.Error, "7") {
			ev.Failures = append(ev.Failures, name+" exit 7 rc/error "+job.Error)
		}
	}
	if !strings.Contains(job.Wire, job.JobID) || !strings.Contains(job.Wire, "stream") {
		ev.Failures = append(ev.Failures, name+" wire")
	}
	if !strings.Contains(job.Command, "agentpulse-test-helper") || !strings.Contains(job.Command, "stream "+fmt.Sprint(wantExit)) {
		ev.Failures = append(ev.Failures, name+" command is not the helper")
	}
}

func observe(url string, col *collector) paho.Client {
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
	if !token.WaitTimeout(5*time.Second) || token.Error() != nil {
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
		sub.WaitTimeout(3 * time.Second)
	}
	return c
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
