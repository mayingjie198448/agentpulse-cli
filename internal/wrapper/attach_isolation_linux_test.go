//go:build linux

package wrapper_test

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"
	"github.com/mayingjie198448/agentpulse-cli/internal/config"
	"github.com/mayingjie198448/agentpulse-cli/internal/model"
	"github.com/mayingjie198448/agentpulse-cli/internal/wrapper"
)

const (
	allowedPrompt = "allowed-attach-token"
	bypassSecret  = "bypass-secret-token"
)

// TestAllowlistedDiscoveryDoesNotReadBypassPID is the native regression for a
// non-empty AGENTPULSE_ATTACH_PID_ALLOWLIST. The default lister must attach
// only the synthetic agy we spawned, and the guard refuses ps plus every
// other PID's /proc files and log before those bytes can be read.
func TestAllowlistedDiscoveryDoesNotReadBypassPID(t *testing.T) {
	root := t.TempDir()
	isolateProcessEnv(t, root)
	home := filepath.Join(root, "home")
	logDir := filepath.Join(root, "logs")
	brainDir := filepath.Join(root, "brain")
	opencodeDir := filepath.Join(root, "opencode-log")
	for _, dir := range []string{logDir, brainDir, opencodeDir} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	t.Setenv("AGENTPULSE_TEST_AGY_LOG_DIR", logDir)
	t.Setenv("AGENTPULSE_TEST_AGY_BRAIN_DIR", brainDir)
	t.Setenv("AGENTPULSE_TEST_OPENCODE_LOG_DIR", opencodeDir)

	bin := buildSyntheticAgy(t, root)
	allowedLog := filepath.Join(logDir, "cli-owned.log")
	bypassLog := filepath.Join(logDir, "cli-bypass.log")
	allowedPID := spawnDetachedAgy(t, bin, home, allowedPrompt, allowedLog)
	bypassPID := spawnDetachedAgy(t, bin, home, bypassSecret, bypassLog)
	waitReparented(t, allowedPID)
	waitReparented(t, bypassPID)
	waitFileSize(t, allowedLog)
	waitFileSize(t, bypassLog)

	opens, stopWatch := watchOpens(t, bypassLog)
	defer stopWatch()
	calibrate, err := os.Open(bypassLog)
	if err != nil {
		t.Fatalf("calibration open: %v", err)
	}
	_ = calibrate.Close()
	if opens() == 0 {
		t.Fatal("inotify did not observe the calibration open; it cannot prove the bypass log stays unread")
	}
	for opens() > 0 {
	}

	guard := newIsoGuard(t, root, bypassLog, bypassPID, allowedPID)
	restore := wrapper.SetIsolationHooksForTest(guard.hooks())
	defer restore()

	broker := startOneShotBroker(t)
	sub := dialMQTT(t, broker, "iso-sub")
	defer sub.Disconnect(200)
	payloads := subscribeAll(t, sub)
	pub := dialMQTT(t, broker, "iso-pub")
	defer pub.Disconnect(200)

	t.Setenv("AGENTPULSE_ATTACH_PID_ALLOWLIST", strconv.Itoa(allowedPID))
	enabled := true
	mgr := wrapper.NewAttachManager(wrapper.AttachManagerConfig{
		MachineID:      "iso-node",
		MQTTClient:     pub,
		ScanInterval:   40 * time.Millisecond,
		AgyLogDir:      logDir,
		AgyBrainDir:    brainDir,
		OpenCodeLogDir: opencodeDir,
		Quiet:          true,
		Agents: []config.AgentConfig{{
			ID:      "agy",
			Alias:   []string{"agy"},
			Enabled: &enabled,
		}},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr.Start(ctx)

	running, attached := waitJob(payloads, func(job model.Job) bool {
		return job.Status == model.StatusRunning && job.Title == allowedPrompt
	}, 4*time.Second)
	mgr.Stop()

	guard.mu.Lock()
	psCalls := append([][]string(nil), guard.psCalls...)
	denied := append([]string(nil), guard.denied...)
	guard.mu.Unlock()
	if len(psCalls) != 0 {
		t.Fatalf("allowlisted discovery invoked ps or lsof: %q", psCalls)
	}
	if len(denied) != 0 {
		t.Fatalf("allowlisted discovery touched a bypass PID or log: %s", strings.Join(denied, "; "))
	}
	if !attached {
		t.Fatalf("allowed synthetic agy was not attached; payloads=%s", strings.Join(payloads.snapshot(), "\n"))
	}
	if opens() != 0 {
		t.Fatal("bypass log was opened after the guard was installed")
	}
	if running.Title != allowedPrompt {
		t.Fatalf("attached title = %q", running.Title)
	}
	if running.Progress == 100 || running.Status == model.StatusDone {
		t.Fatalf("running job already terminal: status=%s progress=%v", running.Status, running.Progress)
	}
	assertNoSecret(t, payloads.snapshot())
	if !processAlive(allowedPID) || !processAlive(bypassPID) {
		t.Fatal("stopping the monitor killed a synthetic CLI")
	}

	// Same allowlist, no exit checker: a real exit must not become DONE/100%.
	mgr = wrapper.NewAttachManager(wrapper.AttachManagerConfig{
		MachineID:      "iso-node",
		MQTTClient:     pub,
		ScanInterval:   40 * time.Millisecond,
		AgyLogDir:      logDir,
		AgyBrainDir:    brainDir,
		OpenCodeLogDir: opencodeDir,
		Quiet:          true,
		Agents: []config.AgentConfig{{
			ID:      "agy",
			Alias:   []string{"agy"},
			Enabled: &enabled,
		}},
	})
	mgr.Start(ctx)
	mark := payloads.len()
	if _, ok := waitJobSince(payloads, mark, func(job model.Job) bool {
		return job.Status == model.StatusRunning && job.Title == allowedPrompt
	}, 4*time.Second); !ok {
		t.Fatal("allowed process was not re-attached before the exit watch")
	}
	exitMark := payloads.len()
	if err := syscall.Kill(allowedPID, syscall.SIGTERM); err != nil {
		t.Fatalf("signal allowed process: %v", err)
	}
	finalJob, ok := waitJobSince(payloads, exitMark, func(job model.Job) bool {
		return job.Title == allowedPrompt && strings.Contains(job.Error, "无法判定退出码")
	}, 5*time.Second)
	if !ok {
		t.Fatal("allowed process exit did not publish a terminal status")
	}
	mgr.Stop()

	guard.mu.Lock()
	psCalls = append([][]string(nil), guard.psCalls...)
	denied = append([]string(nil), guard.denied...)
	guard.mu.Unlock()
	if len(psCalls) != 0 {
		t.Fatalf("allowlisted discovery invoked ps or lsof during exit watch: %q", psCalls)
	}
	if len(denied) != 0 {
		t.Fatalf("exit watch touched a bypass PID or log: %s", strings.Join(denied, "; "))
	}
	if opens() != 0 {
		t.Fatal("bypass log was opened during exit watch")
	}
	if finalJob.Status == model.StatusDone || finalJob.Progress == 100 {
		t.Fatalf("unknown exit became terminal success: status=%s progress=%v error=%q", finalJob.Status, finalJob.Progress, finalJob.Error)
	}
	if finalJob.Status != model.StatusFailed {
		t.Fatalf("unknown exit status = %s, want failed", finalJob.Status)
	}
	if !strings.Contains(finalJob.Error, "无法判定退出码") {
		t.Fatalf("unknown exit error = %q", finalJob.Error)
	}
	if !processAlive(bypassPID) {
		t.Fatal("bypass process died while the allowed process was monitored")
	}
	assertNoSecret(t, payloads.snapshot())
}

func assertNoSecret(t *testing.T, payloads []string) {
	t.Helper()
	for _, payload := range payloads {
		if strings.Contains(payload, bypassSecret) {
			t.Fatalf("published payload contains the bypass secret: %s", payload)
		}
	}
}

type isoGuard struct {
	t         *testing.T
	root      string
	bypassLog string
	bypassPID int
	allowed   map[int]bool
	mu        sync.Mutex
	psCalls   [][]string
	denied    []string
}

func newIsoGuard(t *testing.T, root, bypassLog string, bypassPID, allowedPID int) *isoGuard {
	t.Helper()
	return &isoGuard{
		t:         t,
		root:      root,
		bypassLog: bypassLog,
		bypassPID: bypassPID,
		allowed:   map[int]bool{allowedPID: true},
	}
}

func (g *isoGuard) hooks() wrapper.IsolationHooks {
	return wrapper.IsolationHooks{
		ReadFile:       g.readFile,
		Readlink:       g.readlink,
		Open:           g.open,
		ReadDir:        g.readDir,
		CommandContext: g.command,
	}
}

func (g *isoGuard) command(ctx context.Context, name string, args ...string) *exec.Cmd {
	base := filepath.Base(name)
	if base == "ps" || base == "lsof" {
		g.mu.Lock()
		g.psCalls = append(g.psCalls, append([]string{name}, args...))
		g.denied = append(g.denied, "command "+name+" "+strings.Join(args, " "))
		g.mu.Unlock()
		return exec.CommandContext(ctx, "/bin/false")
	}
	return exec.CommandContext(ctx, name, args...)
}

func (g *isoGuard) readFile(path string) ([]byte, error) {
	if why, ok := g.refuse(path); !ok {
		return nil, fmt.Errorf("refused %s", why)
	}
	return os.ReadFile(path)
}

func (g *isoGuard) readlink(path string) (string, error) {
	if why, ok := g.refuse(path); !ok {
		return "", fmt.Errorf("refused %s", why)
	}
	return os.Readlink(path)
}

func (g *isoGuard) open(path string) (*os.File, error) {
	if why, ok := g.refuse(path); !ok {
		return nil, fmt.Errorf("refused %s", why)
	}
	return os.Open(path)
}

func (g *isoGuard) readDir(path string) ([]os.DirEntry, error) {
	if why, ok := g.refuse(path); !ok {
		return nil, fmt.Errorf("refused %s", why)
	}
	return os.ReadDir(path)
}

func (g *isoGuard) refuse(path string) (string, bool) {
	if path == g.bypassLog || strings.Contains(path, bypassSecret) {
		g.note("open bypass log " + path)
		return path, false
	}
	if path == "/proc" || strings.HasPrefix(path, "/proc/") {
		if path == "/proc/stat" {
			return "", true
		}
		pid, ok := procPID(path)
		if !ok || !g.allowed[pid] {
			why := "proc " + path
			g.note(why)
			return why, false
		}
		return "", true
	}
	if path == g.root || strings.HasPrefix(path, g.root+string(os.PathSeparator)) {
		return "", true
	}
	g.note("outside private root " + path)
	return path, false
}

func (g *isoGuard) note(why string) {
	g.mu.Lock()
	g.denied = append(g.denied, why)
	g.mu.Unlock()
}

func procPID(path string) (int, bool) {
	rest := strings.TrimPrefix(path, "/proc/")
	num, _, _ := strings.Cut(rest, "/")
	pid, err := strconv.Atoi(num)
	if err != nil || pid <= 0 {
		return 0, false
	}
	return pid, true
}

func buildSyntheticAgy(t *testing.T, root string) string {
	t.Helper()
	src, err := os.ReadFile(filepath.Join("testdata", "synthetic_agy.go"))
	if err != nil {
		t.Fatalf("read synthetic source: %v", err)
	}
	dir := filepath.Join(root, "synth")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	// The fixture is stdlib-only. Pin it to the system compiler so the build
	// does not download a toolchain or touch the shared module cache.
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module syntheticagy\n\ngo 1.22\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), src, 0600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(root, "bin", "agy")
	if err := os.MkdirAll(filepath.Dir(out), 0700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "build", "-buildvcs=false", "-o", out, ".")
	cmd.Dir = dir
	cmd.Env = []string{
		"PATH=/usr/bin:/bin",
		"HOME=" + filepath.Join(root, "home"),
		"TMPDIR=" + filepath.Join(root, "tmp"),
		"GOCACHE=" + filepath.Join(root, "gocache"),
		"GOPATH=" + filepath.Join(root, "gopath"),
		"GOMODCACHE=" + filepath.Join(root, "gomodcache"),
		"GOENV=off",
		"GOPROXY=off",
		"GOSUMDB=off",
		"GOTOOLCHAIN=local",
		"GO111MODULE=on",
		"CGO_ENABLED=0",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"LANG=C",
	}
	if raw, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build synthetic agy: %v\n%s", err, raw)
	}
	return out
}

