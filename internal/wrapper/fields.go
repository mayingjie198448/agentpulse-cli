package wrapper

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	// DefaultMaxPromptLength is the default maximum byte length for prompt summary retained in job status.
	DefaultMaxPromptLength = 256
)

var (
	// Regex for OpenAI / Anthropic / AI vendor keys (e.g. sk-..., ant-...)
	apiKeyRegex = regexp.MustCompile(`\b(sk|ant|key)-[a-zA-Z0-9_\-]{16,}\b`)

	// Regex for GitHub tokens
	githubTokenRegex = regexp.MustCompile(`\b(gh[pousr]_[A-Za-z0-9_]{20,}|github_pat_[A-Za-z0-9_]{20,})\b`)

	// Regex for Slack / Discord / OAuth tokens
	oauthTokenRegex = regexp.MustCompile(`\bxox[baprs]-[A-Za-z0-9_\-]{16,}\b`)

	// Regex for AWS Access Key IDs
	awsKeyRegex = regexp.MustCompile(`\b(AKIA|ABIA|ACCA|ASIA)[0-9A-Z]{16}\b`)

	// Regex for Bearer tokens
	bearerRegex = regexp.MustCompile(`(?i)\b(bearer|authorization)\s+[a-zA-Z0-9_\-\.]{10,}\b`)

	// Regex for key-value credentials: password=xyz, token=xyz, secret=xyz, etc.
	credentialKVRegex = regexp.MustCompile(`(?i)\b(api[_-]?key|access[_-]?token|secret[_-]?key|secret|password|passwd|auth[_-]?token|token)\s*([:=])\s*([^\s,;]+)`)

	// Regex for URL embedded credentials: http://user:pass@host
	urlCredRegex = regexp.MustCompile(`(https?://[^:\s]+):([^@\s]+)@`)

	// Regex for Private Key blocks
	privateKeyRegex = regexp.MustCompile(`-----BEGIN [A-Z ]+ PRIVATE KEY-----[\s\S]*?-----END [A-Z ]+ PRIVATE KEY-----`)
)

// SanitizePrompt redacts secret keys/tokens and enforces length limits so sensitive data
// NEVER leaks into MQTT retained messages.
func SanitizePrompt(prompt string, maxLen int) string {
	if maxLen <= 0 {
		maxLen = DefaultMaxPromptLength
	}

	cleaned := strings.TrimSpace(prompt)
	if cleaned == "" {
		return ""
	}

	// 1. Redact Private Key blocks
	cleaned = privateKeyRegex.ReplaceAllString(cleaned, "[PRIVATE_KEY_REDACTED]")

	// 2. Redact URL credentials
	cleaned = urlCredRegex.ReplaceAllString(cleaned, "${1}:[REDACTED]@")

	// 3. Redact Bearer / Authorization headers
	cleaned = bearerRegex.ReplaceAllString(cleaned, "${1} [REDACTED]")

	// 4. Redact Key-Value credentials (e.g. password=secret)
	cleaned = credentialKVRegex.ReplaceAllString(cleaned, "${1}${2}[REDACTED]")

	// 5. Redact vendor-specific API keys and tokens
	cleaned = apiKeyRegex.ReplaceAllString(cleaned, "${1}-[REDACTED]")
	cleaned = githubTokenRegex.ReplaceAllString(cleaned, "[REDACTED_TOKEN]")
	cleaned = oauthTokenRegex.ReplaceAllString(cleaned, "[REDACTED_TOKEN]")
	cleaned = awsKeyRegex.ReplaceAllString(cleaned, "[REDACTED_AWS_KEY]")

	// 6. Normalize newlines and consecutive whitespace to a single space
	cleaned = strings.Join(strings.Fields(cleaned), " ")

	// 7. Enforce max length
	runes := []rune(cleaned)
	if len(runes) > maxLen {
		if maxLen > 3 {
			cleaned = string(runes[:maxLen-3]) + "..."
		} else {
			cleaned = string(runes[:maxLen])
		}
	}

	return cleaned
}

// ExtractPrompt scans command-line arguments for prompt/task flags (e.g. -p, --prompt, --task).
func ExtractPrompt(args []string) string {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if (arg == "-p" || arg == "--prompt" || arg == "--task") && i+1 < len(args) {
			return args[i+1]
		}
		if strings.HasPrefix(arg, "--prompt=") {
			return strings.TrimPrefix(arg, "--prompt=")
		}
		if strings.HasPrefix(arg, "--task=") {
			return strings.TrimPrefix(arg, "--task=")
		}
		if strings.HasPrefix(arg, "-p=") {
			return strings.TrimPrefix(arg, "-p=")
		}
	}
	return ""
}

// InferRepoAndProject inspects the given working directory or git remote to infer
// the repository slug (e.g. "owner/repo" or "repo") and project name.
func InferRepoAndProject(cwd string) (repo, project string) {
	if cwd == "" {
		if cur, err := os.Getwd(); err == nil {
			cwd = cur
		} else {
			cwd = "."
		}
	}

	cleanCwd, err := filepath.Abs(cwd)
	if err != nil {
		cleanCwd = cwd
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// 1. Check if git repository
	cmdGitRoot := exec.CommandContext(ctx, "git", "rev-parse", "--show-toplevel")
	cmdGitRoot.Dir = cleanCwd
	outGitRoot, errGitRoot := cmdGitRoot.Output()
	if errGitRoot == nil {
		gitRoot := strings.TrimSpace(string(outGitRoot))
		if gitRoot != "" {
			project = filepath.Base(gitRoot)
			repo = project

			// Try to extract remote origin url
			cmdRemote := exec.CommandContext(ctx, "git", "config", "--get", "remote.origin.url")
			cmdRemote.Dir = cleanCwd
			outRemote, errRemote := cmdRemote.Output()
			if errRemote == nil {
				remoteURL := strings.TrimSpace(string(outRemote))
				slug := extractGitRepoSlug(remoteURL)
				if slug != "" {
					repo = slug
				}
			}
			return repo, project
		}
	}

	// 2. Fallback to folder name
	base := filepath.Base(cleanCwd)
	if base != "" && base != "." && base != "/" {
		project = base
		repo = base
	}

	return repo, project
}

// extractGitRepoSlug extracts the repository path / slug from git remote URLs.
// Supports:
// - git@github.com:owner/repo.git -> owner/repo
// - https://github.com/owner/repo.git -> owner/repo
// - ssh://git@host:port/owner/repo.git -> owner/repo
func extractGitRepoSlug(url string) string {
	url = strings.TrimSpace(url)
	if url == "" {
		return ""
	}

	url = strings.TrimSuffix(url, ".git")

	// SCP style: git@host:owner/repo
	if strings.Contains(url, ":") && !strings.Contains(url, "://") {
		parts := strings.SplitN(url, ":", 2)
		if len(parts) == 2 {
			slug := strings.Trim(parts[1], "/")
			if slug != "" {
				return slug
			}
		}
	}

	// URL style: https://host/owner/repo
	if strings.Contains(url, "://") {
		parts := strings.SplitN(url, "://", 2)
		if len(parts) == 2 {
			pathParts := strings.SplitN(parts[1], "/", 2)
			if len(pathParts) == 2 {
				slug := strings.Trim(pathParts[1], "/")
				if slug != "" {
					return slug
				}
			}
		}
	}

	return ""
}
