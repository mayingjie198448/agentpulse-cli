package safety

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

var safetyWorkerHandler func([]string)

func TestMain(m *testing.M) {
	MaybeHandleInternal(os.Args)
	if safetyWorkerHandler != nil {
		safetyWorkerHandler(os.Args)
	}
	os.Exit(m.Run())
}

func TestPrepareLaunchArgv_DefaultDoesNotInjectDangerFlag(t *testing.T) {
	in := []string{"agy", "-p", "hello"}
	out, danger := PrepareLaunchArgv(in, false)
	if danger {
		t.Fatal("default launch was treated as danger mode")
	}
	if strings.Join(out, " ") != strings.Join(in, " ") {
		t.Fatalf("argv changed: %q", out)
	}
	if strings.Contains(strings.Join(out, " "), "dangerously-skip-permissions") {
		t.Fatalf("danger flag injected: %q", out)
	}
}

func TestExplicitOptInAddsAgyFlagOnly(t *testing.T) {
	out, danger := PrepareLaunchArgv([]string{"/opt/bin/agy", "-p", "hello"}, true)
	if !danger {
		t.Fatal("explicit opt-in was not danger mode")
	}
	if out[1] != "--dangerously-skip-permissions" {
		t.Fatalf("flag not inserted for agy: %q", out)
	}
	out, danger = PrepareLaunchArgv([]string{"echo", "hi"}, true)
	if !danger {
		t.Fatal("explicit opt-in should still request isolation for non-agy")
	}
	if strings.Contains(strings.Join(out, "\x00"), "--dangerously-skip-permissions") {
		t.Fatalf("non-agy command gained an agy flag: %q", out)
	}
}

func TestPersistedPassMarkerDoesNotAuthorize(t *testing.T) {
	err := Authorize(Authorization{PersistedPassJSON: []byte(`{"result":"PASS"}`)})
	if !errors.Is(err, ErrStaleAuthorization) {
		t.Fatalf("got %v", err)
	}
	_, err = Prepare(context.Background(), Request{
		Argv:              []string{"/bin/true"},
		ExplicitDanger:    true,
		PersistedPassJSON: []byte(`{"result":"PASS","profile_hash":"stale"}`),
	})
	if err == nil || !errors.Is(err, ErrRefused) {
		t.Fatalf("stale marker authorized a launch: %v", err)
	}
}

func TestDangerModeRefusedWhenBackendUnavailable(t *testing.T) {
	base := t.TempDir()
	started := filepath.Join(base, "child-started")
	secret := "s3cr3t-mqtt-password"
	audit := filepath.Join(base, "audit.jsonl")
	_, err := Prepare(context.Background(), Request{
		Argv:           []string{"/bin/touch", started, "--mqtt-password", secret, "mqtt://agent:s3cr3t-token@broker.example:8883"},
		ExplicitDanger: true,
		Backend:        UnavailableBackend(),
		AuditPath:      audit,
		BaseDir:        filepath.Join(base, "runs"),
		RegistryDir:    filepath.Join(base, "registry"),
	})
	if err == nil || !errors.Is(err, ErrRefused) {
		t.Fatalf("expected refuse, got %v", err)
	}
	if _, statErr := os.Stat(started); !os.IsNotExist(statErr) {
		t.Fatal("child started without isolation")
	}
	body, err := os.ReadFile(audit)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if strings.Contains(text, secret) || strings.Contains(text, "s3cr3t-token") {
		t.Fatalf("audit kept a secret:\n%s", text)
	}
	if !strings.Contains(text, `"controller_result":"refused"`) || !strings.Contains(text, `"child_exit":null`) {
		t.Fatalf("audit did not separate refusal from a child exit:\n%s", text)
	}
}