func spawnDetachedAgy(t *testing.T, bin, home, prompt, logPath string) int {
	t.Helper()
	cmd := exec.Command(bin, "-p", prompt)
	cmd.Dir = home
	cmd.Env = []string{
		"PATH=/usr/bin:/bin",
		"HOME=" + home,
		"TMPDIR=" + filepath.Join(filepath.Dir(home), "tmp"),
		"AGENTPULSE_SYNTHETIC_DETACH=1",
		"AGENTPULSE_SYNTHETIC_LOG=" + logPath,
		"LANG=C",
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("detach %s: %v\n%s", prompt, err, out)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil || pid <= 1 {
		t.Fatalf("detach pid %q: %v", out, err)
	}
	t.Cleanup(func() {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	})
	return pid
}

func waitReparented(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	var last error
	for time.Now().Before(deadline) {
		ppid, err := readPPID(pid)
		if err == nil && ppid != os.Getpid() && ppid > 0 {
			return
		}
		last = err
		if err == nil {
			last = fmt.Errorf("ppid %d is still the test process", ppid)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("pid %d was not reparented: %v", pid, last)
}

func readPPID(pid int) (int, error) {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "PPid:") {
			fields := strings.Fields(line)
			if len(fields) != 2 {
				return 0, fmt.Errorf("bad ppid line %q", line)
			}
			return strconv.Atoi(fields[1])
		}
	}
	return 0, fmt.Errorf("no PPid in status")
}

func waitFileSize(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		info, err := os.Stat(path)
		if err == nil && info.Size() > 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("synthetic log was not created: %s", path)
}

func processAlive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

func watchOpens(t *testing.T, path string) (func() int, func()) {
	t.Helper()
	fd, err := syscall.InotifyInit1(syscall.IN_NONBLOCK | syscall.IN_CLOEXEC)
	if err != nil {
		t.Fatalf("inotify: %v", err)
	}
	if _, err := syscall.InotifyAddWatch(fd, path, syscall.IN_OPEN); err != nil {
		_ = syscall.Close(fd)
		t.Fatalf("inotify add: %v", err)
	}
	return func() int { return drainInotify(fd) }, func() { _ = syscall.Close(fd) }
}

func drainInotify(fd int) int {
	buf := make([]byte, 4096)
	total := 0
	for {
		n, err := syscall.Read(fd, buf)
		if err != nil {
			return total
		}
		size := syscall.SizeofInotifyEvent
		i := 0
		for i+size <= n {
			nameLen := int(binary.LittleEndian.Uint32(buf[i+12 : i+16]))
			total++
			i += size + nameLen
		}
	}
}

type payloadLog struct {
	mu    sync.Mutex
	items []string
}

func (p *payloadLog) add(payload string) {
	p.mu.Lock()
	p.items = append(p.items, payload)
	p.mu.Unlock()
}

func (p *payloadLog) snapshot() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]string, len(p.items))
	copy(out, p.items)
	return out
}

