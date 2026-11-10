package safety

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAcceptCreatedProfileRejectsCollisionAndOtherErrors(t *testing.T) {
	if err := acceptCreatedProfile(0); err != nil {
		t.Fatal(err)
	}
	err := acceptCreatedProfile(0x800700B7)
	if err == nil || !errors.Is(err, err) {
		t.Fatal(err)
	}
	if !errors.Is(err, errors.New("x")) && err.Error() == "" {
		t.Fatal(err)
	}
	if !containsAll(err.Error(), "already exists", "refusing to adopt") {
		t.Fatalf("collision: %v", err)
	}
	err = acceptCreatedProfile(0x80070005)
	if err == nil || !containsAll(err.Error(), "HRESULT", "0x80070005") {
		t.Fatalf("other hr: %v", err)
	}
}

func TestDevicePrefixBreaksRawWithinButNotCanonical(t *testing.T) {
	root := `C:\Temp\agentpulse-owned`
	child := `\\?\C:\Temp\agentpulse-owned\fake-home\.ssh\id_ed25519`
	if strings.HasPrefix(strings.ToLower(child), strings.ToLower(root)) {
		t.Fatal("raw prefix unexpectedly matched a device path")
	}
	if !withinCanonical(root, child) {
		t.Fatal("canonical compare rejected the same file")
	}
}

func TestCreationRecordIsRequiredForProtectedSentinel(t *testing.T) {
	root := `C:\Temp\agentpulse-owned`
	sentinel := `\\?\C:\Temp\agentpulse-owned\fake-home\.ssh\id_ed25519`
	prefixedOnly := CreationRecord{CanonicalRoot: canonicalWinPath(root), Nonce: "n", Volume: 1, FileIndex: 2, Created: false}
	if err := bindSentinel(prefixedOnly, sentinel, 9, 9); err == nil {
		t.Fatal("a name without Created was accepted")
	}
	forged := prefixedOnly
	forged.Created = true
	if err := bindSentinel(forged, sentinel, 9, 9); err == nil {
		t.Fatal("a caller-filled Created flag was accepted")
	}
	good, err := mintCreation(CreationRecord{CanonicalRoot: canonicalWinPath(root), Nonce: "n", Volume: 1, FileIndex: 2})
	if err != nil {
		t.Fatal(err)
	}
	if err := bindSentinel(good, sentinel, 9, 9); err != nil {
		t.Fatal(err)
	}
	if err := bindSentinel(good, `C:\Users\someone\.ssh\id_ed25519`, 3, 4); err == nil {
		t.Fatal("protected path outside the record was accepted")
	}
}

func TestMetadataDeniedKeepsParentBinding(t *testing.T) {
	parent := FileBinding{Path: `\\?\C:\Temp\agentpulse-owned\fake-home\.ssh\id`, Volume: 7, Index: 11}
	got, err := hashSentinelBinding(false, FileBinding{}, parent)
	if err != nil || got != parent {
		t.Fatalf("denied metadata dropped the parent binding: %+v %v", got, err)
	}
	if _, err := hashSentinelBinding(false, FileBinding{}, FileBinding{}); err == nil {
		t.Fatal("zeros were accepted as a sentinel binding")
	}
	observed := FileBinding{Path: `C:\Temp\agentpulse-owned\fake-home\.ssh\id`, Volume: 7, Index: 11}
	got, err = hashSentinelBinding(true, observed, parent)
	if err != nil || got.Path != parent.Path || got.Volume != parent.Volume || got.Index != parent.Index {
		t.Fatalf("observed spelling replaced the parent binding: %+v %v", got, err)
	}
}

func TestNtPendingIsNotACompletedRead(t *testing.T) {
	if _, err := ntQueryResult(statusPending, 12, 100); err == nil {
		t.Fatal("STATUS_PENDING was treated as a completed buffer")
	}
	n, err := ntQueryResult(0, 12, 100)
	if err != nil || n != 12 {
		t.Fatalf("success: %d %v", n, err)
	}
	if _, err := ntQueryResult(0, 101, 100); err == nil {
		t.Fatal("information length past the buffer was accepted")
	}
	if _, err := ntQueryResult(statusNoMoreFilesNT, 0, 100); !errors.Is(err, errNTNoMore) {
		t.Fatal(err)
	}
	if err := ntCreateStatus(statusNoMoreFilesNT); err == nil {
		t.Fatal("directory end status was accepted as a created file")
	}
	if err := ntCreateStatus(statusPending); err == nil {
		t.Fatal("pending create was accepted")
	}
	if err := ntCreateStatus(0); err != nil {
		t.Fatal(err)
	}
}

