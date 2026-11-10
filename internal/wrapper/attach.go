package wrapper

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"
	"github.com/mayingjie198448/agentpulse-cli/internal/config"
	"github.com/mayingjie198448/agentpulse-cli/internal/model"
	"github.com/mayingjie198448/agentpulse-cli/internal/store/mqtt"
)

// ProcessInfo represents an OS process snapshot.
type ProcessInfo struct {
	PID       int
	PPID      int
	StartTime time.Time
	Args      []string
	Cwd       string
}

// ProcessLister is a function type for enumerating running OS processes.
type ProcessLister func(ctx context.Context) ([]ProcessInfo, error)

var blockedProcessNames = map[string]bool{
	"agentpulse": true,
	"grep":       true,
	"egrep":      true,
	"fgrep":      true,
	"pgrep":      true,
	"pkill":      true,
	"kill":       true,
	"ps":         true,
	"top":        true,
	"htop":       true,
	"vim":        true,
	"nvim":       true,
	"vi":         true,
	"emacs":      true,
	"nano":       true,
	"cat":        true,
	"less":       true,
	"more":       true,
	"tail":       true,
	"head":       true,
	"git":        true,
	"go":         true,
	"make":       true,
	"zsh":        true,
	"bash":       true,
	"sh":         true,
	"fish":       true,
	"tmux":       true,
	"screen":     true,
	"python":     true,
	"python3":    true,
	"node":       true,
	"test":       true,
}

// ParsePIDAllowlist parses a comma-, space-, or semicolon-separated list of PIDs into a slice of integers.
func ParsePIDAllowlist(s string) []int {
	var pids []int
	fields := strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n' || r == ';'
	})
	for _, f := range fields {
		if pid, err := strconv.Atoi(strings.TrimSpace(f)); err == nil && pid > 0 {
			pids = append(pids, pid)
		}
	}
	return pids
}

// TokenizeCommandLine splits a command line string into tokens respecting quotes.
func TokenizeCommandLine(s string) []string {
	var args []string
	var current strings.Builder
	inSingle := false
	inDouble := false
	escaped := false

	for i := 0; i < len(s); i++ {
		ch := s[i]
		if escaped {
			current.WriteByte(ch)
			escaped = false
			continue
		}
		if ch == '\\' {
			if i+3 < len(s) && s[i+1] == '0' && s[i+2] == '1' && s[i+3] == '2' {
				current.WriteByte('\n')
				i += 3
				continue
			}
			escaped = true
			continue
		}
		if ch == '\'' && !inDouble {
			inSingle = !inSingle
			continue
		}
		if ch == '"' && !inSingle {
			inDouble = !inDouble
			continue
		}
		if (ch == ' ' || ch == '\t') && !inSingle && !inDouble {
			if current.Len() > 0 {
				args = append(args, current.String())
				current.Reset()
			}
			continue
		}
		current.WriteByte(ch)
	}
	if current.Len() > 0 {
		args = append(args, current.String())
	}
	return args
}

// MatchAgentProcess checks whether a given process corresponds to an enabled AI agent CLI.
// It verifies that:
// 1. PID is valid and not self or child of self.
// 2. The PID is not a tracked dispatch job.
// 3. The process executable is not a shell, editor, grep, or agentpulse sub-command.
// 4. The binary name or path matches an enabled agent in config (ag.IsEnabled() == true).
func MatchAgentProcess(
	p ProcessInfo,
	agents []config.AgentConfig,
	isDispatchPID func(int) bool,
	selfPID int,
) (config.AgentConfig, bool) {
	if p.PID <= 1 || p.PID == selfPID || p.PPID == selfPID {
		return config.AgentConfig{}, false
	}
	if isDispatchPID != nil && isDispatchPID(p.PID) {
		return config.AgentConfig{}, false
	}
	if len(p.Args) == 0 {
		return config.AgentConfig{}, false
	}

	binBase := filepath.Base(p.Args[0])
	binLower := strings.ToLower(binBase)

	if strings.Contains(binLower, "agentpulse") || blockedProcessNames[binLower] || strings.HasSuffix(binLower, ".test") {
		return config.AgentConfig{}, false
	}

	for _, arg := range p.Args {
		argLower := strings.ToLower(arg)
		if strings.Contains(argLower, "agentpulse") {
			return config.AgentConfig{}, false
		}
	}

	for _, ag := range agents {
		if !ag.IsEnabled() {
			continue
		}
		agIDLower := strings.ToLower(ag.ID)
		if binLower == agIDLower {
			return ag, true
		}
		for _, alias := range ag.Alias {
			if alias != "" && binLower == strings.ToLower(alias) {
				return ag, true
			}
		}
		if ag.Path != "" {
			if p.Args[0] == ag.Path || binLower == strings.ToLower(filepath.Base(ag.Path)) {
				return ag, true
			}
		}
	}

	return config.AgentConfig{}, false
}

// GenerateAttachJobID creates a stable, deterministic job ID for a bare attached process.
func GenerateAttachJobID(engine string, pid int, startTime time.Time) string {
	if !startTime.IsZero() {
		return fmt.Sprintf("job-attach-%s-%d-%d", engine, pid, startTime.Unix())
	}
	return fmt.Sprintf("job-attach-%s-%d", engine, pid)
}

