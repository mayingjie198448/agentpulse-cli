package config

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config represents the AgentPulse v1 configuration stored in ~/.config/agentpulse/config.yaml.
type Config struct {
	Version   int    `yaml:"version"`
	MachineID string `yaml:"machine_id"`
	Rev       int    `yaml:"rev,omitempty"`
	// MaxConcurrentJobs specifies the maximum number of AI agent jobs that can execute
	// concurrently on this machine (default: 1 for safe serial execution).
	//
	// Working Directory (cwd / worktree) & Resource Isolation Warning:
	// When MaxConcurrentJobs > 1, multiple agent processes may run simultaneously.
	// If multiple concurrent jobs are dispatched to the same working directory (cwd),
	// they may collide on git index locks, conflicting file edits, or test artifacts.
	// Users and callers should ensure each concurrent job runs in an isolated workspace
	// (e.g. dedicated git worktree) and that the host has sufficient CPU, RAM, and model API quota.
	MaxConcurrentJobs int           `yaml:"max_concurrent_jobs,omitempty"`
	Broker            BrokerConfig  `yaml:"broker"`
	Agents            []AgentConfig `yaml:"agents"`
}

// BrokerConfig holds MQTT connection and authentication details.
type BrokerConfig struct {
	URL                string `yaml:"url"`
	Username           string `yaml:"username,omitempty"`
	Password           string `yaml:"password,omitempty"`
	TLS                bool   `yaml:"tls,omitempty"`
	CACertFile         string `yaml:"ca_cert,omitempty"`
	ClientCertFile     string `yaml:"client_cert,omitempty"`
	ClientKeyFile      string `yaml:"client_key,omitempty"`
	InsecureSkipVerify bool   `yaml:"insecure_skip_verify,omitempty"`
	ServerName         string `yaml:"server_name,omitempty"`
}

// AgentConfig represents an AI agent CLI registered on the local machine.
type AgentConfig struct {
	ID      string   `yaml:"id"`
	Path    string   `yaml:"path,omitempty"`
	Alias   []string `yaml:"alias,omitempty"`
	Enabled *bool    `yaml:"enabled,omitempty"`
}

// IsEnabled returns true if the agent is enabled (defaults to true if unspecified).
func (a AgentConfig) IsEnabled() bool {
	if a.Enabled == nil {
		return true
	}
	return *a.Enabled
}

// AgentStatus represents the probed runtime status of a registered agent.
type AgentStatus struct {
	ID           string   `json:"id"`
	Alias        []string `json:"alias"`
	Path         string   `json:"path,omitempty"`
	ResolvedPath string   `json:"resolved_path,omitempty"`
	Ok           bool     `json:"ok"`
	Detail       string   `json:"detail"`
}

// DefaultAgents returns the standard built-in agent CLI presets.
func DefaultAgents() []AgentConfig {
	return []AgentConfig{
		{
			ID:    "agy",
			Path:  "",
			Alias: []string{"agy"},
		},
		{
			ID:    "opencode",
			Path:  "",
			Alias: []string{"op", "opencode"},
		},
		{
			ID:    "codex",
			Path:  "",
			Alias: []string{"codex"},
		},
	}
}

// DefaultConfigPath returns ~/.config/agentpulse/config.yaml on Unix/macOS,
// or %AppData%\agentpulse\config.yaml on Windows.
func DefaultConfigPath() string {
	if runtime.GOOS == "windows" {
		if appData := os.Getenv("AppData"); appData != "" {
			return filepath.Join(appData, "agentpulse", "config.yaml")
		}
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			return filepath.Join(home, "AppData", "Roaming", "agentpulse", "config.yaml")
		}
		return filepath.Join("agentpulse", "config.yaml")
	}

	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		home = "."
	}
	return filepath.Join(home, ".config", "agentpulse", "config.yaml")
}

// LoadConfig loads and parses the configuration from the specified path.
// If path is empty, DefaultConfigPath() is used.
func LoadConfig(path string) (*Config, error) {
	if path == "" {
		path = DefaultConfigPath()
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config file %q: %w", path, err)
	}

	if cfg.Version == 0 {
		cfg.Version = 1
	}
	if cfg.Rev == 0 {
		cfg.Rev = 1
	}
	if cfg.MaxConcurrentJobs <= 0 {
		cfg.MaxConcurrentJobs = 1
	}

	return &cfg, nil
}

