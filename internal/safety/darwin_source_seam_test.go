package safety

import (
	"errors"
	"fmt"
	"path"
	"strings"
	"testing"
)

// seamImageHash is a fixed fake digest. The inspector returns it from memory.
// These tests never open an executable.
const seamImageHash = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

const (
	seamRunID  = "run-seam-1234567890abcdef123456"
	seamRoot   = "/private/tmp/agentpulse-runs/run-seam-1234567890abcdef123456"
	seamNonce  = "nonce-seam-1234567890abcdef12345"
	seamExe    = "/usr/bin/agentpulse"
	seamParent = "/private/tmp/agentpulse-sentinels"
)

// memExclusiveOS is an in-memory exclusive-create boundary. It records the
// parent and name it was asked to open and never calls the host filesystem.
type memExclusiveOS struct {
	openDirErr error
	openErr    error
	writeErr   error
	fstatErr   error
	id         darwinExclusiveFileID
	fdPath     string
	fdPathErr  error
	parents    []string
	names      []string
	closed     []int
}

func (m *memExclusiveOS) bind() darwinExclusiveOS {
	return darwinExclusiveOS{
		OpenDirNoFollow: func(parent string) (int, error) {
			m.parents = append(m.parents, parent)
			if m.openDirErr != nil {
				return -1, m.openDirErr
			}
			return 7, nil
		},
		OpenExclusive: func(dirFD int, name string) (int, error) {
			m.names = append(m.names, name)
			if dirFD != 7 {
				return -1, fmt.Errorf("exclusive open did not receive the directory fd")
			}
			if m.openErr != nil {
				return -1, m.openErr
			}
			return 8, nil
		},
		Write: func(fd int, content []byte) error {
			if fd != 8 {
				return fmt.Errorf("write did not receive the exclusive fd")
			}
			if m.writeErr != nil {
				return m.writeErr
			}
			if len(content) == 0 {
				return fmt.Errorf("empty exclusive write")
			}
			return nil
		},
		Fstat: func(fd int) (darwinExclusiveFileID, error) {
			if fd != 8 {
				return darwinExclusiveFileID{}, fmt.Errorf("fstat did not receive the exclusive fd")
			}
			if m.fstatErr != nil {
				return darwinExclusiveFileID{}, m.fstatErr
			}
			return m.id, nil
		},
		FdPath: func(fd int) (string, error) {
			if fd != 8 {
				return "", fmt.Errorf("F_GETPATH did not receive the exclusive fd")
			}
			if m.fdPathErr != nil {
				return "", m.fdPathErr
			}
			return m.fdPath, nil
		},
		Close: func(fd int) { m.closed = append(m.closed, fd) },
	}
}

func seamRecord(sentinel string, dev, ino uint64) RunRecord {
	return RunRecord{
		RunID:       seamRunID,
		Root:        seamRoot,
		Nonce:       seamNonce,
		ExeSHA256:   seamImageHash,
		Sentinel:    sentinel,
		SentinelDev: dev,
		SentinelIno: ino,
	}
}

func installSeamInspector(dev, ino uint64, hash string, called *bool) func() {
	return setDarwinImageInspector(func(exePath string) (string, uint64, uint64, bool, string, error) {
		if called != nil {
			*called = true
		}
		return path.Clean(exePath), dev, ino, true, hash, nil
	})
}

// pendingFromExclusiveFD runs the production exclusive-create chain with an
// in-memory FD whose F_GETPATH equals probePath. It does not bind.
func pendingFromExclusiveFD(t *testing.T, probePath string, dev, ino uint64) {
	t.Helper()
	parent, name, err := splitDarwinExclusiveName(probePath)
	if err != nil {
		t.Fatalf("split %s: %v", probePath, err)
	}
	mem := &memExclusiveOS{
		id:     darwinExclusiveFileID{Dev: dev, Ino: ino, IsRegular: true},
		fdPath: probePath,
	}
	got, gotDev, gotIno, err := createExclusiveSentinelFD(mem.bind(), parent, name, []byte("sentinel-v1\n"))
	if err != nil {
		t.Fatalf("exclusive create %s: %v", probePath, err)
	}
	if got != probePath || gotDev != dev || gotIno != ino {
		t.Fatalf("exclusive identity %s = %q %d %d", probePath, got, gotDev, gotIno)
	}
}

// authorizeProbeFromExclusiveFD is create → pending → atomic bind. It is the
// only test helper that produces a synthetic proof, and it does so through
// the production chain rather than a string mint.
func authorizeProbeFromExclusiveFD(t *testing.T, runID, root, nonce, probePath string, dev, ino uint64) {
	t.Helper()
	pendingFromExclusiveFD(t, probePath, dev, ino)
	if err := bindDarwinSyntheticProbe(runID, root, nonce, probePath, dev, ino); err != nil {
		t.Fatalf("bind %s: %v", probePath, err)
	}
}