func TestMountPointReparsePrintNameFits(t *testing.T) {
	subst := []uint16{'\\', '?', '?', '\\', 'C', ':', '\\', 'o', 'u', 't'}
	printName := []uint16{'C', ':', '\\', 'o', 'u', 't'}
	buf, err := mountPointReparse(append(subst, 0), append(printName, 0))
	if err != nil {
		t.Fatal(err)
	}
	printOff := int(buf[12]) | int(buf[13])<<8
	printLen := int(buf[14]) | int(buf[15])<<8
	if 16+printOff+printLen > len(buf) {
		t.Fatalf("print name ends at %d, buffer is %d", 16+printOff+printLen, len(buf))
	}
	if printOff != (len(subst)+1)*2 {
		t.Fatalf("print offset %d", printOff)
	}
}

func TestCleanupBlockedUntilDescendantsQuit(t *testing.T) {
	if err := cleanupAllowed(Lifecycle{HelperExited: true, CancelJoined: true, HandlesReleased: true, ActiveProcesses: 1}); err == nil {
		t.Fatal("cleanup allowed while a descendant was active")
	}
	if err := cleanupAllowed(Lifecycle{HelperExited: true, HandlesReleased: true, ActiveProcesses: 0}); err == nil {
		t.Fatal("cleanup allowed before the cancel goroutine joined")
	}
	if err := cleanupAllowed(Lifecycle{HelperExited: true, CancelJoined: true, HandlesReleased: true, ActiveProcesses: 0}); err != nil {
		t.Fatal(err)
	}
}

func TestProfileRetryAfterRootRemoved(t *testing.T) {
	man := RunManifest{RunID: strings.Repeat("ab", 16), Nonce: "n", ProfileName: "ap" + strings.Repeat("ab", 16), ProfileCreated: true, Phase: "root_deleted"}
	plan, err := cleanupPlan(true, false, man)
	if err == nil || plan != "" {
		t.Fatal("a missing root was allowed to select profile deletion")
	}
	out, rerr := manifestRecovery(man)
	if rerr == nil || out.Result != "BLOCKED" || !strings.Contains(out.Reason, man.ProfileName) {
		t.Fatalf("recovery: %+v %v", out, rerr)
	}
}

func TestExecutableCopyMustMatchSourceHash(t *testing.T) {
	same := strings.Repeat("ab", 32)
	if err := agreeHashes(same, same); err != nil {
		t.Fatal(err)
	}
	if err := agreeHashes(same, strings.Repeat("cd", 32)); err == nil {
		t.Fatal("different copy hash was accepted")
	}
	if err := agreeHashes(strings.Repeat("AB", 32), strings.Repeat("AB", 32)); err == nil {
		t.Fatal("uppercase hash was accepted")
	}
}

func TestEscapeDenialsArePerPath(t *testing.T) {
	text := "path=C:\\a\\.ssh\\id\nwrite=eperm\nremove=eperm\npath=C:\\a\\Documents\\file.txt\nwrite=eperm\nremove=nil\n"
	if err := escapeDenials(text, []string{`C:\a\.ssh\id`}); err != nil {
		t.Fatal(err)
	}
	if err := escapeDenials(text, []string{`C:\a\Documents\file.txt`}); err == nil {
		t.Fatal("a path whose remove was not denied was accepted")
	}
}

func TestRequireAccessDeniedUsesEachOperation(t *testing.T) {
	if err := requireAccessDenied(os.ErrPermission); err != nil {
		t.Fatal(err)
	}
	if err := requireAccessDenied(nil); err == nil || err.Error() != "sentinel_not_denied" {
		t.Fatalf("nil: %v", err)
	}
	if err := requireAccessDenied(os.ErrNotExist); err == nil || err.Error() != "sentinel_enoent" {
		t.Fatalf("enoent: %v", err)
	}
	if err := requireAccessDenied(errors.New("sharing violation")); err == nil || err.Error() != "sentinel_not_denied" {
		t.Fatalf("other: %v", err)
	}
}

func TestExitFromWaitPreservesNonZeroStatus(t *testing.T) {
	err := exitFromWait(23, nil)
	code, ok := exitCodeOf(err)
	if !ok || code != 23 {
		t.Fatalf("code=%d ok=%v err=%v", code, ok, err)
	}
	if err := exitFromWait(0, nil); err != nil {
		t.Fatal(err)
	}
	code, ok = exitCodeOf(exitFromWait(0, errors.New("wait failed")))
	if ok || code != 0 {
		t.Fatalf("wait failure looked like an exit: %d %v", code, ok)
	}
}

