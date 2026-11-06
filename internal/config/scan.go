package config

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// BuiltinEngineDef defines the metadata and probe signatures for a built-in agent engine.
type BuiltinEngineDef struct {
	ID           string   `json:"id" yaml:"id"`
	Name         string   `json:"name" yaml:"name"`
	Description  string   `json:"description" yaml:"description"`
	BinaryNames  []string `json:"binary_names" yaml:"binary_names"`
	DefaultAlias []string `json:"default_alias" yaml:"default_alias"`
	CommonPaths  []string `json:"common_paths,omitempty" yaml:"common_paths,omitempty"`
}

// BuiltinEngineDefs returns the list of first-batch preconfigured candidate agent engines.
// Priority is given to headless/non-interactive terminal agents.
func BuiltinEngineDefs() []BuiltinEngineDef {
	return []BuiltinEngineDef{
		{
			ID:           "agy",
			Name:         "Antigravity CLI (agy)",
			Description:  "Google Antigravity CLI agent",
			BinaryNames:  []string{"agy"},
			DefaultAlias: []string{"agy"},
			CommonPaths: []string{
				"~/.gemini/antigravity-cli/bin",
				"~/.antigravity/bin",
				"~/.local/bin",
				"/opt/homebrew/bin",
				"/usr/local/bin",
			},
		},
		{
			ID:           "opencode",
			Name:         "OpenCode CLI (opencode)",
			Description:  "OpenCode AI coding agent",
			BinaryNames:  []string{"opencode", "op"},
			DefaultAlias: []string{"op", "opencode"},
			CommonPaths: []string{
				"~/.local/bin",
				"~/.opencode/bin",
				"/opt/homebrew/bin",
				"/usr/local/bin",
			},
		},
		{
			ID:           "claude",
			Name:         "Claude Code (claude)",
			Description:  "Anthropic Claude Code CLI",
			BinaryNames:  []string{"claude"},
			DefaultAlias: []string{"claude"},
			CommonPaths: []string{
				"~/.local/bin",
				"/opt/homebrew/bin",
				"/usr/local/bin",
			},
		},
		{
			ID:           "codex",
			Name:         "OpenAI Codex CLI (codex)",
			Description:  "OpenAI Codex CLI agent",
			BinaryNames:  []string{"codex"},
			DefaultAlias: []string{"codex"},
			CommonPaths: []string{
				"~/.local/bin",
				"/opt/homebrew/bin",
				"/usr/local/bin",
			},
		},
		{
			ID:           "gemini",
			Name:         "Gemini CLI (gemini)",
			Description:  "Google Gemini CLI agent",
			BinaryNames:  []string{"gemini"},
			DefaultAlias: []string{"gemini"},
			CommonPaths: []string{
				"~/.local/bin",
				"/opt/homebrew/bin",
				"/usr/local/bin",
			},
		},
		{
			ID:           "copilot",
			Name:         "GitHub Copilot CLI (copilot)",
			Description:  "GitHub Copilot in the CLI",
			BinaryNames:  []string{"copilot", "github-copilot-cli"},
			DefaultAlias: []string{"copilot"},
			CommonPaths: []string{
				"~/.local/bin",
				"/opt/homebrew/bin",
				"/usr/local/bin",
			},
		},
		{
			ID:           "aider",
			Name:         "Aider (aider)",
			Description:  "Aider AI pair programming CLI",
			BinaryNames:  []string{"aider"},
			DefaultAlias: []string{"aider"},
			CommonPaths: []string{
				"~/.local/bin",
				"/opt/homebrew/bin",
				"/usr/local/bin",
			},
		},
		{
			ID:           "goose",
			Name:         "Goose (goose)",
			Description:  "Block Goose developer agent",
			BinaryNames:  []string{"goose"},
			DefaultAlias: []string{"goose"},
			CommonPaths: []string{
				"~/.local/bin",
				"~/.goose/bin",
				"/opt/homebrew/bin",
				"/usr/local/bin",
			},
		},
		{
			ID:           "cline",
			Name:         "Cline CLI (cline)",
			Description:  "Cline autonomous coding agent CLI",
			BinaryNames:  []string{"cline"},
			DefaultAlias: []string{"cline"},
			CommonPaths: []string{
				"~/.local/bin",
				"/opt/homebrew/bin",
				"/usr/local/bin",
			},
		},
	}
}

// LookupBuiltinEngine finds a built-in engine definition by ID (case-insensitive).
func LookupBuiltinEngine(id string) (BuiltinEngineDef, bool) {
	lower := strings.ToLower(strings.TrimSpace(id))
	for _, def := range BuiltinEngineDefs() {
		if strings.ToLower(def.ID) == lower {
			return def, true
		}
	}
	return BuiltinEngineDef{}, false
}

// IsBuiltinEngine returns true if the specified agent ID matches a built-in adapted engine.
func IsBuiltinEngine(id string) bool {
	_, ok := LookupBuiltinEngine(id)
	return ok
}