func (p *payloadLog) len() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.items)
}

func waitJob(payloads *payloadLog, match func(model.Job) bool, timeout time.Duration) (model.Job, bool) {
	return waitJobSince(payloads, 0, match, timeout)
}

func waitJobSince(payloads *payloadLog, start int, match func(model.Job) bool, timeout time.Duration) (model.Job, bool) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		items := payloads.snapshot()
		if start < 0 {
			start = 0
		}
		if start > len(items) {
			start = len(items)
		}
		for _, payload := range items[start:] {
			var job model.Job
			if json.Unmarshal([]byte(payload), &job) != nil || job.ID == "" {
				continue
			}
			if match(job) {
				return job, true
			}
		}
		time.Sleep(40 * time.Millisecond)
	}
	return model.Job{}, false
}

func subscribeAll(t *testing.T, client paho.Client) *payloadLog {
	t.Helper()
	log := &payloadLog{}
	token := client.Subscribe("agentpulse/v1/#", 0, func(_ paho.Client, msg paho.Message) {
		log.add(string(msg.Payload()))
	})
	if !token.WaitTimeout(3*time.Second) || token.Error() != nil {
		t.Fatalf("subscribe: %v", token.Error())
	}
	return log
}

func dialMQTT(t *testing.T, broker *oneShotBroker, clientID string) paho.Client {
	t.Helper()
	opts := paho.NewClientOptions().
		AddBroker(broker.URL).
		SetClientID(clientID).
		SetProtocolVersion(4).
		SetAutoReconnect(false).
		SetConnectRetry(false).
		SetCleanSession(true).
		SetOrderMatters(false).
		SetConnectTimeout(3 * time.Second).
		SetPingTimeout(time.Second).
		SetWriteTimeout(2 * time.Second).
		SetKeepAlive(30 * time.Second)
	client := paho.NewClient(opts)
	token := client.Connect()
	if !token.WaitTimeout(4*time.Second) || token.Error() != nil {
		t.Fatalf("connect %s: %v\nbroker: %s", clientID, token.Error(), broker.trace())
	}
	return client
}