func TestFinalizeDeleteDoesNotClaimSuccess(t *testing.T) {
	out, err := finalizeDelete(true, nil)
	if err != nil || out.Result != "deleted" {
		t.Fatalf("%+v %v", out, err)
	}
	out, err = finalizeDelete(false, nil)
	if err == nil || out.Result != "BLOCKED" {
		t.Fatalf("root remained: %+v %v", out, err)
	}
	out, err = finalizeDelete(true, errors.New("profile busy"))
	if err == nil || out.Result != "BLOCKED" || !containsAll(out.Reason, "profile delete failed") {
		t.Fatalf("profile: %+v %v", out, err)
	}
}

func TestCleanupSynchronousAccessAddsSynchronize(t *testing.T) {
	raw := cleanupDirectoryAccess()
	if raw&synchronizeAccess != 0 {
		t.Fatal("the cleanup mask itself was changed to include SYNCHRONIZE; the NT wrapper is the contract")
	}
	if got := accessForSyncNT(raw); got&synchronizeAccess == 0 || got&^synchronizeAccess != raw {
		t.Fatalf("synchronous access = %#x", got)
	}
}

func TestCancelAfterWaitStartsStillTerminates(t *testing.T) {
	started := make(chan struct{})
	terminated := make(chan struct{})
	released := false
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sess := launchSession{
		Cancel: ctx.Done(),
		Wait: func() (int, error) {
			close(started)
			select {
			case <-terminated:
				return 0, nil
			case <-time.After(2 * time.Second):
				return 0, errors.New("watcher was gone before Wait returned")
			}
		},
		Terminate: func() error {
			close(terminated)
			return nil
		},
		Active:  func() (int, error) { return 0, nil },
		Release: func() { released = true },
	}
	errc := make(chan error, 1)
	go func() { errc <- sess.WaitQuiescent() }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("Wait did not start")
	}
	cancel()
	select {
	case err := <-errc:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancel during Wait did not finish the session")
	}
	if !released {
		t.Fatal("handles were not released after quiescence")
	}
}

func TestRefusalAfterLiveDescendantsDoesNotDelete(t *testing.T) {
	deleted := false
	p := &Prepared{
		Confined: true,
		rec:      &RunRecord{RunID: strings.Repeat("ab", 16), Root: `C:\pkg`},
		startFn: func() error {
			return settleWait(waitTimeout, nil)
		},
		removeFn: func() (CleanupOutcome, error) {
			deleted = true
			return CleanupOutcome{Result: "deleted"}, nil
		},
	}
	if err := p.Start(); err == nil || deleted {
		t.Fatalf("start err=%v deleted=%v", err, deleted)
	}
	if !errors.Is(p.blockedCleanup, ErrDescendantsActive) {
		t.Fatalf("gate: %v", p.blockedCleanup)
	}
	if _, err := p.Wait(); err == nil || deleted {
		t.Fatalf("Wait deleted=%v err=%v", deleted, err)
	}
}

func TestConfirmFailureDoesNotDeleteLiveDescendants(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	deleted := false
	ctx, cancel := context.WithCancel(context.Background())
	p := &Prepared{
		ctx:      ctx,
		confirmR: r,
		Confined: true,
		rec:      &RunRecord{RunID: strings.Repeat("cd", 16)},
		waitFn:   func() error { return ErrDescendantsActive },
		killFn:   func() error { return nil },
		removeFn: func() (CleanupOutcome, error) {
			deleted = true
			return CleanupOutcome{Result: "deleted"}, nil
		},
	}
	cancel()
	if err := p.readConfirm(); err == nil || !errors.Is(err, ErrDescendantsActive) || deleted {
		t.Fatalf("confirm err=%v deleted=%v", err, deleted)
	}
	if _, err := p.Wait(); deleted || err == nil {
		t.Fatalf("second cleanup deleted=%v err=%v", deleted, err)
	}
}

func TestQuiescentRefusalStillDeletes(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if _, err := w.Write([]byte("FAIL\n")); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
	deleted := false
	p := &Prepared{
		confirmR: r,
		Confined: true,
		rec:      &RunRecord{RunID: strings.Repeat("ef", 16)},
		waitFn:   func() error { return nil },
		removeFn: func() (CleanupOutcome, error) {
			deleted = true
			return CleanupOutcome{Result: "deleted"}, nil
		},
	}
	if err := p.readConfirm(); err == nil || !deleted {
		t.Fatalf("quiescent refusal err=%v deleted=%v", err, deleted)
	}
}