// commandContext, readProcFile, readProcLink, readProcDir, and openTailFile
// are the process-discovery and log-tail IO seams. Tests wrap them so a
// regression fails before any other PID's command line or log is read.
var (
	commandContext = exec.CommandContext
	readProcFile   = os.ReadFile
	readProcLink   = os.Readlink
	readProcDir    = os.ReadDir
	openTailFile   = os.Open
)

// psListArgs builds the ps invocation. An empty PID list is the production
// full-table scan. A non-empty list must stay a targeted `ps -p` and must
// not include -A.
func psListArgs(pids []int) []string {
	if len(pids) == 0 {
		return []string{"-A", "-o", "pid=,ppid=,args="}
	}
	return []string{"-p", joinPIDList(pids), "-o", "pid=,ppid=,args="}
}

func joinPIDList(pids []int) string {
	parts := make([]string, len(pids))
	for i, pid := range pids {
		parts[i] = strconv.Itoa(pid)
	}
	return strings.Join(parts, ",")
}

func normalizePIDs(pids []int) []int {
	seen := make(map[int]bool, len(pids))
	out := make([]int, 0, len(pids))
	for _, pid := range pids {
		if pid <= 0 || seen[pid] {
			continue
		}
		seen[pid] = true
		out = append(out, pid)
	}
	sort.Ints(out)
	return out
}

// DefaultProcessLister lists processes using standard POSIX ps.
// There is no Windows native backend. Do not route this through PowerShell
// or a machine-wide process walk. On Windows the call stays blocked.
func DefaultProcessLister(ctx context.Context) ([]ProcessInfo, error) {
	return listWithPS(ctx, nil)
}

func listWithPS(ctx context.Context, pids []int) ([]ProcessInfo, error) {
	pids = normalizePIDs(pids)
	cmd := commandContext(ctx, "ps", psListArgs(pids)...)
	out, err := cmd.Output()
	if err != nil {
		if len(pids) > 0 && len(bytes.TrimSpace(out)) == 0 {
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				return nil, nil
			}
		}
		return nil, fmt.Errorf("failed to run ps: %w", err)
	}

	var procs []ProcessInfo
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}

		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		ppid, err := strconv.Atoi(fields[1])
		if err != nil {
			continue
		}

		// Find the start of the args section
		idx := strings.Index(line, fields[1])
		if idx < 0 {
			continue
		}
		cmdLine := strings.TrimSpace(line[idx+len(fields[1]):])
		args := TokenizeCommandLine(cmdLine)
		if len(args) == 0 {
			args = fields[2:]
		}

		procs = append(procs, ProcessInfo{
			PID:  pid,
			PPID: ppid,
			Args: args,
		})
	}
	return procs, nil
}

// listLinuxPIDs reads only the allowlisted PIDs. It does not walk /proc and
// does not run ps, so other processes' command lines are never opened.
func listLinuxPIDs(pids []int) ([]ProcessInfo, error) {
	btime, clk, haveBoot := linuxBootTime()
	var procs []ProcessInfo
	for _, pid := range pids {
		proc, ok := readLinuxProcess(pid, btime, clk, haveBoot)
		if ok {
			procs = append(procs, proc)
		}
	}
	return procs, nil
}

func readLinuxProcess(pid int, btime, clk int64, haveBoot bool) (ProcessInfo, bool) {
	raw, err := readProcFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		return ProcessInfo{}, false
	}
	args := splitCmdline(raw)
	if len(args) == 0 {
		return ProcessInfo{}, false
	}
	statRaw, err := readProcFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return ProcessInfo{}, false
	}
	ppid, startTicks, ok := parseProcStat(string(statRaw))
	if !ok {
		return ProcessInfo{}, false
	}
	info := ProcessInfo{PID: pid, PPID: ppid, Args: args}
	if target, err := readProcLink(fmt.Sprintf("/proc/%d/cwd", pid)); err == nil && target != "" {
		info.Cwd = target
	}
	if haveBoot && clk > 0 && startTicks >= 0 {
		info.StartTime = time.Unix(btime+startTicks/clk, 0).UTC()
	}
	return info, true
}

func splitCmdline(raw []byte) []string {
	if len(raw) == 0 {
		return nil
	}
	parts := bytes.Split(raw, []byte{0})
	args := make([]string, 0, len(parts))
	for _, part := range parts {
		if len(part) == 0 {
			continue
		}
		args = append(args, string(part))
	}
	return args
}

// parseProcStat returns ppid and starttime ticks from /proc/pid/stat.
// The comm field may contain spaces and parentheses, so parsing starts
// after the last ')'.
func parseProcStat(stat string) (ppid int, startTicks int64, ok bool) {
	end := strings.LastIndex(stat, ")")
	if end < 0 || end+2 >= len(stat) {
		return 0, 0, false
	}
	fields := strings.Fields(stat[end+1:])
	// state, ppid, ..., starttime is field 20 (index 19) after comm.
	if len(fields) < 20 {
		return 0, 0, false
	}
	ppid, err := strconv.Atoi(fields[1])
	if err != nil {
		return 0, 0, false
	}
	startTicks, err = strconv.ParseInt(fields[19], 10, 64)
	if err != nil {
		return 0, 0, false
	}
	return ppid, startTicks, true
}