// DiscoveredAgent represents an agent CLI discovered on the local machine during scanning.
type DiscoveredAgent struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Path         string   `json:"path"`
	Builtin      bool     `json:"builtin"`
	DefaultAlias []string `json:"default_alias"`
	Detail       string   `json:"detail"`
}

// DiscoveredIDEPlugin represents an IDE plugin or extension detected during scan.
type DiscoveredIDEPlugin struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	Detail string `json:"detail"`
}

// ScanResult holds the aggregated results of a local agent CLI scan.
type ScanResult struct {
	DiscoveredBuiltin []DiscoveredAgent     `json:"discovered_builtin"`
	MissingBuiltin    []BuiltinEngineDef    `json:"missing_builtin"`
	DiscoveredCustom  []DiscoveredAgent     `json:"discovered_custom"`
	IDEPlugins        []DiscoveredIDEPlugin `json:"ide_plugins"`
}

// ScanOptions configures the scanner environment, enabling hermetic unit tests.
type ScanOptions struct {
	ExtraPaths   []string
	OverridePATH string
	HomeDir      string
}

// CandidateCustomBinaries are other known agent CLIs that may be discovered.
var candidateCustomBinaries = []string{
	"cursor",
	"windsurf",
	"devin",
	"sweep",
	"openhands",
	"mentat",
	"gpt-engineer",
	"chatgpt",
	"bolt",
	"interpreter",
}

// ScanLocalAgents scans PATH and common installation paths on the local machine.
// It categorizes results into built-in candidate engines, missing built-in engines,
// discovered custom CLIs, and IDE plugins (e.g. Continue).
func ScanLocalAgents(opts ...ScanOptions) ScanResult {
	var opt ScanOptions
	if len(opts) > 0 {
		opt = opts[0]
	}

	home := opt.HomeDir
	if home == "" {
		if h, err := os.UserHomeDir(); err == nil {
			home = h
		}
	}

	pathEnv := opt.OverridePATH
	if pathEnv == "" && len(opts) == 0 {
		pathEnv = os.Getenv("PATH")
	}

	searchDirs := buildSearchDirs(pathEnv, home, opt.ExtraPaths)

	var res ScanResult

	// 1. Scan Built-in Engines
	discoveredMap := make(map[string]bool)
	for _, def := range BuiltinEngineDefs() {
		foundPath := findExecutableForEngine(def, searchDirs, pathEnv)
		if foundPath != "" {
			discoveredMap[def.ID] = true
			res.DiscoveredBuiltin = append(res.DiscoveredBuiltin, DiscoveredAgent{
				ID:           def.ID,
				Name:         def.Name,
				Path:         foundPath,
				Builtin:      true,
				DefaultAlias: def.DefaultAlias,
				Detail:       foundPath,
			})
		} else {
			res.MissingBuiltin = append(res.MissingBuiltin, def)
		}
	}

	// 2. Scan for IDE plugins (e.g. Continue)
	detectIDEPlugins(home, searchDirs, &res)

	// 3. Scan for other candidate custom agent CLIs on PATH
	for _, cand := range candidateCustomBinaries {
		if IsBuiltinEngine(cand) {
			continue
		}
		foundPath := findExecutable(cand, searchDirs, pathEnv)
		if foundPath != "" {
			res.DiscoveredCustom = append(res.DiscoveredCustom, DiscoveredAgent{
				ID:           cand,
				Name:         cand,
				Path:         foundPath,
				Builtin:      false,
				DefaultAlias: []string{cand},
				Detail:       foundPath,
			})
		}
	}

	return res
}

func buildSearchDirs(pathEnv string, home string, extraPaths []string) []string {
	seen := make(map[string]bool)
	var dirs []string

	addDir := func(d string) {
		d = expandHome(d, home)
		clean := filepath.Clean(d)
		if clean == "" || clean == "." {
			return
		}
		if !seen[clean] {
			seen[clean] = true
			dirs = append(dirs, clean)
		}
	}

	// PATH entries
	for _, p := range filepath.SplitList(pathEnv) {
		if strings.TrimSpace(p) != "" {
			addDir(p)
		}
	}

	// Extra paths
	for _, p := range extraPaths {
		addDir(p)
	}

	// Common standard installation paths
	if home != "" {
		addDir(filepath.Join(home, ".local", "bin"))
		addDir(filepath.Join(home, "bin"))
		addDir(filepath.Join(home, ".cargo", "bin"))
		addDir(filepath.Join(home, "go", "bin"))
		addDir(filepath.Join(home, ".gemini", "antigravity-cli", "bin"))
		addDir(filepath.Join(home, ".antigravity", "bin"))
		addDir(filepath.Join(home, ".opencode", "bin"))
		addDir(filepath.Join(home, ".goose", "bin"))
		addDir(filepath.Join(home, ".npm-global", "bin"))
		addDir(filepath.Join(home, ".local", "share", "pnpm"))
		if runtime.GOOS == "darwin" {
			addDir(filepath.Join(home, "Library", "Application Support", "pnpm"))
		}
		if runtime.GOOS == "windows" {
			addDir(filepath.Join(home, "AppData", "Roaming", "npm"))
			addDir(filepath.Join(home, "AppData", "Local", "Programs"))
		}

		// NVM versions node bins
		nvmDir := filepath.Join(home, ".nvm", "versions", "node")
		if entries, err := os.ReadDir(nvmDir); err == nil {
			for _, entry := range entries {
				if entry.IsDir() {
					addDir(filepath.Join(nvmDir, entry.Name(), "bin"))
				}
			}
		}
	}

	if runtime.GOOS == "darwin" || runtime.GOOS == "linux" {
		addDir("/opt/homebrew/bin")
		addDir("/opt/homebrew/sbin")
		addDir("/usr/local/bin")
		addDir("/usr/bin")
		addDir("/bin")
	}

	return dirs
}