// SaveConfig writes the configuration to disk with strict permissions:
// directory mode 0700 and file mode 0600.
func SaveConfig(path string, cfg *Config) error {
	if path == "" {
		path = DefaultConfigPath()
	}

	if cfg.Version == 0 {
		cfg.Version = 1
	}
	if cfg.Rev == 0 {
		cfg.Rev = 1
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("failed to create config directory %q: %w", dir, err)
	}

	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	// Write with 0600 permissions
	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("failed to write config file %q: %w", path, err)
	}

	// Ensure permissions are strictly 0600 in case the file already existed with different mode
	_ = os.Chmod(path, 0600)
	_ = os.Chmod(dir, 0700)

	return nil
}

// IsAgyAgent determines whether the given agent config or resolved binary represents agy.
func IsAgyAgent(ag AgentConfig, resolvedPath string) bool {
	if strings.EqualFold(ag.ID, "agy") {
		return true
	}
	for _, alias := range ag.Alias {
		if strings.EqualFold(alias, "agy") {
			return true
		}
	}
	if resolvedPath != "" {
		base := filepath.Base(resolvedPath)
		baseNoExt := strings.TrimSuffix(strings.ToLower(base), ".exe")
		if baseNoExt == "agy" {
			return true
		}
	}
	return false
}

// ProbeAgent probes whether the given agent CLI executable is available.
func ProbeAgent(ag AgentConfig) AgentStatus {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return ProbeAgentContext(ctx, ag)
}

// ProbeAgentContext probes whether the given agent CLI executable is available within the provided context.
func ProbeAgentContext(ctx context.Context, ag AgentConfig) AgentStatus {
	st := AgentStatus{
		ID:    ag.ID,
		Alias: ag.Alias,
		Path:  ag.Path,
	}

	if !ag.IsEnabled() {
		st.Ok = false
		st.Detail = "已禁用"
		return st
	}

	// 1. Resolve executable binary path
	if ag.Path != "" {
		resolved, err := exec.LookPath(ag.Path)
		if err != nil {
			st.Ok = false
			st.Detail = fmt.Sprintf("指定路径不可用: %v", err)
			return st
		}
		st.Ok = true
		st.ResolvedPath = resolved
		st.Detail = resolved
	} else {
		// If path is empty: search PATH by id, then by primary alias
		if p, err := exec.LookPath(ag.ID); err == nil {
			st.Ok = true
			st.ResolvedPath = p
			st.Detail = p
		} else {
			for _, alias := range ag.Alias {
				if alias != "" && alias != ag.ID {
					if p, err := exec.LookPath(alias); err == nil {
						st.Ok = true
						st.ResolvedPath = p
						st.Detail = p
						break
					}
				}
			}
		}
	}

	if !st.Ok {
		st.Detail = "未在 PATH 中找到可执行文件"
		return st
	}

	// 2. For agy (or agents requiring authentication), probe whether it is logged in and ready for execution.
	// Unified across PATH, alias, and explicit path configurations.
	if IsAgyAgent(ag, st.ResolvedPath) {
		authOk, detail := ProbeAgyAuthContext(ctx, st.ResolvedPath)
		if !authOk {
			st.Ok = false
			st.Detail = detail
		} else if detail != "" {
			st.Detail = fmt.Sprintf("%s (%s)", st.ResolvedPath, detail)
		}
	}

	return st
}

// Recognized model families and identifiers for agy model evidence.
var recognizedModelKeywords = []string{
	"gemini",
	"claude",
	"gpt",
	"sonnet",
	"opus",
	"flash",
	"deepseek",
	"llama",
	"mistral",
	"qwen",
	"codex",
	"command-r",
	"grok",
	"minimax",
	"o1-",
	"o3-",
	"o1/",
	"o3/",
	"o1_",
	"o3_",
}