func linuxBootTime() (btime int64, clk int64, ok bool) {
	raw, err := readProcFile("/proc/stat")
	if err != nil {
		return 0, 100, false
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "btime ") {
			btime, err = strconv.ParseInt(strings.TrimSpace(strings.TrimPrefix(line, "btime ")), 10, 64)
			if err != nil || btime <= 0 {
				return 0, 100, false
			}
			// Linux CLK_TCK is 100 on the architectures this program runs.
			return btime, 100, true
		}
	}
	return 0, 100, false
}

// openPathsOfPID returns files this PID itself has open.
// It never inspects another PID.
func openPathsOfPID(pid int) []string {
	if pid <= 0 {
		return nil
	}
	if runtime.GOOS == "linux" {
		return linuxFDPaths(pid)
	}
	if runtime.GOOS == "darwin" {
		return darwinLsofPaths(pid)
	}
	return nil
}

func linuxFDPaths(pid int) []string {
	dir := fmt.Sprintf("/proc/%d/fd", pid)
	entries, err := readProcDir(dir)
	if err != nil {
		return nil
	}
	seen := make(map[string]bool)
	var paths []string
	for _, entry := range entries {
		target, err := readProcLink(filepath.Join(dir, entry.Name()))
		if err != nil || target == "" {
			continue
		}
		target = strings.TrimSuffix(target, " (deleted)")
		if !filepath.IsAbs(target) || seen[target] {
			continue
		}
		seen[target] = true
		paths = append(paths, target)
	}
	return paths
}

func darwinLsofPaths(pid int) []string {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	cmd := commandContext(ctx, "lsof", "-a", "-p", strconv.Itoa(pid), "-Fn")
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	var paths []string
	seen := make(map[string]bool)
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.HasPrefix(line, "n") {
			continue
		}
		path := strings.TrimPrefix(line, "n")
		if !filepath.IsAbs(path) || seen[path] {
			continue
		}
		seen[path] = true
		paths = append(paths, path)
	}
	return paths
}

// fileNameHasPID reports whether name contains pid as a whole numeric token.
func fileNameHasPID(name string, pid int) bool {
	token := strconv.Itoa(pid)
	if token == "" {
		return false
	}
	rest := name
	for {
		i := strings.Index(rest, token)
		if i < 0 {
			return false
		}
		beforeOK := i == 0 || rest[i-1] < '0' || rest[i-1] > '9'
		after := i + len(token)
		afterOK := after == len(rest) || rest[after] < '0' || rest[after] > '9'
		if beforeOK && afterOK {
			return true
		}
		rest = rest[i+1:]
	}
}

func pathUnderDir(path, dir string) bool {
	if path == "" || dir == "" {
		return false
	}
	rel, err := filepath.Rel(dir, path)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return false
	}
	return true
}

// ownedLogFile selects a log for pid without opening any other PID's log.
// Directory entries are names only. Content is opened later, and only for the
// chosen path.
func ownedLogFile(pid int, logDir string, nameOK func(string) bool) string {
	if pid <= 0 || logDir == "" || nameOK == nil {
		return ""
	}
	if entries, err := readProcDir(logDir); err == nil {
		for _, entry := range entries {
			if entry.IsDir() || !nameOK(entry.Name()) || !fileNameHasPID(entry.Name(), pid) {
				continue
			}
			return filepath.Join(logDir, entry.Name())
		}
	}
	for _, path := range openPathsOfPID(pid) {
		if !pathUnderDir(path, logDir) {
			continue
		}
		base := filepath.Base(path)
		if nameOK(base) || strings.HasSuffix(base, ".log") {
			return path
		}
	}
	return ""
}

func waitOwnedLog(ctx context.Context, pid int, logDir string, nameOK func(string) bool) string {
	for attempt := 0; attempt < 10; attempt++ {
		select {
		case <-ctx.Done():
			return ""
		default:
		}
		if path := ownedLogFile(pid, logDir, nameOK); path != "" {
			return path
		}
		timer := time.NewTimer(500 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ""
		case <-timer.C:
		}
	}
	return ""
}

// ResolveProcessCwd probes the current working directory of a process.
func ResolveProcessCwd(pid int) string {
	if pid <= 0 {
		return ""
	}

	if runtime.GOOS == "linux" {
		if target, err := readProcLink(fmt.Sprintf("/proc/%d/cwd", pid)); err == nil {
			return target
		}
	}

	if runtime.GOOS == "darwin" {
		ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
		defer cancel()
		cmd := commandContext(ctx, "lsof", "-a", "-p", strconv.Itoa(pid), "-d", "cwd", "-Fn")
		out, err := cmd.Output()
		if err == nil {
			lines := strings.Split(string(out), "\n")
			for _, l := range lines {
				if strings.HasPrefix(l, "n") {
					path := strings.TrimPrefix(l, "n")
					if path != "" && path != "/" {
						return path
					}
				}
			}
		}
	}

	return ""
}

