package wrapper

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/mayingjie198448/agentpulse-cli/internal/model"
)

const (
	// DefaultMaxPromptLength is the default maximum byte length for prompt summary retained in job status.
	DefaultMaxPromptLength = 256
)

var (
	// Regex for Private Key blocks (PEM format: RSA, EC, DSA, OPENSSH, etc.)
	privateKeyRegex = regexp.MustCompile(`-----BEGIN [A-Z ]+ PRIVATE KEY-----[\s\S]*?-----END [A-Z ]+ PRIVATE KEY-----`)

	// Regex for URL embedded credentials: http://user:pass@host
	urlCredRegex = regexp.MustCompile(`(https?://[^:\s]+):([^@\s]+)@`)

	// Regex for Bearer / Authorization tokens in headers or strings
	bearerRegex = regexp.MustCompile(`(?i)\b(bearer|authorization)\s+[a-zA-Z0-9_\-\.]{10,}\b`)

	// Regex for CLI flag credentials (e.g. --api-key secret, --token secret)
	cliFlagCredRegex = regexp.MustCompile(`(?i)(--?[a-z0-9_-]*(?:api[_-]?key|access[_-]?token|secret[_-]?key|secret|password|passwd|auth[_-]?token|token))\s+([^\s,;]+)`)

	// Regex for key-value credentials: password=xyz, token=xyz, secret=xyz, etc.
	credentialKVRegex = regexp.MustCompile(`(?i)\b([a-z0-9_-]*(?:api[_-]?key|access[_-]?token|secret[_-]?key|secret|password|passwd|auth[_-]?token|token))\s*([:=])\s*([^\s,;]+)`)

	// Regex for OpenAI / Anthropic / AI vendor keys (e.g. sk-..., ant-..., key-..., ai-...)
	apiKeyRegex = regexp.MustCompile(`\b(sk|ant|key|ai)-[a-zA-Z0-9_\-]{16,}\b`)

	// Regex for GitHub / GitLab / HuggingFace tokens
	githubTokenRegex = regexp.MustCompile(`\b(gh[pousr]_[A-Za-z0-9_]{16,}|github_pat_[A-Za-z0-9_]{16,}|glpat-[A-Za-z0-9_\-]{20,}|hf_[A-Za-z0-9]{20,})\b`)

	// Regex for Slack / Discord / OAuth tokens
	oauthTokenRegex = regexp.MustCompile(`\bxox[baprs]-[A-Za-z0-9_\-]{16,}\b`)

	// Regex for AWS Access Key IDs
	awsKeyRegex = regexp.MustCompile(`\b(AKIA|ABIA|ACCA|ASIA)[0-9A-Z]{16}\b`)
)

// RedactSecrets scans a text string and replaces detected credentials, API keys, and tokens
// with redaction placeholders.
//
// Known patterns covered:
//   - PEM Private Key blocks
//   - Embedded URL credentials (e.g. https://user:token@github.com)
//   - Authorization / Bearer tokens
//   - CLI flags with credential values (e.g. --api-key secret, --token secret)
//   - Key-value credentials (e.g. password=secret, token:secret)
//   - Vendor-specific tokens: OpenAI/Anthropic (sk-..., ant-..., ai-...), GitHub (ghp_..., github_pat_...),
//     GitLab (glpat-...), HuggingFace (hf_...), Slack (xoxb-...), AWS Access Keys (AKIA...).
//
// Boundary Note: Non-exhaustive regex patterns provide defense-in-depth sanitization, but do
// not constitute an absolute cryptographic security guarantee. Users should avoid embedding credentials
// directly in task command lines where environment variables or credential managers can be used.
func RedactSecrets(text string) string {
	if text == "" {
		return ""
	}

	// 1. Redact Private Key blocks
	text = privateKeyRegex.ReplaceAllString(text, "[PRIVATE_KEY_REDACTED]")

	// 2. Redact URL credentials
	text = urlCredRegex.ReplaceAllString(text, "${1}:[REDACTED]@")

	// 3. Redact Bearer / Authorization headers
	text = bearerRegex.ReplaceAllString(text, "${1} [REDACTED]")

	// 4. Redact CLI flag credentials (e.g. --api-key secret)
	text = cliFlagCredRegex.ReplaceAllString(text, "${1} [REDACTED]")

	// 5. Redact Key-Value credentials (e.g. password=secret)
	text = credentialKVRegex.ReplaceAllString(text, "${1}${2}[REDACTED]")

	// 6. Redact vendor-specific API keys and tokens
	text = apiKeyRegex.ReplaceAllString(text, "${1}-[REDACTED]")
	text = githubTokenRegex.ReplaceAllString(text, "[REDACTED_TOKEN]")
	text = oauthTokenRegex.ReplaceAllString(text, "[REDACTED_TOKEN]")
	text = awsKeyRegex.ReplaceAllString(text, "[REDACTED_AWS_KEY]")

	return text
}

// SanitizeCommand cleans and redacts secret keys/tokens from command strings so sensitive data
// is never leaked into MQTT retained messages or external job metadata.
func SanitizeCommand(cmd string) string {
	return RedactSecrets(cmd)
}

// SanitizeTitle cleans and bounds a title string, ensuring secrets are redacted.
func SanitizeTitle(title string, maxLen int) string {
	if maxLen <= 0 {
		maxLen = 60
	}
	return SanitizePrompt(title, maxLen)
}

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

	cleaned = RedactSecrets(cleaned)

	// Normalize newlines and consecutive whitespace to a single space
	cleaned = strings.Join(strings.Fields(cleaned), " ")

	// Enforce max rune length
	runes := []rune(cleaned)
	if len(runes) > maxLen {
		if maxLen > 3 {
			cleaned = string(runes[:maxLen-3]) + "..."
		} else if maxLen > 0 {
			cleaned = string(runes[:maxLen])
		} else {
			cleaned = ""
		}
	}

	return cleaned
}

// SanitizeJob returns a copy of model.Job with Command, Title, Prompt, and Error
// fields consistently redacted for external visibility boundaries (MQTT retained, status files, alerts).
func SanitizeJob(j model.Job) model.Job {
	sanitized := j
	sanitized.Command = SanitizeCommand(j.Command)
	sanitized.Title = SanitizeTitle(j.Title, 100)
	sanitized.Prompt = SanitizePrompt(j.Prompt, DefaultMaxPromptLength)
	if sanitized.Error != "" {
		sanitized.Error = RedactSecrets(sanitized.Error)
	}
	return sanitized
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

// repoGitLookup runs git for InferRepoAndProject.
// The default starts the git binary. Tests may replace it; production does not.
var repoGitLookup = defaultRepoGitLookup

func defaultRepoGitLookup(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	return cmd.Output()
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

	// 1. Check if git repository. Production runs git; tests may replace repoGitLookup.
	outGitRoot, errGitRoot := repoGitLookup(ctx, cleanCwd, "rev-parse", "--show-toplevel")
	if errGitRoot == nil {
		gitRoot := strings.TrimSpace(string(outGitRoot))
		if gitRoot != "" {
			project = filepath.Base(gitRoot)
			repo = project

			// Try to extract remote origin url
			outRemote, errRemote := repoGitLookup(ctx, cleanCwd, "config", "--get", "remote.origin.url")
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