type oneShotBroker struct {
	ln    net.Listener
	URL   string
	mu    sync.Mutex
	subs  []brokerSub
	notes []string
}

type brokerSub struct {
	conn   *brokerConn
	filter string
}

type brokerConn struct {
	c  net.Conn
	mu sync.Mutex
}

func startOneShotBroker(t *testing.T) *oneShotBroker {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	b := &oneShotBroker{ln: ln, URL: "tcp://" + ln.Addr().String()}
	go b.accept()
	t.Cleanup(func() { _ = ln.Close() })
	return b
}

func (b *oneShotBroker) note(format string, args ...interface{}) {
	b.mu.Lock()
	b.notes = append(b.notes, fmt.Sprintf(format, args...))
	b.mu.Unlock()
}

func (b *oneShotBroker) trace() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.Join(b.notes, "\n")
}

func (b *oneShotBroker) accept() {
	for {
		conn, err := b.ln.Accept()
		if err != nil {
			return
		}
		go b.serve(&brokerConn{c: conn})
	}
}

func (b *oneShotBroker) serve(conn *brokerConn) {
	defer conn.c.Close()
	for {
		header, body, err := readMQTTPacket(conn.c)
		if err != nil {
			b.drop(conn)
			return
		}
		kind := header >> 4
		switch kind {
		case 1: // CONNECT
			if err := conn.write(0x20, []byte{0x00, 0x00}); err != nil {
				b.drop(conn)
				return
			}
		case 3: // PUBLISH
			topic, payload, pktID, qos, retain, ok := parsePublish(header, body)
			if !ok {
				b.drop(conn)
				return
			}
			b.fanout(topic, payload, retain)
			if qos == 1 {
				ack := []byte{byte(pktID >> 8), byte(pktID)}
				if err := conn.write(0x40, ack); err != nil {
					b.drop(conn)
					return
				}
			}
		case 8: // SUBSCRIBE
			if len(body) < 2 {
				b.drop(conn)
				return
			}
			pktID := binary.BigEndian.Uint16(body[:2])
			rest := body[2:]
			var codes []byte
			for len(rest) >= 3 {
				filter, next, ok := readMQTTString(rest)
				if !ok || len(next) < 1 {
					break
				}
				rest = next[1:]
				b.mu.Lock()
				b.subs = append(b.subs, brokerSub{conn: conn, filter: filter})
				b.mu.Unlock()
				codes = append(codes, 0)
			}
			ack := []byte{byte(pktID >> 8), byte(pktID)}
			ack = append(ack, codes...)
			if err := conn.write(0x90, ack); err != nil {
				b.drop(conn)
				return
			}
		case 12: // PINGREQ
			if err := conn.write(0xD0, nil); err != nil {
				b.drop(conn)
				return
			}
		case 14: // DISCONNECT
			b.drop(conn)
			return
		default:
			b.note("ignore packet %d len %d", kind, len(body))
		}
	}
}

