package safety

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDarwinRefusalKeepsProfileStageAndStderr(t *testing.T) {
	profile := "(version 1)\n(deny default)\n(deny mach-lookup (global-name \"com.apple.securityd\"))\n(deny file-write* (subpath \"/Library/Keychains\"))\n"
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	auditPath := filepath.Join(t.TempDir(), "audit.jsonl")
	p := &Prepared{
		confirmR:  r,
		auditFile: auditPath,
		waitFn:    func() error { return exitStatus{code: 65} },
	}
	rememberSeatbeltProfile(p, profile)
	p.noteChildStderr([]byte("sandbox_init: profile rejected\nmqtt-password=s3cr3t-mqtt-password\n"))
	p.noteChildStderr(bytesRepeat('x', childStderrLimit))
	err = p.readConfirm()
	if err == nil || !strings.Contains(err.Error(), "EOF") {
		t.Fatalf("EOF confirm error = %v", err)
	}
	if p.audit.IsolationApplied || p.audit.ChildExit != nil {
		t.Fatalf("applied=%v child=%v", p.audit.IsolationApplied, p.audit.ChildExit)
	}
	body, rerr := os.ReadFile(auditPath)
	if rerr != nil {
		t.Fatalf("audit: %v", rerr)
	}
	text := string(body)
	if !strings.Contains(text, `"launch_stage":"confirm_pipe_eof"`) {
		t.Fatalf("audit missing confirm stage: %s", text)
	}
	if !strings.Contains(text, `"helper_exit":65`) {
		t.Fatalf("audit missing observed exit 65: %s", text)
	}
	if !strings.Contains(text, `(deny mach-lookup (global-name \"com.apple.securityd\"))`) && !strings.Contains(text, `(deny mach-lookup (global-name "com.apple.securityd"))`) {
		t.Fatalf("audit missing seatbelt profile: %s", text)
	}
	if !strings.Contains(text, "sandbox_init: profile rejected") {
		t.Fatalf("audit missing bounded stderr: %s", text)
	}
	if strings.Contains(text, "s3cr3t-mqtt-password") {
		t.Fatal("audit kept a password")
	}
	if strings.Count(text, "x") > childStderrLimit {
		t.Fatalf("stderr cap exceeded")
	}
	if p.audit.ControllerResult != "refused" {
		t.Fatalf("controller = %s", p.audit.ControllerResult)
	}
}

func TestChildStderrCaptureIsBoundedAndJoined(t *testing.T) {
	p := &Prepared{Cmd: &exec.Cmd{}}
	p.beginChildStderrCapture()
	if p.stderrW == nil {
		t.Fatal("stderr capture did not attach")
	}
	if _, err := p.stderrW.Write([]byte("sandbox_init: profile rejected\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	p.finishChildStderr()
	if !strings.Contains(p.childStderr, "sandbox_init: profile rejected") {
		t.Fatalf("captured %q", p.childStderr)
	}
	p.noteChildStderr(bytesRepeat('y', childStderrLimit))
	if len(p.childStderr) > childStderrLimit {
		t.Fatalf("stderr len %d", len(p.childStderr))
	}
}

func bytesRepeat(b byte, n int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = b
	}
	return out
}
