package safety

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var runIDRe = regexp.MustCompile(`^[a-f0-9]{32}$`)

// RunRecord is the ownership proof for one run root.
// Dev and Ino are the directory identity taken from an O_NOFOLLOW open.
type RunRecord struct {
	RunID         string `json:"run_id"`
	Root          string `json:"root"`
	Work          string `json:"work"`
	Home          string `json:"home"`
	XDGConfigHome string `json:"xdg_config_home"`
	XDGDataHome   string `json:"xdg_data_home"`
	XDGCacheHome  string `json:"xdg_cache_home"`
	XDGStateHome  string `json:"xdg_state_home"`
	Tmp           string `json:"tmp"`
	Dev           uint64 `json:"dev"`
	Ino           uint64 `json:"ino"`
	Nonce         string `json:"nonce"`
	ProfileHash   string `json:"profile_hash"`
	ExeSHA256     string `json:"exe_sha256"`
	Sentinel      string `json:"sentinel"`
	SentinelDev   uint64 `json:"sentinel_dev"`
	SentinelIno   uint64 `json:"sentinel_ino"`
	CreatedUnix   int64  `json:"created_unix"`
	RegistryPath  string `json:"-"`
	MarkerPath    string `json:"-"`
	RegistryDir   string `json:"-"`
}

type profileIdentity struct {
	RunID       string
	ABI         int
	AccessFS    uint64
	Scoped      uint64
	Root        string
	Dev         uint64
	Ino         uint64
	Sentinel    string
	SentinelDev uint64
	SentinelIno uint64
}

func profileHash(p profileIdentity) string {
	s := fmt.Sprintf("v1\nrun=%s\nabi=%d\naccess_fs=%#x\nscoped=%#x\nroot=%s\ndev=%d\nino=%d\nsentinel=%s\nsentinel_dev=%d\nsentinel_ino=%d\nno_new_privs=1\n",
		p.RunID, p.ABI, p.AccessFS, p.Scoped, p.Root, p.Dev, p.Ino, p.Sentinel, p.SentinelDev, p.SentinelIno)
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func exeSHA256() (string, error) {
	p, err := os.Executable()
	if err != nil {
		return "", err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// DefaultRegistryDir is outside any one run root.
func DefaultRegistryDir() string {
	return filepath.Join(os.TempDir(), "agentpulse-run-registry")
}

// DefaultAuditPath is outside any one run root.
func DefaultAuditPath() string {
	return filepath.Join(os.TempDir(), "agentpulse-audit", "audit.jsonl")
}

func writeJSON(path string, v any, mode os.FileMode) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func loadRegistry(dir, runID string) (RunRecord, error) {
	if !runIDRe.MatchString(runID) {
		return RunRecord{}, fmt.Errorf("%w: run id is not a recorded identifier", ErrCleanupBlocked)
	}
	if strings.TrimSpace(dir) == "" {
		dir = DefaultRegistryDir()
	}
	path := filepath.Join(dir, runID+".json")
	b, err := os.ReadFile(path)
	if err != nil {
		return RunRecord{}, fmt.Errorf("%w: ownership record missing: %v", ErrCleanupBlocked, err)
	}
	var rec RunRecord
	if err := json.Unmarshal(b, &rec); err != nil {
		return RunRecord{}, fmt.Errorf("%w: ownership record unreadable", ErrCleanupBlocked)
	}
	if rec.RunID != runID || rec.Nonce == "" || rec.Root == "" {
		return RunRecord{}, fmt.Errorf("%w: ownership record does not match this run", ErrCleanupBlocked)
	}
	rec.RegistryDir = dir
	rec.RegistryPath = path
	rec.MarkerPath = filepath.Join(rec.Root, markerName)
	return rec, nil
}

func withinPath(parent, path string) bool {
	parent = filepath.Clean(parent)
	path = filepath.Clean(path)
	if parent == "" || path == "" {
		return false
	}
	if parent == path {
		return true
	}
	rel, err := filepath.Rel(parent, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}

func pathShapeError(path string) error {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return fmt.Errorf("%w: empty path", ErrCleanupBlocked)
	}
	if trimmed != path {
		return fmt.Errorf("%w: whitespace path", ErrCleanupBlocked)
	}
	if !filepath.IsAbs(path) {
		return fmt.Errorf("%w: relative path", ErrCleanupBlocked)
	}
	clean := filepath.Clean(path)
	if clean == "/" || clean == "." || clean == ".." {
		return fmt.Errorf("%w: refuse %s", ErrCleanupBlocked, clean)
	}
	for _, part := range strings.Split(strings.TrimPrefix(clean, "/"), "/") {
		if part == "" || part == "." || part == ".." {
			return fmt.Errorf("%w: refuse component %q", ErrCleanupBlocked, part)
		}
	}
	return nil
}