// ResolveProcessStartTime probes the process start time.
func ResolveProcessStartTime(pid int) time.Time {
	if pid <= 0 {
		return time.Now().UTC()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	cmd := commandContext(ctx, "ps", "-p", strconv.Itoa(pid), "-o", "lstart=")
	out, err := cmd.Output()
	if err == nil {
		str := strings.TrimSpace(string(out))
		layouts := []string{
			"Mon Jan _2 15:04:05 2006",
			"Mon Jan 2 15:04:05 2006",
			time.ANSIC,
		}
		for _, layout := range layouts {
			if t, err := time.Parse(layout, str); err == nil {
				return t.UTC()
			}
		}
	}

	return time.Now().UTC()
}

// IsProcessAlive reports whether a process with the given PID is currently running.
func IsProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = process.Signal(syscall.Signal(0))
	if err == nil {
		return true
	}
	if errors.Is(err, syscall.EPERM) {
		return true
	}
	return false
}

// AttachManagerConfig configures the bare process attach manager.
type AttachManagerConfig struct {
	MachineID              string
	MQTTClient             paho.Client
	Agents                 []config.AgentConfig
	ScanInterval           time.Duration
	ProcessLister          ProcessLister
	IsProcessAlive         func(pid int) bool
	ProcessExitChecker     func(pid int) (exitCode int, ok bool)
	ReliableSuccessChecker func(pid int) bool
	PIDAllowlist           []int
	PIDFilter              func(pid int) bool
	AgyLogDir              string
	AgyBrainDir            string
	OpenCodeLogDir         string
	StatusFile             string
	Quiet                  bool
	Stdout                 io.Writer
	Stderr                 io.Writer
	IsDispatchPID          func(int) bool
	OnJobCountChange       func()
	StartTimeResolver      func(pid int) time.Time
	CwdResolver            func(pid int) string
}

// AttachedJob tracks a single discovered and attached bare agent process.
type AttachedJob struct {
	mu        sync.Mutex
	pid       int
	startTime time.Time
	job       model.Job
	engine    string
	cancel    context.CancelFunc
	doneCh    chan struct{}
	seq       int64
}

// AttachManager periodically scans local processes, attaches to bare agent CLI processes
// (e.g. agy, opencode), and streams their status and output to MQTT.
type AttachManager struct {
	cfg               AttachManagerConfig
	mu                sync.Mutex
	mqttClient        paho.Client
	activeJobs        map[int]*AttachedJob
	knownPIDs         map[int]time.Time
	pidAllowlist      map[int]bool
	usesDefaultLister bool
	stopCh            chan struct{}
	doneCh            chan struct{}
	started           atomic.Bool
}

// NewAttachManager creates and initializes an AttachManager.
func NewAttachManager(cfg AttachManagerConfig) *AttachManager {
	if cfg.ScanInterval <= 0 {
		cfg.ScanInterval = 2 * time.Second
	}
	usesDefaultLister := false
	if cfg.ProcessLister == nil {
		cfg.ProcessLister = DefaultProcessLister
		usesDefaultLister = true
	}
	if cfg.IsProcessAlive == nil {
		cfg.IsProcessAlive = IsProcessAlive
	}
	if cfg.Agents == nil {
		cfg.Agents = config.DefaultAgents()
	}

	allowlistMap := make(map[int]bool)
	for _, pid := range cfg.PIDAllowlist {
		if pid > 0 {
			allowlistMap[pid] = true
		}
	}
	if len(allowlistMap) == 0 {
		if envPIDs := os.Getenv("AGENTPULSE_ATTACH_PID_ALLOWLIST"); envPIDs != "" {
			for _, pid := range ParsePIDAllowlist(envPIDs) {
				if pid > 0 {
					allowlistMap[pid] = true
				}
			}
		}
	}

	if cfg.AgyLogDir == "" {
		if envAgy := os.Getenv("AGENTPULSE_TEST_AGY_LOG_DIR"); envAgy != "" {
			cfg.AgyLogDir = envAgy
		}
	}
	if cfg.AgyBrainDir == "" {
		if envBrain := os.Getenv("AGENTPULSE_TEST_AGY_BRAIN_DIR"); envBrain != "" {
			cfg.AgyBrainDir = envBrain
		}
	}
	if cfg.OpenCodeLogDir == "" {
		if envOpenCode := os.Getenv("AGENTPULSE_TEST_OPENCODE_LOG_DIR"); envOpenCode != "" {
			cfg.OpenCodeLogDir = envOpenCode
		}
	}

	return &AttachManager{
		cfg:               cfg,
		mqttClient:        cfg.MQTTClient,
		activeJobs:        make(map[int]*AttachedJob),
		knownPIDs:         make(map[int]time.Time),
		pidAllowlist:      allowlistMap,
		usesDefaultLister: usesDefaultLister,
		stopCh:            make(chan struct{}),
		doneCh:            make(chan struct{}),
	}
}

// SetProcessExitChecker dynamically updates the exit code checker.
func (m *AttachManager) SetProcessExitChecker(checker func(pid int) (int, bool)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cfg.ProcessExitChecker = checker
}

// SetPIDAllowlist dynamically updates the PID allowlist filter.
func (m *AttachManager) SetPIDAllowlist(pids []int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cfg.PIDAllowlist = pids
	m.pidAllowlist = make(map[int]bool, len(pids))
	for _, pid := range pids {
		if pid > 0 {
			m.pidAllowlist[pid] = true
		}
	}
}