func (b *oneShotBroker) drop(conn *brokerConn) {
	b.mu.Lock()
	kept := b.subs[:0]
	for _, sub := range b.subs {
		if sub.conn != conn {
			kept = append(kept, sub)
		}
	}
	b.subs = kept
	b.mu.Unlock()
}

func (b *oneShotBroker) fanout(topic string, payload []byte, retain bool) {
	b.mu.Lock()
	subs := append([]brokerSub(nil), b.subs...)
	b.mu.Unlock()
	body := mqttPublishBody(topic, payload)
	header := byte(0x30)
	if retain {
		header |= 0x01
	}
	for _, sub := range subs {
		if !topicMatch(sub.filter, topic) {
			continue
		}
		_ = sub.conn.write(header, body)
	}
}

func (c *brokerConn) write(header byte, body []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	_ = c.c.SetWriteDeadline(time.Now().Add(2 * time.Second))
	return writeMQTTPacket(c.c, header, body)
}

func parsePublish(header byte, body []byte) (topic string, payload []byte, pktID uint16, qos byte, retain bool, ok bool) {
	topic, rest, ok := readMQTTString(body)
	if !ok {
		return "", nil, 0, 0, false, false
	}
	qos = (header >> 1) & 0x03
	retain = header&0x01 != 0
	if qos > 0 {
		if len(rest) < 2 {
			return "", nil, 0, 0, false, false
		}
		pktID = binary.BigEndian.Uint16(rest[:2])
		rest = rest[2:]
	}
	return topic, rest, pktID, qos, retain, true
}

