package safety

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"testing"
)

const workerArg = "--agentpulse-safety-worker"

func TestMain(m *testing.M) {
	MaybeHandleInternal(os.Args)
	if len(os.Args) > 1 && os.Args[1] == workerArg {
		os.Exit(workerMain(os.Args[2:]))
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

func TestENOENTSentinelIsNotDenial(t *testing.T) {
	base := t.TempDir()
	started := filepath.Join(base, "child-started")
	missing := filepath.Join(base, "no-such-sentinel")
	_, err := Prepare(context.Background(), Request{
		Argv:           []string{"/bin/touch", started},
		ExplicitDanger: true,
		SentinelPath:   missing,
		BaseDir:        filepath.Join(base, "runs"),
		RegistryDir:    filepath.Join(base, "registry"),
		AuditPath:      filepath.Join(base, "audit.jsonl"),
	})
	if err == nil {
		t.Fatal("missing sentinel authorized a launch")
	}
	if !strings.Contains(err.Error(), "ENOENT") {
		t.Fatalf("error did not identify ENOENT: %v", err)
	}
	if _, statErr := os.Stat(started); !os.IsNotExist(statErr) {
		t.Fatal("child started after ENOENT sentinel")
	}
}

func TestProfileAndExeMismatchFailBeforeChild(t *testing.T) {
	for _, tc := range []struct {
		name    string
		profile string
		exe     string
	}{
		{"profile", "stale-profile-hash", ""},
		{"exe", "", "0000000000000000000000000000000000000000000000000000000000000000"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fix := newFixture(t)
			started := filepath.Join(fix.base, "child-started")
			p, err := Prepare(context.Background(), fix.req([]string{"/bin/touch", started}, true, tc.profile, tc.exe))
			if err != nil {
				t.Fatalf("prepare: %v", err)
			}
			err = p.Start()
			if err == nil || !errors.Is(err, ErrRefused) {
				t.Fatalf("mismatch started the child: %v", err)
			}
			if _, statErr := os.Stat(started); !os.IsNotExist(statErr) {
				t.Fatal("user command ran after identity mismatch")
			}
			if _, statErr := os.Stat(fix.sentinel); statErr != nil {
				t.Fatal("sentinel disappeared")
			}
		})
	}
}

func TestExplicitDangerWritesProjectAndCannotEscape(t *testing.T) {
	fix := newFixture(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	req := fix.req([]string{
		exe, workerArg,
		"--exit", "0",
		"--escape", fix.sentinel,
	}, true, "", "")
	req.RetainRoot = true
	req.Stdout = &stdout
	req.Stderr = &stderr
	t.Setenv("SSH_AUTH_SOCK", "/tmp/fake-ssh-agent.sock")
	t.Setenv("MQTT_PASSWORD", "s3cr3t-mqtt-password")
	p, err := Prepare(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(p.UserArgv, " "), "dangerously-skip-permissions") {
		t.Fatal("non-agy worker was given an agy danger flag")
	}
	if err := p.Start(); err != nil {
		skipPositiveLandlockIfRaceENOTSUP(t, err)
		t.Fatalf("start: %v\nstderr=%s", err, stderr.String())
	}
	out, err := p.Wait()
	if err != nil {
		t.Fatalf("wait: %v controller=%s stderr=%s", err, out.ControllerResult, stderr.String())
	}
	if out.ControllerResult != "isolated" || out.ChildExit == nil || *out.ChildExit != 0 {
		t.Fatalf("controller/child not separated: %+v", out)
	}
	project := filepath.Join(p.Work, "project", "result.txt")
	body, err := os.ReadFile(project)
	if err != nil || string(body) != "wrote-ok\n" {
		t.Fatalf("project write missing: %v %q", err, body)
	}
	status, err := os.ReadFile(filepath.Join(p.Work, "status.txt"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(status)
	if !strings.Contains(text, "no_new_privs=1") || !strings.Contains(text, "ssh_auth_sock_set=false") || !strings.Contains(text, "mqtt_password_set=false") {
		t.Fatalf("child environment was not confined:\n%s", text)
	}
	if !strings.Contains(text, "home="+p.Work[:len(p.Root)]) && !strings.HasPrefix(strings.TrimSpace(homeLine(text)), "home="+p.Root) {
		// HOME is the run home directory, a child of the run root.
		if !strings.Contains(text, "home="+filepath.Join(p.Root, "home")) {
			t.Fatalf("test HOME not inside run root:\n%s", text)
		}
	}
	escape, err := os.ReadFile(filepath.Join(p.Work, "escape.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(escape), "write=eperm") || !strings.Contains(string(escape), "remove=eperm") {
		t.Fatalf("escape was not denied: %s", escape)
	}
	for _, rel := range fix.protected {
		got, err := os.ReadFile(filepath.Join(fix.fakeHome, rel))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != "sentinel-v1\n" {
			t.Fatalf("%s changed: %q", rel, got)
		}
	}
	if _, err := os.Stat(fix.sentinel + ".child-created"); !os.IsNotExist(err) {
		t.Fatal("probe created a sibling next to the sentinel")
	}
	audit, err := os.ReadFile(fix.audit)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(audit), "s3cr3t-mqtt-password") {
		t.Fatalf("audit kept a secret:\n%s", audit)
	}
	if !strings.Contains(string(audit), `"credential_ipc":"BLOCKED"`) || !strings.Contains(string(audit), `"network":"BLOCKED_NOT_CONFINED"`) {
		t.Fatalf("audit overclaimed unverified surfaces:\n%s", audit)
	}
	cout, err := CleanupRun(p.RunID, CleanupOptions{RegistryDir: fix.registry, AuditPath: fix.audit, RealHome: fix.fakeHome})
	if err != nil {
		t.Fatal(err)
	}
	if cout.Result != "deleted" || len(cout.Targets) != 1 || cout.Targets[0] != p.Root {
		t.Fatalf("cleanup targets: %+v", cout)
	}
	if _, err := os.Stat(p.Root); !os.IsNotExist(err) {
		t.Fatal("run root still present")
	}
	if _, err := os.Stat(fix.sentinel); err != nil {
		t.Fatal("cleanup removed the sentinel")
	}
	if _, err := os.Stat(fix.audit); err != nil {
		t.Fatal("audit log was inside the cleaned tree")
	}
}

func TestAgyOptInFlagIsInsideVerifiedConfinement(t *testing.T) {
	fix := newFixture(t)
	script := filepath.Join(fix.base, "bin", "agy")
	if err := os.MkdirAll(filepath.Dir(script), 0700); err != nil {
		t.Fatal(err)
	}
	body := []byte("#!/bin/sh\nmkdir -p project\nprintf '%s\\n' \"$@\" > project/argv.txt\nprintf 'wrote-ok\\n' > project/result.txt\nexit 0\n")
	if err := os.WriteFile(script, body, 0700); err != nil {
		t.Fatal(err)
	}
	req := fix.req([]string{script, "-p", "hello"}, true, "", "")
	req.RetainRoot = true
	p, err := Prepare(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(p.UserArgv, " ")
	if !strings.Contains(joined, "--dangerously-skip-permissions") {
		t.Fatalf("explicit agy opt-in did not add the flag inside the confined argv: %q", p.UserArgv)
	}
	if filepath.Base(p.Cmd.Path) == "agy" {
		t.Fatal("agy was exec'd directly instead of the isolate helper")
	}
	if err := p.Start(); err != nil {
		skipPositiveLandlockIfRaceENOTSUP(t, err)
		t.Fatal(err)
	}
	out, err := p.Wait()
	if err != nil {
		t.Fatal(err)
	}
	if out.ChildExit == nil || *out.ChildExit != 0 || out.ControllerResult != "isolated" {
		t.Fatalf("outcome %+v", out)
	}
	got, err := os.ReadFile(filepath.Join(p.Work, "project", "result.txt"))
	if err != nil || string(got) != "wrote-ok\n" {
		t.Fatalf("project result: %v %q", err, got)
	}
	argv, err := os.ReadFile(filepath.Join(p.Work, "project", "argv.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(argv), "--dangerously-skip-permissions") {
		t.Fatalf("child did not observe the opt-in flag: %s", argv)
	}
	sent, err := os.ReadFile(fix.sentinel)
	if err != nil || string(sent) != "sentinel-v1\n" {
		t.Fatalf("sentinel changed: %v %q", err, sent)
	}
}

func TestChildExitIsNotFlattenedIntoControllerSuccess(t *testing.T) {
	fix := newFixture(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	req := fix.req([]string{exe, workerArg, "--exit", "7"}, true, "", "")
	req.RetainRoot = true
	p, err := Prepare(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Start(); err != nil {
		skipPositiveLandlockIfRaceENOTSUP(t, err)
		t.Fatal(err)
	}
	out, waitErr := p.Wait()
	if waitErr == nil {
		t.Fatal("child exit 7 was reported as controller success")
	}
	var ee *exec.ExitError
	if !errors.As(waitErr, &ee) || ee.ExitCode() != 7 {
		t.Fatalf("wait err = %v", waitErr)
	}
	if out.ControllerResult != "isolated" || out.ChildExit == nil || *out.ChildExit != 7 {
		t.Fatalf("flattened outcome: %+v", out)
	}
	audit, err := os.ReadFile(fix.audit)
	if err != nil {
		t.Fatal(err)
	}
	text := string(audit)
	if !strings.Contains(text, `"child_exit":7`) || !strings.Contains(text, `"controller_result":"isolated"`) {
		t.Fatalf("audit flattened exits:\n%s", text)
	}
	if strings.Contains(text, `"result":"PASS"`) || strings.Contains(text, `"controller_result":"PASS"`) {
		t.Fatalf("audit used a PASS marker:\n%s", text)
	}
}

// TestRaceLandlockENOTSUPDoesNotStartChild locks the fail-closed side of
// the race/landlock incompatibility. Positive confinement tests skip only
// after apply reports this ENOTSUP; this test keeps asserting that the user
// command was not started.
func TestRaceLandlockENOTSUPDoesNotStartChild(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("landlock apply is linux-only")
	}
	if !builtWithRace() {
		t.Skip("non-race builds apply landlock; positive confinement tests verify that path. -race is what makes AllThreadsSyscall return ENOTSUP")
	}
	fix := newFixture(t)
	started := filepath.Join(fix.base, "child-started")
	p, err := Prepare(context.Background(), fix.req([]string{"/bin/touch", started}, true, "", ""))
	if err != nil {
		t.Fatal(err)
	}
	err = p.Start()
	if err == nil || !errors.Is(err, ErrRefused) {
		t.Fatalf("race ENOTSUP must refuse the child, got %v", err)
	}
	if !strings.Contains(err.Error(), landlockNoNewPrivsENOTSUPReason) {
		t.Fatalf("race landlock failure was not AllThreadsSyscall ENOTSUP on no_new_privs: %v", err)
	}
	if _, statErr := os.Stat(started); !os.IsNotExist(statErr) {
		t.Fatal("user command started after landlock apply failed")
	}
	body, rerr := os.ReadFile(fix.audit)
	if rerr != nil {
		t.Fatal(rerr)
	}
	text := string(body)
	if !strings.Contains(text, `"controller_result":"refused"`) || !strings.Contains(text, `"child_exit":null`) {
		t.Fatalf("audit did not record fail-closed refusal:\n%s", text)
	}
	if strings.Contains(text, `"isolation_applied":true`) {
		t.Fatalf("audit claimed isolation after ENOTSUP:\n%s", text)
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

func TestCleanupRejectsUnsafeTargetsAndMissingOwnership(t *testing.T) {
	fix := newFixture(t)
	exeHash, err := exeSHA256()
	if err != nil {
		t.Fatal(err)
	}
	rec, err := createRunRoot(createOpts{
		BaseDir:     fix.runs,
		RegistryDir: fix.registry,
		RealHome:    fix.fakeHome,
		Sentinel:    fix.sentinel,
		ExeHash:     exeHash,
	})
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(fix.base, "outside-sentinel")
	if err := os.WriteFile(outside, []byte("safe"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(rec.Root, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		path string
	}{
		{"empty", ""},
		{"slash", "/"},
		{"dotdot", filepath.Join(rec.Root, "..")},
		{"parent", filepath.Dir(rec.Root)},
		{"home", fix.fakeHome},
		{"symlink", link},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tampered := rec
			tampered.Root = tc.path
			out, err := deleteOwned(tampered, []string{fix.fakeHome, RealHome()})
			if err == nil || out.Result == "deleted" {
				t.Fatalf("deleted unsafe target %q: %+v", tc.path, out)
			}
		})
	}
	if body, err := os.ReadFile(outside); err != nil || string(body) != "safe" {
		t.Fatalf("outside sentinel damaged: %v %q", err, body)
	}
	if body, err := os.ReadFile(fix.sentinel); err != nil || string(body) != "sentinel-v1\n" {
		t.Fatalf("fixture sentinel damaged: %v %q", err, body)
	}

	t.Run("missing marker", func(t *testing.T) {
		if err := os.Remove(filepath.Join(rec.Root, markerName)); err != nil {
			t.Fatal(err)
		}
		out, err := CleanupRun(rec.RunID, CleanupOptions{RegistryDir: fix.registry, AuditPath: fix.audit, RealHome: fix.fakeHome})
		if err == nil || out.Result != "BLOCKED" {
			t.Fatalf("missing marker deleted the root: %+v %v", out, err)
		}
		if _, err := os.Stat(rec.Root); err != nil {
			t.Fatal("root was not retained")
		}
	})
}

func TestCleanupSymlinkSubstitutionDoesNotEscape(t *testing.T) {
	fix := newFixture(t)
	exeHash, err := exeSHA256()
	if err != nil {
		t.Fatal(err)
	}
	rec, err := createRunRoot(createOpts{
		BaseDir: fix.runs, RegistryDir: fix.registry, RealHome: fix.fakeHome,
		Sentinel: fix.sentinel, ExeHash: exeHash,
	})
	if err != nil {
		t.Fatal(err)
	}
	renamed := rec.Root + ".kept"
	if err := os.Rename(rec.Root, renamed); err != nil {
		t.Fatal(err)
	}
	decoy := filepath.Join(fix.base, "decoy")
	if err := os.MkdirAll(decoy, 0700); err != nil {
		t.Fatal(err)
	}
	decoyFile := filepath.Join(decoy, "SENTINEL")
	if err := os.WriteFile(decoyFile, []byte("safe"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(decoy, rec.Root); err != nil {
		t.Fatal(err)
	}
	out, err := CleanupRun(rec.RunID, CleanupOptions{RegistryDir: fix.registry, AuditPath: fix.audit, RealHome: fix.fakeHome})
	if err == nil || out.Result == "deleted" {
		t.Fatalf("followed substituted symlink: %+v %v", out, err)
	}
	if body, rerr := os.ReadFile(decoyFile); rerr != nil || string(body) != "safe" {
		t.Fatalf("decoy damaged: %v %q", rerr, body)
	}
	if _, rerr := os.Stat(renamed); rerr != nil {
		t.Fatal("original root was deleted under its renamed path")
	}
}

func TestCleanupIgnoresHomeEnv(t *testing.T) {
	fix := newFixture(t)
	homeDir := filepath.Join(fix.base, "mutable-home")
	if err := os.MkdirAll(homeDir, 0700); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(homeDir, "keep")
	if err := os.WriteFile(keep, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", homeDir)
	exeHash, err := exeSHA256()
	if err != nil {
		t.Fatal(err)
	}
	rec, err := createRunRoot(createOpts{
		BaseDir: fix.runs, RegistryDir: fix.registry, RealHome: fix.fakeHome,
		Sentinel: fix.sentinel, ExeHash: exeHash,
	})
	if err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(rec.Root, "inside")
	if err := os.WriteFile(inside, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	out, err := CleanupRun(rec.RunID, CleanupOptions{RegistryDir: fix.registry, AuditPath: fix.audit, RealHome: fix.fakeHome})
	if err != nil || out.Result != "deleted" {
		t.Fatalf("owned cleanup failed: %+v %v", out, err)
	}
	if _, err := os.Stat(rec.Root); !os.IsNotExist(err) {
		t.Fatal("owned root remained")
	}
	if body, err := os.ReadFile(keep); err != nil || string(body) != "keep" {
		t.Fatalf("HOME was used as a delete root: %v %q", err, body)
	}
	if strings.Contains(strings.Join(out.Targets, " "), homeDir) {
		t.Fatal("cleanup target was derived from HOME")
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

// skipPositiveLandlockIfRaceENOTSUP skips a test that needs a successful
// landlock apply when this binary is race-instrumented and the apply just
// failed because AllThreadsSyscall(PR_SET_NO_NEW_PRIVS) returned ENOTSUP.
//
// Race instrumentation is incompatible with landlock on this Linux: the
// race runtime links cgo, and Go returns ENOTSUP from AllThreadsSyscall
// without issuing the prctl. The production non-race path is verified by
// running these tests without -race. -race must not require landlock
// success. A non-race ENOTSUP is a real failure and is not skipped.
// testing.Short is not a substitute: the decision is race-aware and uses
// the apply-time error. Fail-closed tests that expect a refusal stay in
// place; this helper does not turn an isolation failure into a bare run.
func skipPositiveLandlockIfRaceENOTSUP(t *testing.T, err error) {
	t.Helper()
	if err == nil || !builtWithRace() {
		return
	}
	if !strings.Contains(err.Error(), landlockNoNewPrivsENOTSUPReason) {
		return
	}
	t.Skip("race instrumentation is incompatible with landlock on this Linux: AllThreadsSyscall(PR_SET_NO_NEW_PRIVS) returns ENOTSUP, so landlock cannot apply. The child was not started (fail-closed). The production non-race path is verified without -race; -race must not require landlock success")
}

// builtWithRace reports whether this test binary was built with -race.
// It reads the build setting Go records for the race detector. It does not
// use testing.Short, and it does not treat a plain cgo or ENOTSUP result as
// a race build.
func builtWithRace() bool {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return false
	}
	for _, s := range info.Settings {
		if s.Key == "-race" && s.Value == "true" {
			return true
		}
	}
	return false
}

func homeLine(status string) string {
	for _, line := range strings.Split(status, "\n") {
		if strings.HasPrefix(line, "home=") {
			return line
		}
	}
	return ""
}

func workerMain(args []string) int {
	exitCode := 0
	projectRel := filepath.Join("project", "result.txt")
	var escape string
	for i := 0; i < len(args); i++ {
		if i+1 >= len(args) {
			return 2
		}
		switch args[i] {
		case "--project-out":
			i++
			projectRel = args[i]
		case "--escape":
			i++
			escape = args[i]
		case "--exit":
			i++
			n, err := strconv.Atoi(args[i])
			if err != nil {
				return 2
			}
			exitCode = n
		default:
			return 2
		}
	}
	if err := os.MkdirAll(filepath.Dir(projectRel), 0700); err != nil {
		fmt.Fprintf(os.Stderr, "mkdir %v\n", err)
		return 51
	}
	if err := os.WriteFile(projectRel, []byte("wrote-ok\n"), 0600); err != nil {
		fmt.Fprintf(os.Stderr, "write project %v\n", err)
		return 51
	}
	noNew := "0"
	if body, err := os.ReadFile("/proc/self/status"); err == nil {
		for _, line := range strings.Split(string(body), "\n") {
			if strings.HasPrefix(line, "NoNewPrivs:") {
				noNew = strings.TrimSpace(strings.TrimPrefix(line, "NoNewPrivs:"))
			}
		}
	}
	var status strings.Builder
	fmt.Fprintf(&status, "no_new_privs=%s\n", noNew)
	fmt.Fprintf(&status, "home=%s\n", os.Getenv("HOME"))
	if _, ok := os.LookupEnv("SSH_AUTH_SOCK"); ok {
		status.WriteString("ssh_auth_sock_set=true\n")
	} else {
		status.WriteString("ssh_auth_sock_set=false\n")
	}
	if os.Getenv("MQTT_PASSWORD") != "" {
		status.WriteString("mqtt_password_set=true\n")
	} else {
		status.WriteString("mqtt_password_set=false\n")
	}
	if err := os.WriteFile("status.txt", []byte(status.String()), 0600); err != nil {
		return 52
	}
	if escape != "" {
		writeErr := os.WriteFile(escape, []byte("hacked"), 0600)
		removeErr := os.Remove(escape)
		msg := fmt.Sprintf("write=%s\nremove=%s\n", errnoName(writeErr), errnoName(removeErr))
		if err := os.WriteFile("escape.txt", []byte(msg), 0600); err != nil {
			return 53
		}
	}
	return exitCode
}

func errnoName(err error) string {
	if err == nil {
		return "nil"
	}
	if errors.Is(err, os.ErrNotExist) {
		return "enoent"
	}
	if errors.Is(err, os.ErrPermission) {
		return "eperm"
	}
	return "other:" + err.Error()
}