// SetMQTTClient updates the MQTT client used for publishing telemetry.
func (m *AttachManager) SetMQTTClient(client paho.Client) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.mqttClient = client
}

// ActiveCount returns the number of currently attached running jobs.
func (m *AttachManager) ActiveCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.activeJobs)
}

// Start launches the background process scanning loop.
func (m *AttachManager) Start(ctx context.Context) {
	if m.started.Swap(true) {
		return
	}
	go m.run(ctx)
}

// Stop cleanly terminates all attached jobs and stops the scanning loop.
func (m *AttachManager) Stop() {
	if !m.started.Load() {
		return
	}
	select {
	case <-m.stopCh:
		return
	default:
		close(m.stopCh)
	}

	m.mu.Lock()
	for _, aj := range m.activeJobs {
		aj.cancel()
	}
	m.mu.Unlock()

	select {
	case <-m.doneCh:
	case <-time.After(2 * time.Second):
	}
}

func (m *AttachManager) run(ctx context.Context) {
	defer close(m.doneCh)

	// Perform initial scan immediately
	m.scan(ctx)

	ticker := time.NewTicker(m.cfg.ScanInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-m.stopCh:
			return
		case <-ticker.C:
			m.scan(ctx)
		}
	}
}

// discoverProcesses lists candidate processes.
// A non-empty allowlist on the default lister is applied before any OS read:
// Linux opens only those PIDs' proc files, and other systems run ps -p for
// those PIDs only. An empty allowlist keeps the existing full-table ps.
func (m *AttachManager) discoverProcesses(ctx context.Context) ([]ProcessInfo, error) {
	m.mu.Lock()
	pids := make([]int, 0, len(m.pidAllowlist))
	for pid := range m.pidAllowlist {
		pids = append(pids, pid)
	}
	useDefault := m.usesDefaultLister
	lister := m.cfg.ProcessLister
	m.mu.Unlock()

	pids = normalizePIDs(pids)
	if useDefault && len(pids) > 0 {
		if runtime.GOOS == "linux" {
			return listLinuxPIDs(pids)
		}
		return listWithPS(ctx, pids)
	}
	if lister == nil {
		return nil, nil
	}
	return lister(ctx)
}

func (m *AttachManager) scan(ctx context.Context) {
	procs, err := m.discoverProcesses(ctx)
	if err != nil {
		if !m.cfg.Quiet && m.cfg.Stderr != nil {
			fmt.Fprintf(m.cfg.Stderr, "[agentpulse attach] process scan error: %v\n", err)
		}
		return
	}

	selfPID := os.Getpid()

	m.mu.Lock()
	now := time.Now()
	for pid, t := range m.knownPIDs {
		if now.Sub(t) > 2*time.Hour {
			delete(m.knownPIDs, pid)
		}
	}
	allowlist := make(map[int]bool, len(m.pidAllowlist))
	for k, v := range m.pidAllowlist {
		allowlist[k] = v
	}
	pidFilter := m.cfg.PIDFilter
	m.mu.Unlock()

	for _, p := range procs {
		if len(allowlist) > 0 && !allowlist[p.PID] {
			continue
		}
		if pidFilter != nil && !pidFilter(p.PID) {
			continue
		}

		matchedAg, ok := MatchAgentProcess(p, m.cfg.Agents, m.cfg.IsDispatchPID, selfPID)
		if !ok {
			continue
		}

		m.mu.Lock()
		_, active := m.activeJobs[p.PID]
		m.mu.Unlock()
		if active {
			continue
		}

		if !m.cfg.IsProcessAlive(p.PID) {
			continue
		}

		// Found a new bare agent CLI process
		m.attachProcess(ctx, p, matchedAg)
	}
}

func (m *AttachManager) attachProcess(ctx context.Context, p ProcessInfo, ag config.AgentConfig) {
	startTime := p.StartTime
	if startTime.IsZero() {
		if m.cfg.StartTimeResolver != nil {
			startTime = m.cfg.StartTimeResolver(p.PID)
		} else {
			startTime = ResolveProcessStartTime(p.PID)
		}
	}

	cwd := p.Cwd
	if cwd == "" {
		if m.cfg.CwdResolver != nil {
			cwd = m.cfg.CwdResolver(p.PID)
		} else {
			cwd = ResolveProcessCwd(p.PID)
		}
	}

	cmdStr := strings.Join(p.Args, " ")
	title := InferTitle(p.Args, cmdStr)
	if title == "" || title == "Agent pulse job" {
		title = fmt.Sprintf("Bare %s (PID %d)", ag.ID, p.PID)
	}

	jobID := GenerateAttachJobID(ag.ID, p.PID, startTime)
	now := time.Now().UTC()

	job := model.Job{
		ID:        jobID,
		MachineID: m.cfg.MachineID,
		Engine:    model.EngineType(ag.ID),
		Status:    model.StatusRunning,
		Progress:  0.0,
		Cwd:       cwd,
		Command:   cmdStr,
		Title:     title,
		StartedAt: startTime,
		UpdatedAt: now,
	}

	jobCtx, jobCancel := context.WithCancel(ctx)
	aj := &AttachedJob{
		pid:       p.PID,
		startTime: startTime,
		job:       job,
		engine:    ag.ID,
		cancel:    jobCancel,
		doneCh:    make(chan struct{}),
	}

	m.mu.Lock()
	m.activeJobs[p.PID] = aj
	m.knownPIDs[p.PID] = time.Now()
	m.mu.Unlock()

	if m.cfg.OnJobCountChange != nil {
		m.cfg.OnJobCountChange()
	}

	if !m.cfg.Quiet && m.cfg.Stdout != nil {
		fmt.Fprintf(m.cfg.Stdout, "[agentpulse attach] Attached bare %s process %d (%s)\n", ag.ID, p.PID, title)
	}

	go m.monitorAttachedJob(jobCtx, aj)
}