func assertNoSentinelGrant(t *testing.T, paths ...string) {
	t.Helper()
	darwinProbeMu.Lock()
	defer darwinProbeMu.Unlock()
	for _, p := range paths {
		if _, ok := darwinPendingProbes[p]; ok {
			t.Errorf("pending proof registered for %q", p)
		}
		if _, ok := darwinSyntheticProofs[p]; ok {
			t.Errorf("synthetic proof registered for %q", p)
		}
	}
}

func TestDarwinExclusiveCreateSourceSeam(t *testing.T) {
	asked := seamParent + "/sentinel-asked"
	joined := asked
	kernel := seamParent + "/sentinel-kernel"

	t.Run("FGetPathErrorExclusiveEntryZeroGrant", func(t *testing.T) {
		clearDarwinRegistries()
		mem := &memExclusiveOS{
			id:        darwinExclusiveFileID{Dev: 100, Ino: 200, IsRegular: true},
			fdPathErr: fmt.Errorf("F_GETPATH failed"),
		}
		restore := setDarwinExclusiveOS(mem.bind())
		defer restore()
		got, dev, ino, err := createDarwinExclusiveSentinel(asked, []byte("sentinel-v1\n"))
		if err == nil {
			t.Errorf("exclusive entry returned success path %q dev=%d ino=%d after F_GETPATH failure", got, dev, ino)
		} else if !errors.Is(err, ErrSentinelUnusable) {
			t.Errorf("exclusive entry error = %v, want ErrSentinelUnusable", err)
		}
		assertNoSentinelGrant(t, asked, joined, kernel)
		if len(mem.parents) != 1 || mem.parents[0] != seamParent || len(mem.names) != 1 || mem.names[0] != "sentinel-asked" {
			t.Errorf("exclusive entry did not open the caller parent/name: parents=%v names=%v", mem.parents, mem.names)
		}
	})

	t.Run("FGetPathErrorSyntheticChainZeroGrant", func(t *testing.T) {
		clearDarwinRegistries()
		mem := &memExclusiveOS{
			id:        darwinExclusiveFileID{Dev: 100, Ino: 200, IsRegular: true},
			fdPathErr: fmt.Errorf("F_GETPATH failed"),
		}
		got, dev, ino, err := createExclusiveSentinelFD(mem.bind(), seamParent, "sentinel-asked", []byte("sentinel-v1\n"))
		if err == nil {
			t.Errorf("synthetic chain returned success path %q dev=%d ino=%d after F_GETPATH failure", got, dev, ino)
		} else if !errors.Is(err, ErrSentinelUnusable) {
			t.Errorf("synthetic chain error = %v, want ErrSentinelUnusable", err)
		}
		assertNoSentinelGrant(t, asked, joined)
		if err := bindDarwinSyntheticProbe(seamRunID, seamRoot, seamNonce, asked, 100, 200); err != nil {
			t.Errorf("bind of a non-pending caller path returned %v; not-pending must be a no-op, not a grant", err)
		}
		if isVerifiedDarwinSyntheticProbe(asked, seamRoot) {
			t.Error("caller string became a verified probe after F_GETPATH failure")
		}
	})

	t.Run("FdPathSubstitutionDoesNotRegisterCallerString", func(t *testing.T) {
		clearDarwinRegistries()
		mem := &memExclusiveOS{
			id:     darwinExclusiveFileID{Dev: 300, Ino: 400, IsRegular: true},
			fdPath: kernel,
		}
		restore := setDarwinExclusiveOS(mem.bind())
		defer restore()
		got, dev, ino, err := createDarwinExclusiveSentinel(asked, []byte("sentinel-v1\n"))
		if err != nil {
			t.Fatalf("kernel path create failed: %v", err)
		}
		if got != kernel || dev != 300 || ino != 400 {
			t.Fatalf("create returned %q dev=%d ino=%d, want kernel identity", got, dev, ino)
		}
		darwinProbeMu.Lock()
		_, callerPending := darwinPendingProbes[asked]
		_, kernelPending := darwinPendingProbes[kernel]
		darwinProbeMu.Unlock()
		if callerPending {
			t.Fatal("caller string was registered beside the FD path")
		}
		if !kernelPending {
			t.Fatal("FD path was not pending")
		}
		if err := bindDarwinSyntheticProbe(seamRunID, seamRoot, seamNonce, asked, 300, 400); err != nil {
			t.Fatalf("bind of substituted caller string returned %v", err)
		}
		if isVerifiedDarwinSyntheticProbe(asked, seamRoot) {
			t.Fatal("substituted caller string was authorized")
		}
		if err := bindDarwinSyntheticProbe(seamRunID, seamRoot, seamNonce, kernel, 300, 400); err != nil {
			t.Fatalf("bind of FD path failed: %v", err)
		}
		if !isVerifiedDarwinSyntheticProbe(kernel, seamRoot) {
			t.Fatal("FD path was not authorized after exact bind")
		}
	})

	t.Run("EmptyFdPathZeroGrant", func(t *testing.T) {
		clearDarwinRegistries()
		mem := &memExclusiveOS{id: darwinExclusiveFileID{Dev: 100, Ino: 200, IsRegular: true}, fdPath: "   "}
		if _, _, _, err := createExclusiveSentinelFD(mem.bind(), seamParent, "sentinel-asked", []byte("sentinel-v1\n")); err == nil || !errors.Is(err, ErrSentinelUnusable) {
			t.Fatalf("blank F_GETPATH error = %v", err)
		}
		assertNoSentinelGrant(t, asked, "   ")
	})

	t.Run("NonRegularZeroGrant", func(t *testing.T) {
		clearDarwinRegistries()
		mem := &memExclusiveOS{id: darwinExclusiveFileID{Dev: 100, Ino: 200, IsRegular: false}, fdPath: kernel}
		if _, _, _, err := createExclusiveSentinelFD(mem.bind(), seamParent, "sentinel-asked", []byte("sentinel-v1\n")); err == nil || !errors.Is(err, ErrSentinelUnusable) {
			t.Fatalf("non-regular create error = %v", err)
		}
		assertNoSentinelGrant(t, asked, kernel)
	})

	t.Run("FstatErrorZeroGrant", func(t *testing.T) {
		clearDarwinRegistries()
		mem := &memExclusiveOS{fstatErr: fmt.Errorf("fstat failed"), fdPath: kernel}
		if _, _, _, err := createExclusiveSentinelFD(mem.bind(), seamParent, "sentinel-asked", []byte("sentinel-v1\n")); err == nil || !errors.Is(err, ErrSentinelUnusable) {
			t.Fatalf("fstat error = %v", err)
		}
		assertNoSentinelGrant(t, asked, kernel)
	})

	t.Run("ZeroIdentityZeroGrant", func(t *testing.T) {
		clearDarwinRegistries()
		mem := &memExclusiveOS{id: darwinExclusiveFileID{Dev: 0, Ino: 200, IsRegular: true}, fdPath: kernel}
		if _, _, _, err := createExclusiveSentinelFD(mem.bind(), seamParent, "sentinel-asked", []byte("sentinel-v1\n")); err == nil || !errors.Is(err, ErrSentinelUnusable) {
			t.Fatalf("zero dev error = %v", err)
		}
		assertNoSentinelGrant(t, asked, kernel)
		mem = &memExclusiveOS{id: darwinExclusiveFileID{Dev: 100, Ino: 0, IsRegular: true}, fdPath: kernel}
		if _, _, _, err := createExclusiveSentinelFD(mem.bind(), seamParent, "sentinel-asked", []byte("sentinel-v1\n")); err == nil || !errors.Is(err, ErrSentinelUnusable) {
			t.Fatalf("zero ino error = %v", err)
		}
		assertNoSentinelGrant(t, asked, kernel)
	})

	t.Run("ProtectedFdPathZeroGrant", func(t *testing.T) {
		clearDarwinRegistries()
		protected := "/Users/alice/sentinel-from-fd"
		mem := &memExclusiveOS{id: darwinExclusiveFileID{Dev: 100, Ino: 200, IsRegular: true}, fdPath: protected}
		if _, _, _, err := createExclusiveSentinelFD(mem.bind(), seamParent, "sentinel-asked", []byte("sentinel-v1\n")); err == nil || !errors.Is(err, ErrRefused) {
			t.Fatalf("protected FD path error = %v", err)
		}
		assertNoSentinelGrant(t, asked, protected)
	})

	t.Run("EmptyNonceDoesNotMintImage", func(t *testing.T) {
		clearDarwinRegistries()
		called := false
		restore := installSeamInspector(10, 20, seamImageHash, &called)
		defer restore()
		rec := seamRecord("", 0, 0)
		rec.Nonce = ""
		_, err := resolveDarwinAllowedReads(rec, seamExe, []string{"/Users/alice"})
		if err == nil {
			t.Error("empty nonce resolve returned nil error")
		}
		if called {
			t.Error("empty nonce still invoked the image inspector")
		}
		if isVerifiedDarwinImage(seamExe, seamRoot) {
			t.Error("empty nonce left a verified image proof")
		}
	})

	t.Run("WhitespaceNonceDoesNotMintImage", func(t *testing.T) {
		clearDarwinRegistries()
		called := false
		restore := installSeamInspector(10, 20, seamImageHash, &called)
		defer restore()
		rec := seamRecord("", 0, 0)
		rec.Nonce = "   "
		_, err := resolveDarwinAllowedReads(rec, seamExe, nil)
		if err == nil {
			t.Error("whitespace nonce resolve returned nil error")
		}
		if called {
			t.Error("whitespace nonce still invoked the image inspector")
		}
		if isVerifiedDarwinImage(seamExe, seamRoot) {
			t.Error("whitespace nonce left a verified image proof")
		}
	})

	t.Run("EmptyNonceImageProofRejectedByProfile", func(t *testing.T) {
		clearDarwinRegistries()
		darwinProbeMu.Lock()
		darwinImageProofs[seamExe] = DarwinImageProof{
			CanonicalPath: seamExe,
			Dev:           10,
			Ino:           20,
			RunID:         seamRunID,
			Root:          seamRoot,
			Nonce:         "",
			ExeSHA256:     seamImageHash,
			Minted:        true,
		}
		darwinProbeMu.Unlock()
		if _, err := DarwinSeatbeltProfile(seamRoot, seamExe); err == nil {
			t.Fatal("profile accepted an image proof with an empty nonce")
		}
	})

	t.Run("CreatePendingBindResolveProfile", func(t *testing.T) {
		clearDarwinRegistries()
		mem := &memExclusiveOS{
			id:     darwinExclusiveFileID{Dev: 100, Ino: 200, IsRegular: true},
			fdPath: kernel,
		}
		restoreOS := setDarwinExclusiveOS(mem.bind())
		defer restoreOS()
		got, dev, ino, err := createDarwinExclusiveSentinel(asked, []byte("sentinel-v1\n"))
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		if got != kernel || dev != 100 || ino != 200 {
			t.Fatalf("create identity = %q %d %d", got, dev, ino)
		}
		darwinProbeMu.Lock()
		pending := darwinPendingProbes[kernel]
		darwinProbeMu.Unlock()
		if pending.CanonicalPath != kernel || pending.Dev != 100 || pending.Ino != 200 {
			t.Fatalf("pending = %+v", pending)
		}
		if err := bindDarwinSyntheticProbe(seamRunID, seamRoot, seamNonce, kernel, dev, ino); err != nil {
			t.Fatalf("bind: %v", err)
		}
		darwinProbeMu.Lock()
		_, stillPending := darwinPendingProbes[kernel]
		probe := darwinSyntheticProofs[kernel]
		darwinProbeMu.Unlock()
		if stillPending {
			t.Fatal("pending survived bind")
		}
		if probe.RunID != seamRunID || probe.Root != seamRoot || probe.Nonce != seamNonce || probe.Dev != 100 || probe.Ino != 200 {
			t.Fatalf("probe proof = %+v", probe)
		}
		called := false
		restoreImg := installSeamInspector(10, 20, seamImageHash, &called)
		defer restoreImg()
		rec := seamRecord(kernel, dev, ino)
		reads, err := resolveDarwinAllowedReads(rec, seamExe, []string{"/Users/alice"})
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if !called {
			t.Fatal("resolver did not call the image inspector")
		}
		if len(reads) != 2 || reads[0] != seamExe || reads[1] != kernel {
			t.Fatalf("reads = %v", reads)
		}
		darwinProbeMu.Lock()
		image := darwinImageProofs[seamExe]
		darwinProbeMu.Unlock()
		if image.RunID != seamRunID || image.Root != path.Clean(seamRoot) || image.Nonce != seamNonce || image.ExeSHA256 != seamImageHash || image.Dev != 10 || image.Ino != 20 {
			t.Errorf("image proof = %+v, want exact run/root/nonce", image)
		}
		prof, err := DarwinSeatbeltProfile(seamRoot, reads...)
		if err != nil {
			t.Fatalf("profile: %v", err)
		}
		for _, lit := range []string{seamExe, kernel} {
			want := fmt.Sprintf("(allow file-read* (literal %q))", lit)
			if !strings.Contains(prof, want) {
				t.Errorf("profile missing %s", want)
			}
		}
		if !strings.Contains(prof, `(deny mach-lookup (global-name "com.apple.securityd"))`) {
			t.Error("profile dropped the securityd denial")
		}
		// Empty context cannot reuse the consumed FD identity.
		if err := bindDarwinSyntheticProbe("", seamRoot, seamNonce, kernel, dev, ino); err != nil {
			t.Fatalf("second bind of consumed probe = %v", err)
		}
		if isVerifiedDarwinSyntheticProbe(kernel, "") {
			t.Error("empty root verified the probe")
		}
	})
}