func expandHome(path string, home string) string {
	if home == "" {
		return path
	}
	if path == "~" {
		return home
	}
	if strings.HasPrefix(path, "~/") || strings.HasPrefix(path, `~\`) {
		return filepath.Join(home, path[2:])
	}
	return path
}

func findExecutableForEngine(def BuiltinEngineDef, searchDirs []string, pathEnv string) string {
	for _, binName := range def.BinaryNames {
		p := findExecutable(binName, searchDirs, pathEnv)
		if p != "" {
			return p
		}
	}
	return ""
}

func findExecutable(binName string, searchDirs []string, pathEnv string) string {
	// Look up in search directories
	for _, dir := range searchDirs {
		candidate := filepath.Join(dir, binName)
		if isExecutableFile(candidate) {
			return candidate
		}
		if runtime.GOOS == "windows" && !strings.HasSuffix(strings.ToLower(binName), ".exe") {
			candidateExe := filepath.Join(dir, binName+".exe")
			if isExecutableFile(candidateExe) {
				return candidateExe
			}
		}
	}

	// Try exec.LookPath if PATH matches current environment
	if pathEnv == os.Getenv("PATH") && pathEnv != "" {
		if resolved, err := exec.LookPath(binName); err == nil {
			return resolved
		}
	}

	return ""
}

func isExecutableFile(path string) bool {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	if runtime.GOOS == "windows" {
		ext := strings.ToLower(filepath.Ext(path))
		return ext == ".exe" || ext == ".bat" || ext == ".cmd"
	}
	return (info.Mode() & 0111) != 0
}

func detectIDEPlugins(home string, searchDirs []string, res *ScanResult) {
	if home == "" {
		return
	}

	// 1. Continue CLI or ~/.continue config
	continueConfig := filepath.Join(home, ".continue")
	if info, err := os.Stat(continueConfig); err == nil && info.IsDir() {
		res.IDEPlugins = append(res.IDEPlugins, DiscoveredIDEPlugin{
			Name:   "Continue",
			Path:   continueConfig,
			Detail: "发现 Continue 配置文件目录 (~/.continue)，暂不作为独立终端 CLI 引擎挂接",
		})
	} else if p := findExecutable("continue", searchDirs, ""); p != "" {
		res.IDEPlugins = append(res.IDEPlugins, DiscoveredIDEPlugin{
			Name:   "Continue",
			Path:   p,
			Detail: "发现 Continue 二进制，属 IDE 插件扩展，暂不作为独立终端 CLI 引擎挂接",
		})
	}

	// 2. VS Code / Cursor extensions for Continue
	checkExtensionsDir(filepath.Join(home, ".vscode", "extensions"), "Continue", res)
	checkExtensionsDir(filepath.Join(home, ".cursor", "extensions"), "Continue", res)
}

func checkExtensionsDir(extDir, targetName string, res *ScanResult) {
	entries, err := os.ReadDir(extDir)
	if err != nil {
		return
	}
	targetLower := strings.ToLower(targetName)
	for _, entry := range entries {
		if entry.IsDir() && strings.Contains(strings.ToLower(entry.Name()), targetLower) {
			// Check if we already noted it
			alreadyNoted := false
			for _, p := range res.IDEPlugins {
				if p.Name == targetName && strings.Contains(p.Detail, "扩展插件") {
					alreadyNoted = true
					break
				}
			}
			if !alreadyNoted {
				fullPath := filepath.Join(extDir, entry.Name())
				res.IDEPlugins = append(res.IDEPlugins, DiscoveredIDEPlugin{
					Name:   targetName,
					Path:   fullPath,
					Detail: "发现 " + targetName + " 编辑器扩展插件，暂不作为独立终端 CLI 引擎挂接",
				})
			}
			break
		}
	}
}