func (m *AttachManager) monitorAttachedJob(ctx context.Context, aj *AttachedJob) {
	defer close(aj.doneCh)
	defer func() {
		m.mu.Lock()
		delete(m.activeJobs, aj.pid)
		m.mu.Unlock()

		if m.cfg.OnJobCountChange != nil {
			m.cfg.OnJobCountChange()
		}
	}()

	// 1. Publish initial status and attachment log
	m.publishJobStatus(aj)
	m.publishLog(aj, model.LevelInfo, fmt.Sprintf("Attached to local bare %s process (PID %d): %s", aj.engine, aj.pid, aj.job.Title))

	// 2. Start log tailer in background
	tailCtx, tailCancel := context.WithCancel(ctx)
	defer tailCancel()
	go m.tailProcessLogs(tailCtx, aj)

	// 3. Monitor process liveness
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			m.finalizeJob(aj, true)
			return
		case <-ticker.C:
			if !m.cfg.IsProcessAlive(aj.pid) {
				// Allow brief window for log tailer to flush trailing output
				time.Sleep(100 * time.Millisecond)
				m.finalizeJob(aj, false)
				return
			}
			// Periodic refresh of job update timestamp
			aj.mu.Lock()
			aj.job.UpdatedAt = time.Now().UTC()
			aj.mu.Unlock()
			m.publishJobStatus(aj)
		}
	}
}

func (m *AttachManager) finalizeJob(aj *AttachedJob, interrupted bool) {
	aj.mu.Lock()
	finishTime := time.Now().UTC()
	aj.job.UpdatedAt = finishTime
	aj.job.DurationStr = finishTime.Sub(aj.job.StartedAt).Round(time.Second).String()

	m.mu.Lock()
	exitChecker := m.cfg.ProcessExitChecker
	successChecker := m.cfg.ReliableSuccessChecker
	m.mu.Unlock()

	var exitCode int
	var exitCodeKnown bool
	if !interrupted && exitChecker != nil {
		exitCode, exitCodeKnown = exitChecker(aj.pid)
	}

	hasReliableSuccess := false
	if exitCodeKnown && exitCode == 0 {
		hasReliableSuccess = true
	} else if !interrupted && successChecker != nil && successChecker(aj.pid) {
		hasReliableSuccess = true
	}

	if interrupted {
		aj.job.Status = model.StatusFailed
		if aj.job.Error == "" {
			aj.job.Error = "进程监控被中断或停止"
		}
	} else if aj.job.Error != "" {
		// Log parser or runtime already recorded an error
		aj.job.Status = model.StatusFailed
		if exitCodeKnown && exitCode != 0 && !strings.Contains(aj.job.Error, fmt.Sprintf("退出码 %d", exitCode)) {
			aj.job.Error = fmt.Sprintf("进程非正常退出 (退出码 %d): %s", exitCode, aj.job.Error)
		}
	} else if exitCodeKnown {
		if exitCode == 0 {
			aj.job.Status = model.StatusDone
			aj.job.Progress = 100.0
		} else {
			aj.job.Status = model.StatusFailed
			aj.job.Error = fmt.Sprintf("进程非正常退出 (退出码 %d)", exitCode)
		}
	} else if hasReliableSuccess {
		aj.job.Status = model.StatusDone
		aj.job.Progress = 100.0
	} else {
		// Exit code cannot be determined and no reliable success evidence
		aj.job.Status = model.StatusFailed
		aj.job.Error = "无法判定退出码（依据不足，非正常可观测退出）"
	}
	finalJob := aj.job
	aj.mu.Unlock()

	m.publishJobStatus(aj)
	m.publishAlert(finalJob)
	m.publishLog(aj, model.LevelInfo, fmt.Sprintf("Bare %s process (PID %d) finished: status %s in %s", aj.engine, aj.pid, finalJob.Status, finalJob.DurationStr))

	if !m.cfg.Quiet && m.cfg.Stdout != nil {
		fmt.Fprintf(m.cfg.Stdout, "[agentpulse attach] Bare %s process %d finished: %s (%s)\n", aj.engine, aj.pid, finalJob.Status, finalJob.DurationStr)
	}
}

func (m *AttachManager) publishJobStatus(aj *AttachedJob) {
	m.mu.Lock()
	client := m.mqttClient
	m.mu.Unlock()

	if client == nil || !client.IsConnected() {
		return
	}

	aj.mu.Lock()
	j := aj.job
	aj.mu.Unlock()

	topic := mqtt.JobStatusTopic(j.MachineID, j.ID)
	data, err := json.Marshal(j)
	if err != nil {
		return
	}
	token := client.Publish(topic, 1, true, data)
	token.WaitTimeout(2 * time.Second)
}

