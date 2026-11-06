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

// ProbeAgent probes whether the given agent CLI executable is available.
func ProbeAgent(ag AgentConfig) AgentStatus {
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

	// If explicit path configured
	if ag.Path != "" {
		resolved, err := exec.LookPath(ag.Path)
		if err == nil {
			st.Ok = true
			st.ResolvedPath = resolved
			st.Detail = resolved
		} else {
			st.Ok = false
			st.Detail = fmt.Sprintf("指定路径不可用: %v", err)
		}
		return st
	}

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

	if !st.Ok {
		st.Detail = "未在 PATH 中找到可执行文件"
		return st
	}

	// For agy, probe whether it is logged in and ready for non-interactive execution
	if filepath.Base(st.ResolvedPath) == "agy" || ag.ID == "agy" {
		authOk, detail := ProbeAgyAuth(st.ResolvedPath)
		if !authOk {
			st.Ok = false
			st.Detail = detail
		} else if detail != "" {
			st.Detail = fmt.Sprintf("%s (%s)", st.ResolvedPath, detail)
		}
	}

	return st
}

// HasAgyModelEvidence checks whether output contains concrete evidence of available models.
func HasAgyModelEvidence(output string) bool {
	lower := strings.ToLower(output)
	if strings.Contains(lower, "gemini") ||
		strings.Contains(lower, "claude") ||
		strings.Contains(lower, "gpt") ||
		strings.Contains(lower, "sonnet") ||
		strings.Contains(lower, "opus") ||
		strings.Contains(lower, "flash") {
		return true
	}

	// Also check if any non-spinner, non-storage line looks like a model entry
	for _, rawLine := range strings.Split(output, "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" ||
			strings.Contains(line, "Fetching available models") ||
			strings.Contains(strings.ToLower(line), "using file-based token storage") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 2 && !strings.HasPrefix(fields[0], "-") && !strings.HasPrefix(fields[0], "Error") {
			return true
		}
	}
	return false
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
		strings.Contains(lower, "error getting token source") ||
		strings.Contains(lower, "authentication required") ||
		strings.Contains(lower, "not logged in and no controlling terminal") ||
		strings.Contains(lower, "paste the authorization code") {
		return false, "未登录 (需执行 'agy' 完成登录认证)"
	}

	if ctx.Err() == context.DeadlineExceeded {
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
	statuses := make([]AgentStatus, len(agents))
	for i, ag := range agents {
		statuses[i] = ProbeAgent(ag)
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