func TestHarnessFailsClosedWithoutBackend(t *testing.T) {
	base := t.TempDir()
	keep := filepath.Join(base, "keep")
	if err := os.WriteFile(keep, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", base)
	sentinel := filepath.Join(base, "sentinel")
	if err := os.WriteFile(sentinel, []byte("sentinel-v1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := NewHarness(Options{
		BaseDir:      filepath.Join(base, "runs"),
		RegistryDir:  filepath.Join(base, "registry"),
		AuditPath:    filepath.Join(base, "audit.jsonl"),
		RealHome:     base,
		SentinelPath: sentinel,
		Backend:      UnavailableBackend(),
	})
	if err == nil || !errors.Is(err, ErrRefused) {
		t.Fatalf("harness fell open: %v", err)
	}
	if body, rerr := os.ReadFile(keep); rerr != nil || string(body) != "keep" {
		t.Fatalf("harness touched HOME: %v %q", rerr, body)
	}
}

func TestRequireCredentialIPCFailsClosed(t *testing.T) {
	fix := newFixture(t)
	req := fix.req([]string{"/bin/true"}, true, "", "")
	req.RequireVerifiedCredentialIPC = true
	_, err := Prepare(context.Background(), req)
	if err == nil || !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "BLOCKED") {
		t.Fatalf("credential IPC was treated as verified: %v", err)
	}
}

func TestDarwinProfileIsNotAVerification(t *testing.T) {
	profile, err := DarwinSeatbeltProfile("/tmp/agentpulse-run-example")
	if err != nil {
		t.Fatal(err)
	}
	for _, needle := range []string{"deny file-write*", "subpath \"/tmp/agentpulse-run-example\"", "com.apple.securityd", "com.apple.SecurityServer"} {
		if !strings.Contains(profile, needle) {
			t.Fatalf("profile missing %q:\n%s", needle, profile)
		}
	}
	status := NativeVerification()
	for _, key := range []string{"darwin_sandbox_exec", "darwin_mach_securityd_keychain", "windows_write_isolation", "windows_reparse_junction", "credential_agent_ipc_proxy", "network_confinement"} {
		if status[key] != "BLOCKED" {
			t.Fatalf("%s=%s", key, status[key])
		}
	}
}

func TestProductionExecCommandStaysBehindSafetyBoundary(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		if strings.Contains(path, string(filepath.Separator)+"internal"+string(filepath.Separator)+"safety"+string(filepath.Separator)) {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(body), "exec.Command") {
			t.Errorf("exec.Command outside the safety boundary: %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestRedactArgv(t *testing.T) {
	out := RedactArgv([]string{
		"agy", "--mqtt-password", "s3cr3t-mqtt-password",
		"mqtt://agent:s3cr3t-token@broker.example:8883",
		"--token=raw-token",
	})
	text := strings.Join(out, " ")
	if strings.Contains(text, "s3cr3t") || strings.Contains(text, "raw-token") {
		t.Fatalf("redact failed: %q", out)
	}
	if !strings.Contains(text, "--mqtt-password") || !strings.Contains(text, "[REDACTED]") {
		t.Fatalf("redact dropped the flag name: %q", out)
	}
}

type fixture struct {
	base      string
	fakeHome  string
	sentinel  string
	runs      string
	registry  string
	audit     string
	protected []string
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	base := t.TempDir()
	fake := filepath.Join(base, "fake-home")
	protected := []string{
		".ssh/id_ed25519",
		".config/agentpulse/config.yaml",
		"Library/Keychains/login.keychain-db",
		"Library/CloudStorage/OneDrive-Personal/doc.txt",
		"Library/Mobile Documents/com~apple~CloudDocs/note.txt",
		"CloudStorage/iCloud/file.txt",
		"OneDrive/file.txt",
		"Desktop/file.txt",
		"Documents/file.txt",
	}
	for _, rel := range protected {
		p := filepath.Join(fake, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("sentinel-v1\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return fixture{
		base:      base,
		fakeHome:  fake,
		sentinel:  filepath.Join(fake, ".ssh", "id_ed25519"),
		runs:      filepath.Join(base, "runs"),
		registry:  filepath.Join(base, "registry"),
		audit:     filepath.Join(base, "audit.jsonl"),
		protected: protected,
	}
}

func (f fixture) req(argv []string, explicit bool, profileOverride, exeOverride string) Request {
	return Request{
		Argv:                argv,
		ExplicitDanger:      explicit,
		BaseDir:             f.runs,
		RegistryDir:         f.registry,
		AuditPath:           f.audit,
		RealHome:            f.fakeHome,
		SentinelPath:        f.sentinel,
		ProfileHashOverride: profileOverride,
		ExeHashOverride:     exeOverride,
	}
}