// HasAgyModelEvidence checks whether output contains concrete evidence of available models.
// It abolishes the naive "any two words is a model" heuristic and requires explicit model
// family evidence or a recognized structured model entry format.
// Plain two-word or multi-word sentences (e.g. "service temporarily unavailable", "hello world")
// must NOT report ready.
func HasAgyModelEvidence(output string) bool {
	if strings.TrimSpace(output) == "" {
		return false
	}

	lines := strings.Split(output, "\n")
	for _, rawLine := range lines {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}

		lower := strings.ToLower(line)

		// Filter out noise / storage notice / spinner / info lines
		if strings.Contains(lower, "using file-based token storage") ||
			strings.Contains(lower, "fetching available models") ||
			strings.Contains(lower, "fetching models") ||
			strings.Contains(lower, "loading models") {
			continue
		}

		// Filter out error, warning, fatal, usage prefixes
		if strings.HasPrefix(lower, "error") ||
			strings.HasPrefix(lower, "err:") ||
			strings.HasPrefix(lower, "fatal") ||
			strings.HasPrefix(lower, "warning") ||
			strings.HasPrefix(lower, "warn:") ||
			strings.HasPrefix(lower, "info:") ||
			strings.HasPrefix(lower, "usage:") ||
			strings.HasPrefix(lower, "[error]") ||
			strings.HasPrefix(lower, "[warn]") {
			continue
		}

		// Filter out common HTTP / server / network error phrases
		if strings.Contains(lower, "service temporarily unavailable") ||
			strings.Contains(lower, "service unavailable") ||
			strings.Contains(lower, "temporarily unavailable") ||
			strings.Contains(lower, "bad gateway") ||
			strings.Contains(lower, "gateway timeout") ||
			strings.Contains(lower, "internal server error") ||
			strings.Contains(lower, "connection refused") ||
			strings.Contains(lower, "connection reset") ||
			strings.Contains(lower, "network unreachable") ||
			strings.Contains(lower, "unexpected eof") ||
			strings.Contains(lower, "rate limit exceeded") ||
			strings.Contains(lower, "request timeout") ||
			strings.Contains(lower, "permission denied") ||
			strings.Contains(lower, "unauthorized") ||
			strings.Contains(lower, "not logged into") ||
			strings.Contains(lower, "please sign in") ||
			strings.Contains(lower, "authentication required") {
			continue
		}

		// Check for recognized AI model families / keywords
		if isRecognizedModelLine(lower, line) {
			return true
		}

		// Check for structured technical model ID entry (e.g. "custom-model-v1 Custom Model")
		if isStructuredModelEntry(line) {
			return true
		}
	}

	return false
}

func isRecognizedModelLine(lower, rawLine string) bool {
	matchedKeyword := false
	for _, kw := range recognizedModelKeywords {
		if strings.Contains(lower, kw) {
			matchedKeyword = true
			break
		}
	}
	if !matchedKeyword {
		return false
	}

	clean := strings.TrimPrefix(rawLine, "- ")
	clean = strings.TrimPrefix(clean, "* ")
	fields := strings.Fields(clean)
	if len(fields) == 0 {
		return false
	}

	// Sentence words indicating failure / absence / status rather than a model listing
	sentenceStopWords := []string{
		"failed", "cannot", "could", "unable", "not", "no", "error", "please",
		"connect", "connection", "available", "found", "timeout", "server", "service",
	}
	firstWordLower := strings.ToLower(fields[0])
	for _, stop := range sentenceStopWords {
		if firstWordLower == stop {
			return false
		}
	}

	return true
}

func isStructuredModelEntry(rawLine string) bool {
	clean := strings.TrimPrefix(rawLine, "- ")
	clean = strings.TrimPrefix(clean, "* ")
	fields := strings.Fields(clean)
	// Must have at least 2 columns
	if len(fields) < 2 {
		return false
	}

	id := fields[0]

	// Must contain technical model ID delimiters: hyphen, slash, or colon
	if !strings.ContainsAny(id, "-/:") {
		return false
	}

	// Must contain digits or explicit version marker (e.g. "-v1", "7b", "120b", "3.8")
	hasVersion := strings.ContainsAny(id, "0123456789") ||
		strings.Contains(strings.ToLower(id), "-v") ||
		strings.Contains(strings.ToLower(id), "preview") ||
		strings.Contains(strings.ToLower(id), "latest")

	if !hasVersion {
		return false
	}

	// The first column must NOT be a common English word or sentence fragment
	commonProse := []string{
		"service", "server", "system", "client", "request", "response",
		"message", "status", "notice", "warning", "error", "failed",
	}
	idLower := strings.ToLower(id)
	for _, word := range commonProse {
		if strings.HasPrefix(idLower, word) {
			return false
		}
	}

	return true
}

// ProbeAgyAuth probes whether agy is authenticated for non-interactive execution.
// It executes 'agy models' with SSH session variables stripped and a short timeout.
// Unknown output, execution failure, or timeout must NOT report ready; only explicit
// login success evidence reports ready.
func ProbeAgyAuth(binPath string) (bool, string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return ProbeAgyAuthContext(ctx, binPath)
}