func (m *AttachManager) publishLog(aj *AttachedJob, level model.LogLevel, message string) {
	m.mu.Lock()
	client := m.mqttClient
	m.mu.Unlock()

	if client == nil || !client.IsConnected() {
		return
	}

	seq := atomic.AddInt64(&aj.seq, 1)

	aj.mu.Lock()
	machID := aj.job.MachineID
	jobID := aj.job.ID
	engine := aj.job.Engine
	aj.mu.Unlock()

	entry := struct {
		Timestamp time.Time        `json:"timestamp"`
		JobID     string           `json:"job_id"`
		MachineID string           `json:"machine_id"`
		Engine    model.EngineType `json:"engine"`
		Level     model.LogLevel   `json:"level"`
		Message   string           `json:"message"`
		Seq       int64            `json:"seq"`
	}{
		Timestamp: time.Now().UTC(),
		JobID:     jobID,
		MachineID: machID,
		Engine:    engine,
		Level:     level,
		Message:   TruncateLogMessage(message),
		Seq:       seq,
	}

	topic := mqtt.JobLogsTopic(machID, jobID)
	data, err := json.Marshal(entry)
	if err != nil {
		return
	}
	client.Publish(topic, 0, false, data)
}

func (m *AttachManager) publishAlert(j model.Job) {
	m.mu.Lock()
	client := m.mqttClient
	machID := m.cfg.MachineID
	m.mu.Unlock()

	if client == nil || !client.IsConnected() {
		return
	}

	alert := map[string]interface{}{
		"job_id":       j.ID,
		"machine_id":   machID,
		"engine":       string(j.Engine),
		"status":       string(j.Status),
		"title":        j.Title,
		"cwd":          j.Cwd,
		"duration_str": j.DurationStr,
		"started_at":   j.StartedAt.Format(time.RFC3339),
		"finished_at":  j.UpdatedAt.Format(time.RFC3339),
		"error":        j.Error,
	}

	data, err := json.Marshal(alert)
	if err != nil {
		return
	}
	token := client.Publish("agentpulse/v1/notifications/alerts", 1, false, data)
	token.WaitTimeout(2 * time.Second)
}

func (m *AttachManager) handleOutputLine(aj *AttachedJob, line string, isStderr bool) {
	clean := strings.TrimSpace(line)
	if clean == "" {
		return
	}

	level := ClassifyLogLevel(clean, isStderr)

	if pct, ok := ParseProgress(clean); ok {
		aj.mu.Lock()
		changed := aj.job.Progress != pct
		aj.job.Progress = pct
		aj.job.UpdatedAt = time.Now().UTC()
		aj.mu.Unlock()
		if changed {
			m.publishJobStatus(aj)
		}
	}

	if level == model.LevelError {
		aj.mu.Lock()
		if aj.job.Error == "" {
			aj.job.Error = TruncateLogMessage(clean)
		}
		aj.mu.Unlock()
	}

	m.publishLog(aj, level, clean)
}

// tailProcessLogs attempts to locate and tail output logs for the attached agent process.
func (m *AttachManager) tailProcessLogs(ctx context.Context, aj *AttachedJob) {
	home, _ := os.UserHomeDir()
	if home == "" {
		home = "."
	}

	switch strings.ToLower(aj.engine) {
	case "agy":
		m.tailAgyLogs(ctx, aj, home)
	case "opencode":
		m.tailOpenCodeLogs(ctx, aj, home)
	}
}

func (m *AttachManager) tailAgyLogs(ctx context.Context, aj *AttachedJob, home string) {
	logDir := m.cfg.AgyLogDir
	if logDir == "" {
		logDir = filepath.Join(home, ".gemini", "antigravity-cli", "log")
	}

	// A non-empty allowlist on the default lister must not open sibling logs
	// to see which PID they mention. Only this PID's own fd or a filename
	// that already contains the PID is eligible.
	var logFile string
	if m.logsRestricted() {
		logFile = waitOwnedLog(ctx, aj.pid, logDir, func(name string) bool {
			return strings.HasPrefix(name, "cli-") && strings.HasSuffix(name, ".log")
		})
	} else {
		logFile = m.findAgyLogByContent(ctx, logDir, aj.pid)
	}

	if logFile == "" {
		return
	}

	var convID string
	convRegex := regexp.MustCompile(`Created conversation ([a-zA-Z0-9-]+)`)

	brainDir := m.cfg.AgyBrainDir
	if brainDir == "" && m.cfg.AgyLogDir != "" {
		candidate := filepath.Join(filepath.Dir(m.cfg.AgyLogDir), "brain")
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			brainDir = candidate
		}
	}
	if brainDir == "" {
		brainDir = filepath.Join(home, ".gemini", "antigravity-cli", "brain")
	}

	tailFile(ctx, logFile, func(line string) {
		m.handleOutputLine(aj, line, false)
		if convID == "" {
			matches := convRegex.FindStringSubmatch(line)
			if len(matches) > 1 {
				convID = matches[1]
				brainLog := filepath.Join(brainDir, convID, ".system_generated", "logs", "transcript.jsonl")
				go m.tailTranscript(ctx, aj, brainLog)
			}
		}
	})
}

