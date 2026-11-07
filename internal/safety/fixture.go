package safety

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// RunNativeFixture runs one confined child that writes a project file and
// tries to modify synthetic sentinels. It does not launch agy or any other
// agent, and it does not read real user credentials.
//
// On Windows this is the ops acceptance entrypoint. A Linux or cross-compiled
// build does not mark that acceptance passed. Darwin stays BLOCKED until a
// native seatbelt probe exists.
func RunNativeFixture(ctx context.Context) (string, error) {
	backend, err := SelectBackend()
	name := "unavailable"
	if backend != nil {
		name = backend.Name()
	}
	var b strings.Builder
	fmt.Fprintf(&b, "backend=%s\n", name)
	for k, v := range NativeVerification() {
		fmt.Fprintf(&b, "catalog_%s=%s\n", k, v)
	}
	fmt.Fprintf(&b, "crosscompile_is_not_windows_pass=true\n")
	fmt.Fprintf(&b, "launches_agent=false\n")
	if err != nil {
		fmt.Fprintf(&b, "result=BLOCKED\nreason=%s\n", err.Error())
		return b.String(), fmt.Errorf("%w: %v", ErrRefused, err)
	}
	base, err := os.MkdirTemp("", "agentpulse-fixture-")
	if err != nil {
		return b.String(), err
	}
	fake := filepath.Join(base, "fake-home")
	rels := []string{
		filepath.Join(".ssh", "id_ed25519"),
		filepath.Join(".config", "agentpulse", "config.yaml"),
		filepath.Join("Library", "Keychains", "login.keychain-db"),
		filepath.Join("Library", "CloudStorage", "OneDrive-Personal", "doc.txt"),
		filepath.Join("OneDrive", "file.txt"),
		filepath.Join("Desktop", "file.txt"),
		filepath.Join("Documents", "file.txt"),
	}
	for _, rel := range rels {
		p := filepath.Join(fake, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			return b.String(), err
		}
		if err := os.WriteFile(p, []byte("sentinel-v1\n"), 0600); err != nil {
			return b.String(), err
		}
	}
	sentinel := filepath.Join(fake, ".ssh", "id_ed25519")
	audit := filepath.Join(base, "audit.jsonl")
	exe, err := os.Executable()
	if err != nil {
		return b.String(), err
	}
	hash, _ := exeSHA256()
	fmt.Fprintf(&b, "exe_sha256=%s\n", hash)
	req := Request{
		Argv:           []string{exe, FixtureWorkerArg, "--escape", sentinel, "--exit", "0", "--mqtt-password", "s3cr3t-mqtt-password"},
		ExplicitDanger: true,
		BaseDir:        filepath.Join(base, "runs"),
		RegistryDir:    filepath.Join(base, "registry"),
		AuditPath:      audit,
		RealHome:       fake,
		SentinelPath:   sentinel,
		RetainRoot:     true,
	}
	// The worker does not understand --mqtt-password. Keep the secret out of
	// its argv; the audit redact test covers argv. The env var is the leak check.
	req.Argv = []string{exe, FixtureWorkerArg, "--escape", sentinel, "--exit", "0"}
	prepared, err := Prepare(ctx, req)
	if err != nil {
		fmt.Fprintf(&b, "result=BLOCKED\nreason=%s\n", err.Error())
		return b.String(), err
	}
	fmt.Fprintf(&b, "profile_hash=%s\n", prepared.audit.ProfileHash)
	fmt.Fprintf(&b, "run_root=%s\n", prepared.Root)
	if err := prepared.Start(); err != nil {
		fmt.Fprintf(&b, "result=BLOCKED\nreason=%s\n", err.Error())
		return b.String(), err
	}
	out, waitErr := prepared.Wait()
	project, rerr := os.ReadFile(filepath.Join(prepared.Work, "project", "result.txt"))
	escape, _ := os.ReadFile(filepath.Join(prepared.Work, "escape.txt"))
	status, _ := os.ReadFile(filepath.Join(prepared.Work, "status.txt"))
	sent, serr := os.ReadFile(sentinel)
	fmt.Fprintf(&b, "child_status=%s\n", strings.TrimSpace(string(status)))
	fmt.Fprintf(&b, "child_exit=%v\ncontroller_result=%s\n", out.ChildExit, out.ControllerResult)
	if runtime.GOOS == "windows" && !strings.Contains(string(status), "appcontainer=1") {
		fmt.Fprintf(&b, "result=FAIL\nappcontainer_not_inherited=true\n")
		return b.String(), fmt.Errorf("descendant was not in the AppContainer")
	}
	if waitErr != nil || rerr != nil || string(project) != "wrote-ok\n" || serr != nil || string(sent) != "sentinel-v1\n" || !strings.Contains(string(escape), "write=eperm") {
		fmt.Fprintf(&b, "result=FAIL\nwait=%v\nproject=%q\nescape=%s\nsentinel=%q\n", waitErr, project, escape, sent)
		return b.String(), fmt.Errorf("fixture failed: wait=%v project=%v sentinel=%v", waitErr, rerr, serr)
	}
	for _, rel := range rels {
		body, err := os.ReadFile(filepath.Join(fake, rel))
		if err != nil || string(body) != "sentinel-v1\n" {
			fmt.Fprintf(&b, "result=FAIL\nprotected=%s\n", rel)
			return b.String(), fmt.Errorf("protected file changed: %s", rel)
		}
	}
	cout, cerr := CleanupRun(prepared.RunID, CleanupOptions{RegistryDir: req.RegistryDir, AuditPath: audit, RealHome: fake})
	auditBody, _ := os.ReadFile(audit)
	if strings.Contains(string(auditBody), "s3cr3t-mqtt-password") {
		fmt.Fprintf(&b, "result=FAIL\naudit_leaked_secret=true\n")
		return b.String(), fmt.Errorf("audit leaked a secret")
	}
	if cerr != nil || cout.Result != "deleted" {
		fmt.Fprintf(&b, "result=FAIL\ncleanup=%s %v\n", cout.Result, cerr)
		return b.String(), fmt.Errorf("cleanup: %v %v", cout.Result, cerr)
	}
	if _, err := os.Stat(sentinel); err != nil {
		fmt.Fprintf(&b, "result=FAIL\nsentinel_removed=true\n")
		return b.String(), fmt.Errorf("cleanup removed the sentinel")
	}
	if _, err := os.Stat(audit); err != nil {
		fmt.Fprintf(&b, "result=FAIL\naudit_removed=true\n")
		return b.String(), fmt.Errorf("audit was inside the cleaned root")
	}
	fmt.Fprintf(&b, "result=PASS\n")
	fmt.Fprintf(&b, "native_windows_catalog=%s\n", NativeVerification()["windows_appcontainer_native_fixture"])
	return b.String(), nil
}