// ProbeAgyAuthContext probes whether agy is authenticated within the provided context.
func ProbeAgyAuthContext(ctx context.Context, binPath string) (bool, string) {
	cmd := exec.CommandContext(ctx, binPath, "models")
	cmd.Env = FilterChildProcessEnv(os.Environ())

	output, err := cmd.CombinedOutput()
	outStr := strings.TrimSpace(string(output))
	lower := strings.ToLower(outStr)

	// Explicit auth failure detection
	if strings.Contains(lower, "please sign in") ||
		strings.Contains(lower, "not logged into antigravity") ||
		strings.Contains(lower, "not logged into") ||
		strings.Contains(lower, "error getting token source") ||
		strings.Contains(lower, "authentication required") ||
		strings.Contains(lower, "not logged in and no controlling terminal") ||
		strings.Contains(lower, "paste the authorization code") ||
		strings.Contains(lower, "login required") ||
		strings.Contains(lower, "unauthenticated") {
		return false, "未登录 (需执行 'agy' 完成登录认证)"
	}

	if ctx.Err() != nil {
		return false, "探测超时 (执行 'agy models' 超时，无法确认登录态)"
	}

	if err != nil {
		errDetail := outStr
		if errDetail == "" {
			errDetail = err.Error()
		} else {
			// Extract the first meaningful error line
			for _, l := range strings.Split(errDetail, "\n") {
				l = strings.TrimSpace(l)
				if l != "" && !strings.Contains(strings.ToLower(l), "using file-based token storage") {
					errDetail = l
					break
				}
			}
		}
		return false, fmt.Sprintf("探测失败 (%s)", errDetail)
	}

	if !HasAgyModelEvidence(outStr) {
		return false, "未知输出 (未检测到可用模型列表，无法确认登录态)"
	}

	return true, "已登录就绪"
}

// ProbeAgents probes a list of agent configs.
func ProbeAgents(agents []AgentConfig) []AgentStatus {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return ProbeAgentsContext(ctx, agents)
}

// ProbeAgentsContext probes a list of agent configs within the provided context.
func ProbeAgentsContext(ctx context.Context, agents []AgentConfig) []AgentStatus {
	statuses := make([]AgentStatus, len(agents))
	for i, ag := range agents {
		statuses[i] = ProbeAgentContext(ctx, ag)
	}
	return statuses
}

// FilterChildProcessEnv strips credential-bearing variables and incoming SSH session indicators
// before a child process starts.
// Removing SSH_CLIENT, SSH_CONNECTION, and SSH_TTY ensures that local AI agent CLIs (e.g. agy)
// launched by listener or wrapper do not misidentify the execution environment as a remote/headless
// SSH session, allowing them to access the host's normal credential/keyring store just like
// interactive sessions.
// Leftover AGENTPULSE_MQTT_* and AGENTPULSE_BROKER_* values are also removed so a wrapped agent
// cannot inherit broker credentials.
func FilterChildProcessEnv(env []string) []string {
	var filtered []string
	for _, kv := range env {
		parts := strings.SplitN(kv, "=", 2)
		if len(parts) == 0 {
			continue
		}
		key := strings.ToUpper(parts[0])
		if strings.Contains(key, "PASSWORD") ||
			strings.HasPrefix(key, "AGENTPULSE_MQTT_") ||
			strings.HasPrefix(key, "AGENTPULSE_BROKER_") ||
			key == "SSH_CLIENT" ||
			key == "SSH_CONNECTION" ||
			key == "SSH_TTY" {
			continue
		}
		filtered = append(filtered, kv)
	}
	return filtered
}

// ResolveAgentBinary resolves an agent ID or alias to an executable path.
// If matching agent has explicit Path, that is returned.
// If matching agent has empty Path, it checks PATH by ID, then by aliases.
// If no agent matched, it falls back to looking up cmdOrAlias in PATH.
func ResolveAgentBinary(cfg *Config, cmdOrAlias string) string {
	target := strings.TrimSpace(cmdOrAlias)
	if target == "" {
		return ""
	}

	var agents []AgentConfig
	if cfg != nil && len(cfg.Agents) > 0 {
		agents = cfg.Agents
	} else {
		agents = DefaultAgents()
	}

	// 1. Check if matches configured agent ID or alias
	for _, ag := range agents {
		if !ag.IsEnabled() {
			continue
		}
		match := false
		if strings.EqualFold(ag.ID, target) {
			match = true
		} else {
			for _, al := range ag.Alias {
				if strings.EqualFold(al, target) {
					match = true
					break
				}
			}
		}

		if match {
			if ag.Path != "" {
				if resolved, err := exec.LookPath(ag.Path); err == nil {
					return resolved
				}
				return ag.Path
			}
			// Search PATH by id
			if p, err := exec.LookPath(ag.ID); err == nil {
				return p
			}
			// Search PATH by aliases
			for _, al := range ag.Alias {
				if al != "" {
					if p, err := exec.LookPath(al); err == nil {
						return p
					}
				}
			}
		}
	}

	// 2. Direct lookup in PATH
	if p, err := exec.LookPath(target); err == nil {
		return p
	}
	return target
}
