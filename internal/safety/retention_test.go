package safety

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRetainRunEvidenceKeepsRawProbeFinalArgvAndProfile(t *testing.T) {
	parent := t.TempDir()
	runRoot := filepath.Join(parent, "runs", "run1")
	if err := os.MkdirAll(runRoot, 0700); err != nil {
		t.Fatal(err)
	}
	auditPath := filepath.Join(parent, "audit.jsonl")
	probe := "path=/private/tmp/fixture/work/dir-link-escape/escape-attempt.txt\nwrite=eperm\nremove=enoent\nread=enoent\npath=/private/tmp/fixture/work/dir-link-escape/target.txt\nwrite=eperm\nremove=eperm\nread=eperm\n"
	profile := "(version 1)\n(deny default)\n(deny mach-lookup (global-name \"com.apple.securityd\"))\n"
	argv := []string{"/private/tmp/bin/tool", "--mqtt-password", "s3cr3t-mqtt-password", "--escape", "/private/tmp/fixture/work/dir-link-escape/target.txt"}
	ev, err := retainRunEvidence(evidenceRetention{
		Parent: parent, RunRoot: runRoot, AuditPath: auditPath,
		WorkerProbe: probe, FinalArgv: argv, Profile: profile, RunID: "run1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if ev.ControllerResult != "retained" {
		t.Fatalf("result %s %s", ev.ControllerResult, ev.CleanupReason)
	}
	sum := sha256.Sum256([]byte(profile))
	if ev.SeatbeltProfileSHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("sha256 = %s", ev.SeatbeltProfileSHA256)
	}
	if ev.ProfileHash != "" {
		t.Fatal("retention used the run-identity profile_hash")
	}
	if err := AppendAudit(auditPath, ev); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(auditPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, want := range []string{"write=eperm", "read=enoent", "dir-link-escape/target.txt", "(deny default)", ev.SeatbeltProfileSHA256, "[REDACTED]"} {
		if !strings.Contains(text, want) {
			t.Fatalf("audit missing %q\n%s", want, text)
		}
	}
	if strings.Contains(text, "s3cr3t-mqtt-password") {
		t.Fatalf("audit kept a secret\n%s", text)
	}
	if _, err := os.Stat(filepath.Join(runRoot, "audit.jsonl")); !os.IsNotExist(err) {
		t.Fatal("retention wrote inside the run root")
	}
}

func TestRetainRunEvidenceMissingFieldIsBlockedWithoutInventedBytes(t *testing.T) {
	parent := t.TempDir()
	runRoot := filepath.Join(parent, "runs", "run1")
	if err := os.MkdirAll(runRoot, 0700); err != nil {
		t.Fatal(err)
	}
	auditPath := filepath.Join(parent, "audit.jsonl")
	ev, err := retainRunEvidence(evidenceRetention{
		Parent: parent, RunRoot: runRoot, AuditPath: auditPath,
		FinalArgv: []string{"/bin/tool"}, Profile: "(deny default)\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	if ev.ControllerResult != "BLOCKED" || !strings.Contains(ev.CleanupReason, "worker_probe") {
		t.Fatalf("got %s %s", ev.ControllerResult, ev.CleanupReason)
	}
	if ev.WorkerProbe != "" || ev.SeatbeltProfile != "" || ev.SeatbeltProfileSHA256 != "" || len(ev.FinalArgv) != 0 {
		t.Fatalf("missing field was filled: %+v", ev)
	}
	if strings.Contains(ev.CleanupReason, "10d50e64") || strings.Contains(ev.CleanupReason, "aa794ed") {
		t.Fatal("blocked record invented a previous fixture")
	}
}

func TestRetainRunEvidenceRejectsPathEscape(t *testing.T) {
	parent := t.TempDir()
	runRoot := filepath.Join(parent, "runs", "run1")
	if err := os.MkdirAll(runRoot, 0700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(filepath.Dir(parent), "outside-audit.jsonl")
	escaped := parent + "/../" + filepath.Base(outside)
	_, err := retainRunEvidence(evidenceRetention{
		Parent: parent, RunRoot: runRoot, AuditPath: escaped,
		WorkerProbe: "write=eperm\nremove=eperm\nread=eperm\n",
		FinalArgv:   []string{"/bin/tool"}, Profile: "(deny default)\n",
	})
	if err == nil {
		t.Fatal("path escape was accepted")
	}
	if _, statErr := os.Stat(outside); !os.IsNotExist(statErr) {
		t.Fatal("escape wrote outside the exclusive parent")
	}
	_, err = retainRunEvidence(evidenceRetention{
		Parent: parent, RunRoot: runRoot, AuditPath: filepath.Join(runRoot, "audit.jsonl"),
		WorkerProbe: "write=eperm\nremove=eperm\nread=eperm\n",
		FinalArgv:   []string{"/bin/tool"}, Profile: "(deny default)\n",
	})
	if err == nil {
		t.Fatal("audit inside the run root was accepted")
	}
}

func TestRetainRunEvidenceRejectsSurvivingPasswordAssignment(t *testing.T) {
	parent := t.TempDir()
	runRoot := filepath.Join(parent, "runs", "run1")
	if err := os.MkdirAll(runRoot, 0700); err != nil {
		t.Fatal(err)
	}
	ev, err := retainRunEvidence(evidenceRetention{
		Parent: parent, RunRoot: runRoot, AuditPath: filepath.Join(parent, "audit.jsonl"),
		WorkerProbe: "write=eperm\nremove=eperm\nread=eperm\npassword=s3cr3t-mqtt-password\n",
		FinalArgv:   []string{"/bin/tool", "--mqtt-password", "s3cr3t-mqtt-password"},
		Profile:     "(deny default)\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(ev.WorkerProbe, "s3cr3t") || strings.Contains(strings.Join(ev.FinalArgv, " "), "s3cr3t") {
		t.Fatalf("secret survived redaction: probe=%q argv=%q", ev.WorkerProbe, ev.FinalArgv)
	}
	if strings.Contains(strings.ToLower(ev.WorkerProbe+strings.Join(ev.FinalArgv, "\n")), "password=") {
		t.Fatalf("password assignment survived: probe=%q argv=%q", ev.WorkerProbe, ev.FinalArgv)
	}
	if err := AppendAudit(filepath.Join(parent, "audit.jsonl"), ev); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(parent, "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "s3cr3t") || strings.Contains(strings.ToLower(string(body)), "password=") {
		t.Fatalf("audit kept a secret or assignment\n%s", body)
	}
}

func TestArgvAfterSeparatorIncludesLaterEscape(t *testing.T) {
	args := []string{"/usr/bin/sandbox-exec", "-p", "(deny default)", "/bin/tool", "--agentpulse-internal-isolate", "--", "/bin/tool", "--exit", "0", "--escape", "/tmp/a", "--escape", "/tmp/b"}
	got := argvAfterSeparator(args)
	if len(got) != 7 || got[4] != "/tmp/a" || got[6] != "/tmp/b" {
		t.Fatalf("argv = %q", got)
	}
	if argvAfterSeparator([]string{"/bin/tool"}) != nil {
		t.Fatal("missing separator returned arguments")
	}
}
