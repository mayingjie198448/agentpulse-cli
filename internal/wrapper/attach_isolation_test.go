package wrapper_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mayingjie198448/agentpulse-cli/internal/config"
	"github.com/mayingjie198448/agentpulse-cli/internal/wrapper"
)

func TestPSListArgsAllowlistIsTargeted(t *testing.T) {
	global := wrapper.PSListArgsForTest(nil)
	if strings.Join(global, " ") != "-A -o pid=,ppid=,args=" {
		t.Fatalf("empty allowlist args = %q", global)
	}
	targeted := wrapper.PSListArgsForTest([]int{42, 7, 42, 0, -3})
	joined := strings.Join(targeted, " ")
	if strings.Contains(joined, "-A") || strings.Contains(joined, "-e") {
		t.Fatalf("targeted args include a global selector: %q", targeted)
	}
	if joined != "-p 7,42 -o pid=,ppid=,args=" {
		t.Fatalf("targeted args = %q", targeted)
	}
}

func TestUnscopedDiscoveryStillRequestsGlobalPS(t *testing.T) {
	root := t.TempDir()
	isolateProcessEnv(t, root)
	t.Setenv("AGENTPULSE_ATTACH_PID_ALLOWLIST", "")

	var calls []string
	restore := wrapper.SetIsolationHooksForTest(wrapper.IsolationHooks{
		CommandContext: func(ctx context.Context, name string, args ...string) *exec.Cmd {
			if name == "ps" || name == "lsof" {
				calls = append(calls, name+" "+strings.Join(args, " "))
				// Record the requested argv, then run the reviewed helper.
				// The helper exits 0 and does not start an agent. No shell.
				argv := reviewedHelperArgv(t, "true")
				return exec.CommandContext(ctx, argv[0], argv[1:]...)
			}
			return exec.CommandContext(ctx, name, args...)
		},
	})
	defer restore()

	enabled := true
	mgr := wrapper.NewAttachManager(wrapper.AttachManagerConfig{
		MachineID:    "unscoped-node",
		ScanInterval: 30 * time.Millisecond,
		Agents: []config.AgentConfig{{
			ID:      "agy",
			Alias:   []string{"agy"},
			Enabled: &enabled,
		}},
		Quiet: true,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mgr.Start(ctx)
	time.Sleep(120 * time.Millisecond)
	mgr.Stop()

	if len(calls) == 0 {
		t.Fatal("expected the default lister to request ps when no allowlist is set")
	}
	for _, call := range calls {
		if call != "ps -A -o pid=,ppid=,args=" {
			t.Fatalf("unscoped discovery requested %q, want the existing global ps form", call)
		}
	}
}

func TestBareAttachDocExitIsNotAlwaysDone(t *testing.T) {
	path := filepath.Join("..", "..", "docs", "bare-cli-attach.md")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read doc: %v", err)
	}
	text := string(body)
	if strings.Contains(text, "自动跃迁为 `DONE`") || strings.Contains(text, "自动跃迁为DONE") {
		t.Fatal("bare-cli-attach.md still says a process exit becomes DONE")
	}
	if !strings.Contains(text, "无法判定退出码") {
		t.Fatal("bare-cli-attach.md no longer documents the unknown-exit failure")
	}
}

func isolateProcessEnv(t *testing.T, root string) {
	t.Helper()
	for _, dir := range []string{
		"home", "xdg-config", "xdg-cache", "xdg-data", "tmp", "gocache", "gopath", "gomodcache",
	} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0700); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "xdg-config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "xdg-cache"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "xdg-data"))
	t.Setenv("TMPDIR", filepath.Join(root, "tmp"))
	t.Setenv("GOCACHE", filepath.Join(root, "gocache"))
	t.Setenv("GOPATH", filepath.Join(root, "gopath"))
	t.Setenv("GOMODCACHE", filepath.Join(root, "gomodcache"))
	t.Setenv("GOENV", "off")
	t.Setenv("GOPROXY", "off")
	t.Setenv("GOSUMDB", "off")
	t.Setenv("GOTOOLCHAIN", "local")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
}