func mqttPublishBody(topic string, payload []byte) []byte {
	body := make([]byte, 2+len(topic)+len(payload))
	binary.BigEndian.PutUint16(body[:2], uint16(len(topic)))
	copy(body[2:], topic)
	copy(body[2+len(topic):], payload)
	return body
}

func topicMatch(filter, topic string) bool {
	if filter == "#" || filter == topic {
		return true
	}
	fp := strings.Split(filter, "/")
	tp := strings.Split(topic, "/")
	for i := 0; i < len(fp); i++ {
		if fp[i] == "#" {
			return true
		}
		if i >= len(tp) {
			return false
		}
		if fp[i] != "+" && fp[i] != tp[i] {
			return false
		}
	}
	return len(fp) == len(tp)
}

func readMQTTPacket(r io.Reader) (byte, []byte, error) {
	var hdr [1]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return 0, nil, err
	}
	rem, err := readMQTTRemaining(r)
	if err != nil {
		return 0, nil, err
	}
	body := make([]byte, rem)
	if _, err := io.ReadFull(r, body); err != nil {
		return 0, nil, err
	}
	return hdr[0], body, nil
}

func readMQTTRemaining(r io.Reader) (int, error) {
	mul := 1
	val := 0
	for i := 0; i < 4; i++ {
		var b [1]byte
		if _, err := io.ReadFull(r, b[:]); err != nil {
			return 0, err
		}
		val += int(b[0]&127) * mul
		if b[0]&128 == 0 {
			return val, nil
		}
		mul *= 128
	}
	return 0, fmt.Errorf("bad remaining length")
}

func readMQTTString(b []byte) (string, []byte, bool) {
	if len(b) < 2 {
		return "", nil, false
	}
	n := int(binary.BigEndian.Uint16(b[:2]))
	if n < 0 || len(b) < 2+n {
		return "", nil, false
	}
	return string(b[2 : 2+n]), b[2+n:], true
}

func writeMQTTPacket(w io.Writer, header byte, body []byte) error {
	if _, err := w.Write([]byte{header}); err != nil {
		return err
	}
	if _, err := w.Write(encodeMQTTRemaining(len(body))); err != nil {
		return err
	}
	_, err := w.Write(body)
	return err
}

func encodeMQTTRemaining(n int) []byte {
	var out []byte
	for {
		digit := n % 128
		n /= 128
		if n > 0 {
			digit |= 0x80
		}
		out = append(out, byte(digit))
		if n == 0 {
			return out
		}
	}
}