func TestOwnedPackageIsTheOnlyHomeDescendant(t *testing.T) {
	home := `C:\Users\me`
	runID := strings.Repeat("ab", 16)
	pkg := `\\?\C:\Users\me\AppData\Local\Packages\ap` + runID
	if homeBlocksDelete(home, pkg, "ap"+runID, "S-1-15-2-1", runID, true) {
		t.Fatal("owned package root was refused")
	}
	if !homeBlocksDelete(home, pkg, "ap"+runID, "", runID, true) {
		t.Fatal("package without a SID was accepted")
	}
	if !homeBlocksDelete(home, pkg, "someone-else", "S-1-15-2-1", runID, true) {
		t.Fatal("unrelated profile name was accepted")
	}
	if !homeBlocksDelete(home, pkg, "ap"+runID, "S-1-15-2-1", runID, false) {
		t.Fatal("package whose file identity did not match was accepted")
	}
	if !homeBlocksDelete(home, home, "ap"+runID, "S-1-15-2-1", runID, true) {
		t.Fatal("the home itself was accepted")
	}
	if !homeBlocksDelete(home, `C:\Users`, "ap"+runID, "S-1-15-2-1", runID, true) {
		t.Fatal("a parent of the home was accepted")
	}
	if !homeBlocksDelete(home, `C:\Users\me\Documents`, "ap"+runID, "S-1-15-2-1", runID, true) {
		t.Fatal("another home descendant was accepted")
	}
}

func TestManifestCannotDeleteAProfile(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(filepath.Dir(dir), "escaped.manifest.json")
	if err := os.WriteFile(outside, []byte("{\"run_id\":\"..\\\\escaped\",\"profile_created\":true,\"profile_name\":\"apevil\",\"nonce\":\"n\"}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	out, err := CleanupRun(`..\escaped`, CleanupOptions{RegistryDir: dir})
	if err == nil || out.Result != "BLOCKED" || strings.Contains(out.Reason, "apevil") {
		t.Fatalf("traversal: %+v %v", out, err)
	}
	id := strings.Repeat("ab", 16)
	body := []byte(`{"run_id":"` + id + `","nonce":"n","profile_name":"apforeign","package_sid":"S-1-15-2-9","profile_created":true,"phase":"s_ok","root":""}` + "\n")
	if err := os.WriteFile(filepath.Join(dir, id+".manifest.json"), body, 0600); err != nil {
		t.Fatal(err)
	}
	out, err = CleanupRun(id, CleanupOptions{RegistryDir: dir})
	if err == nil || out.Result != "BLOCKED" || !strings.Contains(out.Reason, "apforeign") || !strings.Contains(out.Reason, "retained") {
		t.Fatalf("forged: %+v %v", out, err)
	}
	if _, err := os.Stat(filepath.Join(dir, id+".manifest.json")); err != nil {
		t.Fatal("manifest evidence was removed")
	}
}

func TestFailureImmediatelyAfterSOKReportsBothErrors(t *testing.T) {
	removed := false
	err := afterProfileCreated("ap0123", func(string) error {
		return errors.New("disk full")
	}, func(name string) error {
		removed = true
		if name != "ap0123" {
			t.Fatalf("removed %s", name)
		}
		return errors.New("still exists")
	})
	if err == nil || !removed || !strings.Contains(err.Error(), "ap0123") || !strings.Contains(err.Error(), "disk full") || !strings.Contains(err.Error(), "still exists") {
		t.Fatalf("err=%v removed=%v", err, removed)
	}
	err = failCreatedProfile("ap0123", errors.New("no folder"), func(string) error {
		return errors.New("busy")
	}, func(string) error {
		return errors.New("manifest unwritable")
	})
	if err == nil || !strings.Contains(err.Error(), "no folder") || !strings.Contains(err.Error(), "busy") || !strings.Contains(err.Error(), "manifest unwritable") {
		t.Fatal(err)
	}
}

func containsAll(s string, parts ...string) bool {
	for _, p := range parts {
		if !contains(s, p) {
			return false
		}
	}
	return true
}

func contains(s, part string) bool {
	return len(part) == 0 || (len(s) >= len(part) && (s == part || len(s) > 0 && (indexOf(s, part) >= 0)))
}

func indexOf(s, part string) int {
	for i := 0; i+len(part) <= len(s); i++ {
		if s[i:i+len(part)] == part {
			return i
		}
	}
	return -1
}