func fileContainsPID(path, pidPattern string) bool {
	f, err := openTailFile(path)
	if err != nil {
		return false
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	// Check first 10 lines
	for i := 0; i < 10 && scanner.Scan(); i++ {
		if strings.Contains(scanner.Text(), pidPattern) {
			return true
		}
	}
	return false
}

func (m *AttachManager) tailTranscript(ctx context.Context, aj *AttachedJob, path string) {
	// Wait for transcript file to appear
	for i := 0; i < 20; i++ {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if _, err := os.Stat(path); err == nil {
			break
		}
		time.Sleep(300 * time.Millisecond)
	}

	type transcriptStep struct {
		Source    string          `json:"source"`
		Type      string          `json:"type"`
		Content   string          `json:"content"`
		ToolCalls json.RawMessage `json:"tool_calls"`
	}

	tailFile(ctx, path, func(line string) {
		var step transcriptStep
		if err := json.Unmarshal([]byte(line), &step); err == nil {
			if step.Type == "PLANNER_RESPONSE" && strings.TrimSpace(step.Content) != "" {
				m.handleOutputLine(aj, step.Content, false)
			} else if len(step.ToolCalls) > 0 {
				m.handleOutputLine(aj, fmt.Sprintf("[tool_calls] %s", string(step.ToolCalls)), false)
			}
		}
	})
}

func (m *AttachManager) tailOpenCodeLogs(ctx context.Context, aj *AttachedJob, home string) {
	logDir := m.cfg.OpenCodeLogDir
	if logDir == "" {
		logDir = filepath.Join(home, ".local", "share", "opencode", "log")
	}

	var logFile string
	if m.logsRestricted() {
		logFile = waitOwnedLog(ctx, aj.pid, logDir, func(name string) bool {
			return strings.HasSuffix(name, ".log")
		})
	} else {
		logFile = m.findNewestOpenCodeLog(ctx, logDir)
	}

	if logFile == "" {
		return
	}

	tailFile(ctx, logFile, func(line string) {
		m.handleOutputLine(aj, line, false)
	})
}

func (m *AttachManager) logsRestricted() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.usesDefaultLister && len(m.pidAllowlist) > 0
}

func (m *AttachManager) findAgyLogByContent(ctx context.Context, logDir string, pid int) string {
	pidPattern := fmt.Sprintf("pid %d", pid)
	for attempt := 0; attempt < 10; attempt++ {
		select {
		case <-ctx.Done():
			return ""
		default:
		}

		entries, err := os.ReadDir(logDir)
		if err == nil {
			type fileInfo struct {
				name    string
				modTime time.Time
			}
			var files []fileInfo
			for _, e := range entries {
				if strings.HasPrefix(e.Name(), "cli-") && strings.HasSuffix(e.Name(), ".log") {
					if info, err := e.Info(); err == nil {
						files = append(files, fileInfo{name: e.Name(), modTime: info.ModTime()})
					}
				}
			}
			sort.Slice(files, func(i, j int) bool {
				return files[i].modTime.After(files[j].modTime)
			})

			// Inspect the newest 25 files. Unrestricted production scans keep
			// this content match; allowlisted discovery does not.
			limit := 25
			if len(files) < limit {
				limit = len(files)
			}
			for i := 0; i < limit; i++ {
				fp := filepath.Join(logDir, files[i].name)
				if fileContainsPID(fp, pidPattern) {
					return fp
				}
			}
		}

		timer := time.NewTimer(500 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ""
		case <-timer.C:
		}
	}
	return ""
}

func (m *AttachManager) findNewestOpenCodeLog(ctx context.Context, logDir string) string {
	for attempt := 0; attempt < 5; attempt++ {
		select {
		case <-ctx.Done():
			return ""
		default:
		}

		entries, err := os.ReadDir(logDir)
		if err == nil && len(entries) > 0 {
			type fileInfo struct {
				name    string
				modTime time.Time
			}
			var files []fileInfo
			for _, e := range entries {
				if strings.HasSuffix(e.Name(), ".log") {
					if info, err := e.Info(); err == nil {
						files = append(files, fileInfo{name: e.Name(), modTime: info.ModTime()})
					}
				}
			}
			sort.Slice(files, func(i, j int) bool {
				return files[i].modTime.After(files[j].modTime)
			})
			if len(files) > 0 {
				return filepath.Join(logDir, files[0].name)
			}
		}
		timer := time.NewTimer(500 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ""
		case <-timer.C:
		}
	}
	return ""
}

func tailFile(ctx context.Context, path string, onLine func(line string)) {
	f, err := openTailFile(path)
	if err != nil {
		return
	}
	defer f.Close()

	reader := bufio.NewReader(f)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		line, err := reader.ReadString('\n')
		if err == nil {
			clean := strings.TrimRight(line, "\r\n")
			if clean != "" {
				onLine(clean)
			}
			continue
		}

		if errors.Is(err, io.EOF) {
			if len(line) > 0 {
				clean := strings.TrimRight(line, "\r\n")
				if clean != "" {
					onLine(clean)
				}
			}
			time.Sleep(250 * time.Millisecond)
			continue
		}

		return
	}
}
