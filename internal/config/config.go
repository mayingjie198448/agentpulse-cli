package config

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config represents the AgentPulse v1 configuration stored in ~/.config/agentpulse/config.yaml.
type Config struct {
	Version   int           `yaml:"version"`
	MachineID string        `yaml:"machine_id"`
	Rev       int           `yaml:"rev,omitempty"`
	Broker    BrokerConfig  `yaml:"broker"`
	Agents    []AgentConfig `yaml:"agents"`
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
	Custom  bool     `yaml:"custom,omitempty"`
}

// IsEnabled returns true if the agent is enabled (defaults to true if unspecified).
func (a AgentConfig) IsEnabled() bool {
	if a.Enabled == nil {
		return true
	}
	return *a.Enabled
}

// IsCustom returns true if the agent is a custom (non-builtin) agent CLI.
func (a AgentConfig) IsCustom() bool {
	if a.Custom {
		return true
	}
	return !IsBuiltinEngine(a.ID)
}

// IsBuiltin returns true if the agent is a built-in adapted agent engine.
func (a AgentConfig) IsBuiltin() bool {
	return !a.IsCustom()
}

// BuiltinAgents returns the configured agents that are built-in adapted engines.
func (c *Config) BuiltinAgents() []AgentConfig {
	var list []AgentConfig
	for _, ag := range c.Agents {
		if ag.IsBuiltin() {
			list = append(list, ag)
		}
	}
	return list
}

// CustomAgents returns the configured agents that are custom/non-builtin agent CLIs.
func (c *Config) CustomAgents() []AgentConfig {
	var list []AgentConfig
	for _, ag := range c.Agents {
		if ag.IsCustom() {
			list = append(list, ag)
		}
	}
	return list
}

// AgentStatus represents the probed runtime status of a registered agent.
type AgentStatus struct {
	ID           string   `json:"id"`
	Alias        []string `json:"alias"`
	Path         string   `json:"path,omitempty"`
	ResolvedPath string   `json:"resolved_path,omitempty"`
	Ok           bool     `json:"ok"`
	Detail       string   `json:"detail"`
	Custom       bool     `json:"custom,omitempty"`
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
		ID:     ag.ID,
		Alias:  ag.Alias,
		Path:   ag.Path,
		Custom: ag.IsCustom(),
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
		return st
	}

	for _, alias := range ag.Alias {
		if alias != "" && alias != ag.ID {
			if p, err := exec.LookPath(alias); err == nil {
				st.Ok = true
				st.ResolvedPath = p
				st.Detail = p
				return st
			}
		}
	}

	st.Ok = false
	st.Detail = "未在 PATH 中找到可执行文件"
	return st
}

// ProbeAgents probes a list of agent configs.
func ProbeAgents(agents []AgentConfig) []AgentStatus {
	statuses := make([]AgentStatus, len(agents))
	for i, ag := range agents {
		statuses[i] = ProbeAgent(ag)
	}
	return statuses
}

// FilterChildProcessEnv strips credential-bearing variables before a child process starts.
// Product configuration is not read from the environment. Leftover AGENTPULSE_MQTT_* and
// AGENTPULSE_BROKER_* values are still removed so a wrapped agent cannot inherit broker material.
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
			strings.HasPrefix(key, "AGENTPULSE_BROKER_") {
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
