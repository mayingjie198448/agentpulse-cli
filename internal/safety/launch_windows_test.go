//go:build windows

package safety

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

// parseEnvBlock decodes a UTF-16 environment block into individual entries.
// A valid Windows environment block consists of null-terminated UTF-16
// strings, terminated by an additional null character (double-NUL at the end).
func parseEnvBlock(block []uint16) []string {
	if len(block) == 0 {
		return nil
	}
	var entries []string
	start := 0
	for i := 0; i < len(block); i++ {
		if block[i] == 0 {
			if i == start {
				// Empty entry marks the final double-NUL terminator
				break
			}
			entry := string(utf16.Decode(block[start:i]))
			entries = append(entries, entry)
			start = i + 1
		}
	}
	return entries
}

func TestWindowsEnvBlockSortContract(t *testing.T) {
	t.Run("EmptyEnvPreserved", func(t *testing.T) {
		for _, in := range [][]string{nil, {}} {
			ptr, block, err := envBlock(in)
			if err != nil {
				t.Fatalf("envBlock(%#v) unexpected error: %v", in, err)
			}
			if ptr != nil {
				t.Fatalf("envBlock(%#v) expected nil ptr, got %p", in, ptr)
			}
			if block != nil {
				t.Fatalf("envBlock(%#v) expected nil block, got %v", in, block)
			}
		}
	})

	t.Run("ValuePreservationAndInputImmutability", func(t *testing.T) {
		input := []string{
			"Z_KEY=val with spaces and = signs = intact",
			"A_KEY=",
			"M_KEY=MiXeD_CaSe_VaL",
		}
		orig := slices.Clone(input)

		ptr, block, err := envBlock(input)
		if err != nil {
			t.Fatalf("envBlock failed: %v", err)
		}
		if ptr == nil || block == nil {
			t.Fatalf("envBlock returned nil pointer or block")
		}

		// Input slice must not be modified in place.
		if !slices.Equal(input, orig) {
			t.Fatalf("input slice mutated: got %v, want %v", input, orig)
		}

		got := parseEnvBlock(block)
		want := []string{
			"A_KEY=",
			"M_KEY=MiXeD_CaSe_VaL",
			"Z_KEY=val with spaces and = signs = intact",
		}
		if !slices.Equal(got, want) {
			t.Fatalf("got %v, want %v", got, want)
		}
	})

	t.Run("AlphaBeforeAFirst", func(t *testing.T) {
		// Under Windows ordinal sorting, 'L' (0x004C) < '_' (0x005F).
		// Non-ordinal or linguistic sort may place '_' before letters.
		input := []string{
			"A_FIRST=1",
			"ALPHA=2",
			"a_first_lower=3",
			"alpha_lower=4",
		}
		_, block, err := envBlock(input)
		if err != nil {
			t.Fatalf("envBlock failed: %v", err)
		}
		got := parseEnvBlock(block)
		want := []string{
			"ALPHA=2",
			"alpha_lower=4",
			"A_FIRST=1",
			"a_first_lower=3",
		}
		if !slices.Equal(got, want) {
			t.Fatalf("got %v, want %v", got, want)
		}
	})

	t.Run("NonBMPVsBMPPrivateUseDiscriminator", func(t *testing.T) {
		// In UTF-16 ordinal comparison (Windows native contract):
		// Non-BMP code point U+10000 encodes to surrogate pair [0xD800, 0xDC00].
		// BMP private-use code point U+E000 encodes to single code unit [0xE000].
		// Because 0xD800 < 0xE000, U+10000 must sort BEFORE U+E000.
		//
		// (A naive UTF-8 byte comparison or Unicode rune comparison would
		// incorrectly place U+E000 before U+10000 because 0xEE < 0xF0 in UTF-8
		// and rune 0xE000 < rune 0x10000).
		input := []string{
			"\uE000_BMP_PUA=pua_val",
			"\U00010000_NON_BMP=non_bmp_val",
		}
		_, block, err := envBlock(input)
		if err != nil {
			t.Fatalf("envBlock failed: %v", err)
		}
		got := parseEnvBlock(block)
		want := []string{
			"\U00010000_NON_BMP=non_bmp_val",
			"\uE000_BMP_PUA=pua_val",
		}
		if !slices.Equal(got, want) {
			t.Fatalf("got %v, want %v", got, want)
		}
	})

	t.Run("MixedCaseUnicodeOrdinalSort", func(t *testing.T) {
		// Greek mixed-case letters and Latin letters:
		// 'A' (Latin, 0x0041)
		// 'Z' (Latin, 0x005A)
		// 'α' (Greek Small Alpha, U+03B1) folds to 'Α' (Greek Capital Alpha, U+0391)
		// 'β' (Greek Small Beta, U+03B2) folds to 'Β' (Greek Capital Beta, U+0392)
		// 'ω' (Greek Small Omega, U+03C9) folds to 'Ω' (Greek Capital Omega, U+03A9)
		// Under ordinal comparison: Latin (0x41, 0x5A) < Greek (0x391, 0x392, 0x3A9).
		input := []string{
			"ω_omega=5",
			"Z_LATIN=2",
			"Α_ALPHA=3",
			"a_latin=1",
			"Β_BETA=4",
		}
		_, block, err := envBlock(input)
		if err != nil {
			t.Fatalf("envBlock failed: %v", err)
		}
		got := parseEnvBlock(block)
		want := []string{
			"a_latin=1",
			"Z_LATIN=2",
			"Α_ALPHA=3",
			"Β_BETA=4",
			"ω_omega=5",
		}
		if !slices.Equal(got, want) {
			t.Fatalf("got %v, want %v", got, want)
		}
	})

	t.Run("StableCaseEqualNames", func(t *testing.T) {
		// When variable names are case-insensitively equal, their relative
		// input order must be preserved (stable sort).
		input1 := []string{"foo=1", "FOO=2", "bar=3", "BAR=4"}
		_, block1, err := envBlock(input1)
		if err != nil {
			t.Fatalf("envBlock failed: %v", err)
		}
		got1 := parseEnvBlock(block1)
		want1 := []string{"bar=3", "BAR=4", "foo=1", "FOO=2"}
		if !slices.Equal(got1, want1) {
			t.Fatalf("case 1: got %v, want %v", got1, want1)
		}

		input2 := []string{"FOO=2", "foo=1", "BAR=4", "bar=3"}
		_, block2, err := envBlock(input2)
		if err != nil {
			t.Fatalf("envBlock failed: %v", err)
		}
		got2 := parseEnvBlock(block2)
		want2 := []string{"BAR=4", "bar=3", "FOO=2", "foo=1"}
		if !slices.Equal(got2, want2) {
			t.Fatalf("case 2: got %v, want %v", got2, want2)
		}
	})
}

func TestWindowsEnvBlockUTF16Contract(t *testing.T) {
	t.Run("BlockStructureAndDoubleNUL", func(t *testing.T) {
		input := []string{"FOO=bar", "BAZ=qux"}
		ptr, block, err := envBlock(input)
		if err != nil {
			t.Fatalf("envBlock failed: %v", err)
		}
		if ptr == nil || block == nil {
			t.Fatalf("expected non-nil pointer and block")
		}
		if ptr != &block[0] {
			t.Fatalf("expected ptr to point to &block[0]")
		}
		if len(block) < 2 {
			t.Fatalf("block too short: %d", len(block))
		}
		// Block must end with double NUL: entry terminator + block terminator
		if block[len(block)-1] != 0 || block[len(block)-2] != 0 {
			t.Fatalf("block not terminated with double NUL: [%x, %x]",
				block[len(block)-2], block[len(block)-1])
		}
		parsed := parseEnvBlock(block)
		if len(parsed) != 2 {
			t.Fatalf("parsed entries count %d, want 2: %v", len(parsed), parsed)
		}
	})

	t.Run("NonBMPSurrogateValues", func(t *testing.T) {
		// Non-BMP characters in values must be correctly encoded as UTF-16
		// surrogate pairs and survive decode intact.
		// Squirrel U+1F43F -> 0xD83D, 0xDC3F
		// Grinning Face U+1F600 -> 0xD83D, 0xDE00
		input := []string{
			"EMOJI_VAL=squirrel: \U0001F43F, grin: \U0001F600",
			"SUPPLEMENTARY_VAL=linear_b: \U00010000",
		}
		ptr, block, err := envBlock(input)
		if err != nil {
			t.Fatalf("envBlock failed: %v", err)
		}
		if ptr == nil || block == nil {
			t.Fatalf("expected non-nil pointer and block")
		}

		// Verify surrogate code units exist in raw block
		foundSquirrelSurrogate := false
		foundGrinSurrogate := false
		for i := 0; i+1 < len(block); i++ {
			if block[i] == 0xD83D && block[i+1] == 0xDC3F {
				foundSquirrelSurrogate = true
			}
			if block[i] == 0xD83D && block[i+1] == 0xDE00 {
				foundGrinSurrogate = true
			}
		}
		if !foundSquirrelSurrogate {
			t.Errorf("surrogate pair for U+1F43F (0xD83D, 0xDC3F) not found in block")
		}
		if !foundGrinSurrogate {
			t.Errorf("surrogate pair for U+1F600 (0xD83D, 0xDE00) not found in block")
		}

		got := parseEnvBlock(block)
		if len(got) != 2 {
			t.Fatalf("got %d entries, want 2: %v", len(got), got)
		}
		// Value preservation check
		foundEmoji := false
		foundLinearB := false
		for _, entry := range got {
			if strings.Contains(entry, "\U0001F43F") && strings.Contains(entry, "\U0001F600") {
				foundEmoji = true
			}
			if strings.Contains(entry, "\U00010000") {
				foundLinearB = true
			}
		}
		if !foundEmoji || !foundLinearB {
			t.Fatalf("surrogate values not preserved intact in parsed block: %v", got)
		}
	})

	t.Run("RejectInteriorAndDoubleNUL", func(t *testing.T) {
		// CreateProcess environment strings must reject any NUL embedded
		// within an entry (interior NUL, double NUL, leading/trailing NUL).
		badInputs := [][]string{
			{"KEY\x00NAME=val"},
			{"KEY=val\x00ue"},
			{"K\x00\x00EY=val"},
			{"KEY=val\x00\x00ue"},
			{"\x00KEY=val"},
			{"KEY=val\x00"},
			{"\x00"},
			{"VALID=1", "BAD\x00KEY=2"},
			{"VALID=1", "BAD=val\x00ue"},
		}
		for _, bad := range badInputs {
			ptr, block, err := envBlock(bad)
			if err == nil {
				t.Errorf("envBlock(%#v) expected error for embedded NUL, got nil (block: %v)", bad, block)
			}
			if ptr != nil {
				t.Errorf("envBlock(%#v) expected nil ptr on error, got %p", bad, ptr)
			}
			if block != nil {
				t.Errorf("envBlock(%#v) expected nil block on error, got %v", bad, block)
			}
		}
	})
}

// assertExactNumericErrno asserts that the explicit numeric diagnostic field
// e.g. "(errno 5)" appears in the error message, rather than any freestanding digit.
func assertExactNumericErrno(t *testing.T, msg string, errno windows.Errno) {
	t.Helper()
	field := fmt.Sprintf("(errno %d)", errno)
	if !strings.Contains(msg, field) {
		t.Fatalf("expected explicit numeric diagnostic field %q in error message %q", field, msg)
	}
}

func TestWindowsCreateProcessErrnoContract(t *testing.T) {
	t.Run("DirectErrnoAccessDenied", func(t *testing.T) {
		origErr := windows.ERROR_ACCESS_DENIED
		got := formatCreateProcessError(origErr)
		if got == nil {
			t.Fatalf("expected non-nil error")
		}

		// Exact numeric errno field assertion (not substring of a digit)
		assertExactNumericErrno(t, got.Error(), origErr)

		// errors.Is / As chain identity
		if !errors.Is(got, origErr) {
			t.Errorf("errors.Is(got, ERROR_ACCESS_DENIED) = false, want true")
		}

		var targetErrno windows.Errno
		if !errors.As(got, &targetErrno) {
			t.Errorf("errors.As(got, &targetErrno) = false, want true")
		} else if targetErrno != origErr {
			t.Errorf("errors.As got %d, want %d", targetErrno, origErr)
		}

		// Access-denied wording retained
		if !strings.Contains(got.Error(), "AppContainer cannot start the executable (access denied)") {
			t.Errorf("missing access-denied wording in: %q", got.Error())
		}
		if !strings.Contains(got.Error(), "Refusing to rewrite ACLs outside the fixture temp or package folder") {
			t.Errorf("missing ACL refusal wording in: %q", got.Error())
		}
	})

	t.Run("DirectErrnoOther", func(t *testing.T) {
		origErr := windows.ERROR_FILE_NOT_FOUND
		got := formatCreateProcessError(origErr)
		if got == nil {
			t.Fatalf("expected non-nil error")
		}

		assertExactNumericErrno(t, got.Error(), origErr)

		if !errors.Is(got, origErr) {
			t.Errorf("errors.Is(got, ERROR_FILE_NOT_FOUND) = false, want true")
		}

		var targetErrno windows.Errno
		if !errors.As(got, &targetErrno) {
			t.Errorf("errors.As(got, &targetErrno) = false, want true")
		} else if targetErrno != origErr {
			t.Errorf("errors.As got %d, want %d", targetErrno, origErr)
		}

		if !strings.Contains(got.Error(), "BLOCKED: CreateProcess AppContainer:") {
			t.Errorf("missing CreateProcess AppContainer prefix in: %q", got.Error())
		}
	})

	t.Run("WrappedErrnoNested", func(t *testing.T) {
		origErr := windows.ERROR_ACCESS_DENIED
		// Genuine wrapping with %w (do not use %v to destroy the test chain)
		wrapped := fmt.Errorf("nested outer failure: %w", origErr)
		got := formatCreateProcessError(wrapped)
		if got == nil {
			t.Fatalf("expected non-nil error")
		}

		assertExactNumericErrno(t, got.Error(), origErr)

		if !errors.Is(got, origErr) {
			t.Errorf("errors.Is(got, ERROR_ACCESS_DENIED) = false, want true")
		}
		if !errors.Is(got, wrapped) {
			t.Errorf("errors.Is(got, wrapped) = false, want true")
		}

		var targetErrno windows.Errno
		if !errors.As(got, &targetErrno) {
			t.Errorf("errors.As(got, &targetErrno) = false, want true")
		} else if targetErrno != origErr {
			t.Errorf("errors.As got %d, want %d", targetErrno, origErr)
		}

		// Access-denied wording retained for nested access denied
		if !strings.Contains(got.Error(), "AppContainer cannot start the executable (access denied)") {
			t.Errorf("missing access-denied wording in: %q", got.Error())
		}
	})

	t.Run("JoinedWithErrRefused", func(t *testing.T) {
		origErr := windows.ERROR_ACCESS_DENIED
		joined := errors.Join(ErrRefused, origErr)
		got := formatCreateProcessError(joined)
		if got == nil {
			t.Fatalf("expected non-nil error")
		}

		assertExactNumericErrno(t, got.Error(), origErr)

		// errors.Is identity for both ErrRefused and errno
		if !errors.Is(got, ErrRefused) {
			t.Errorf("errors.Is(got, ErrRefused) = false, want true")
		}
		if !errors.Is(got, origErr) {
			t.Errorf("errors.Is(got, ERROR_ACCESS_DENIED) = false, want true")
		}
		if !errors.Is(got, joined) {
			t.Errorf("errors.Is(got, joined) = false, want true")
		}

		var targetErrno windows.Errno
		if !errors.As(got, &targetErrno) {
			t.Errorf("errors.As(got, &targetErrno) = false, want true")
		} else if targetErrno != origErr {
			t.Errorf("errors.As got %d, want %d", targetErrno, origErr)
		}

		// Access-denied wording retained
		if !strings.Contains(got.Error(), "AppContainer cannot start the executable (access denied)") {
			t.Errorf("missing access-denied wording in: %q", got.Error())
		}
	})

	t.Run("JoinedWithErrRefusedNonAccessDenied", func(t *testing.T) {
		origErr := windows.ERROR_FILE_NOT_FOUND
		joined := errors.Join(ErrRefused, origErr)
		got := formatCreateProcessError(joined)
		if got == nil {
			t.Fatalf("expected non-nil error")
		}

		assertExactNumericErrno(t, got.Error(), origErr)

		if !errors.Is(got, ErrRefused) {
			t.Errorf("errors.Is(got, ErrRefused) = false, want true")
		}
		if !errors.Is(got, origErr) {
			t.Errorf("errors.Is(got, ERROR_FILE_NOT_FOUND) = false, want true")
		}

		var targetErrno windows.Errno
		if !errors.As(got, &targetErrno) {
			t.Errorf("errors.As(got, &targetErrno) = false, want true")
		} else if targetErrno != origErr {
			t.Errorf("errors.As got %d, want %d", targetErrno, origErr)
		}
	})

	t.Run("GenericNonErrnoChainRetained", func(t *testing.T) {
		genericErr := errors.New("generic non-errno failure")
		got := formatCreateProcessError(genericErr)
		if got == nil {
			t.Fatalf("expected non-nil error")
		}

		// generic non-errno chain retained
		if !errors.Is(got, genericErr) {
			t.Errorf("errors.Is(got, genericErr) = false, want true")
		}

		var targetErrno windows.Errno
		if errors.As(got, &targetErrno) {
			t.Errorf("errors.As unexpectedly matched windows.Errno (%d) for generic error", targetErrno)
		}

		if !strings.Contains(got.Error(), "BLOCKED: CreateProcess AppContainer:") {
			t.Errorf("missing CreateProcess AppContainer prefix in: %q", got.Error())
		}
		if !strings.Contains(got.Error(), "generic non-errno failure") {
			t.Errorf("missing generic error text in: %q", got.Error())
		}
	})
}

func TestWindowsLaunchCriticalEnvContract(t *testing.T) {
	t.Run("ReplenishMissingSystemRoot", func(t *testing.T) {
		input := []string{"PATH=C:\\bin", "TEMP=C:\\temp"}
		orig := slices.Clone(input)
		got := addCriticalEnv(input)

		// Input immutability
		if !slices.Equal(input, orig) {
			t.Fatalf("input mutated: got %v, want %v", input, orig)
		}

		// SYSTEMROOT must be present
		found := false
		var systemRootVal string
		for _, kv := range got {
			k, v, ok := strings.Cut(kv, "=")
			if ok && strings.EqualFold(k, "SYSTEMROOT") {
				found = true
				systemRootVal = v
				break
			}
		}
		if !found {
			t.Fatalf("expected SYSTEMROOT in child environment, got: %v", got)
		}
		expectedRoot := os.Getenv("SystemRoot")
		if expectedRoot == "" {
			expectedRoot = os.Getenv("SYSTEMROOT")
		}
		if expectedRoot != "" && !strings.EqualFold(systemRootVal, expectedRoot) {
			t.Fatalf("SYSTEMROOT value %q does not match parent %q", systemRootVal, expectedRoot)
		}

		// Existing entries preserved
		hasPath := false
		hasTemp := false
		for _, kv := range got {
			if strings.HasPrefix(kv, "PATH=") {
				hasPath = true
			}
			if strings.HasPrefix(kv, "TEMP=") {
				hasTemp = true
			}
		}
		if !hasPath || !hasTemp {
			t.Fatalf("existing entries lost in %v", got)
		}
	})

	t.Run("DoNotInheritOtherParentEnv", func(t *testing.T) {
		input := []string{"ISOLATED_APP=1"}
		got := addCriticalEnv(input)

		// Verify no uncontrolled leakage of parent environment
		disallowed := []string{"USERPROFILE", "USERNAME", "COMSPEC", "COMPUTERNAME", "APPDATA"}
		for _, kv := range got {
			k, _, ok := strings.Cut(kv, "=")
			if !ok {
				continue
			}
			for _, bad := range disallowed {
				if strings.EqualFold(k, bad) {
					t.Fatalf("unexpected parent variable %q inherited into child: %v", k, got)
				}
			}
		}
	})

	t.Run("PreserveExistingSystemRoot", func(t *testing.T) {
		input := []string{"PATH=C:\\bin", "SystemRoot=C:\\CustomWindows"}
		got := addCriticalEnv(input)
		if len(got) != 2 {
			t.Fatalf("expected exactly 2 entries, got %d: %v", len(got), got)
		}
		foundCount := 0
		for _, kv := range got {
			k, v, ok := strings.Cut(kv, "=")
			if ok && strings.EqualFold(k, "SYSTEMROOT") {
				foundCount++
				if v != "C:\\CustomWindows" {
					t.Fatalf("existing SystemRoot value mutated: got %q, want %q", v, "C:\\CustomWindows")
				}
			}
		}
		if foundCount != 1 {
			t.Fatalf("expected exactly 1 SystemRoot entry, got %d", foundCount)
		}
	})

	t.Run("CaseInsensitiveDetection", func(t *testing.T) {
		input := []string{"systemroot=C:\\LowerWindows", "OTHER=1"}
		got := addCriticalEnv(input)
		if len(got) != 2 {
			t.Fatalf("expected exactly 2 entries, got %d: %v", len(got), got)
		}
		foundCount := 0
		for _, kv := range got {
			k, _, ok := strings.Cut(kv, "=")
			if ok && strings.EqualFold(k, "SYSTEMROOT") {
				foundCount++
			}
		}
		if foundCount != 1 {
			t.Fatalf("expected exactly 1 systemroot entry, got %d", foundCount)
		}
	})

	t.Run("EnvBlockIntegrationAndSorting", func(t *testing.T) {
		input := []string{"TEMP=C:\\temp", "PATH=C:\\bin"}
		gotEnv := addCriticalEnv(input)
		ptr, block, err := envBlock(gotEnv)
		if err != nil {
			t.Fatalf("envBlock failed: %v", err)
		}
		if ptr == nil || block == nil {
			t.Fatalf("envBlock returned nil ptr or block")
		}
		entries := parseEnvBlock(block)
		foundSystemRoot := false
		for _, e := range entries {
			k, _, ok := strings.Cut(e, "=")
			if ok && strings.EqualFold(k, "SYSTEMROOT") {
				foundSystemRoot = true
				break
			}
		}
		if !foundSystemRoot {
			t.Fatalf("SYSTEMROOT missing from envBlock output: %v", entries)
		}

		// Verify ordinal sort order: PATH (0x50) < SYSTEMROOT (0x53) < TEMP (0x54)
		pathIdx, srIdx, tempIdx := -1, -1, -1
		for i, e := range entries {
			k, _, ok := strings.Cut(e, "=")
			if !ok {
				continue
			}
			if strings.EqualFold(k, "PATH") {
				pathIdx = i
			}
			if strings.EqualFold(k, "SYSTEMROOT") {
				srIdx = i
			}
			if strings.EqualFold(k, "TEMP") {
				tempIdx = i
			}
		}
		if pathIdx == -1 || srIdx == -1 || tempIdx == -1 {
			t.Fatalf("entries missing from block: path=%d, sr=%d, temp=%d: %v", pathIdx, srIdx, tempIdx, entries)
		}
		if !(pathIdx < srIdx && srIdx < tempIdx) {
			t.Fatalf("expected sort order PATH < SYSTEMROOT < TEMP, got indices: path=%d, sr=%d, temp=%d", pathIdx, srIdx, tempIdx)
		}
	})

	t.Run("ConfinedLaunchConfiguredEnv", func(t *testing.T) {
		p := &Prepared{
			Cmd:      &exec.Cmd{Env: []string{"PATH=C:\\bin", "HOME=C:\\fake-home"}},
			Confined: true,
			rec: &RunRecord{
				ProfileName: "test-profile-contract",
				PackageSID:  "S-1-15-2-12345-67890",
			},
		}
		if err := configureConfinedLaunch(p); err != nil {
			t.Fatalf("configureConfinedLaunch unexpected error: %v", err)
		}
		found := false
		for _, kv := range p.Cmd.Env {
			k, _, ok := strings.Cut(kv, "=")
			if ok && strings.EqualFold(k, "SYSTEMROOT") {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("configureConfinedLaunch did not supply missing SYSTEMROOT in p.Cmd.Env: %v", p.Cmd.Env)
		}
	})

	t.Run("B1_ParentMissingOrEmptyYieldsCriticalEnv", func(t *testing.T) {
		// When parent SystemRoot is missing or empty, addCriticalEnv must still
		// append "SYSTEMROOT=" to child environment, matching Go1.27.3 os/exec.
		// A nil/empty child must NOT remain nil/empty.
		testCases := []struct {
			name   string
			child  []string
			parent string
		}{
			{"NilChildParentEmpty", nil, ""},
			{"EmptyChildParentEmpty", []string{}, ""},
			{"NonEmptyChildParentEmpty", []string{"ALPHA=1", "BETA=2"}, ""},
			{"NilChildParentSynthetic", nil, "X:\\SyntheticWindows"},
			{"EmptyChildParentSynthetic", []string{}, "X:\\SyntheticWindows"},
			{"NonEmptyChildParentSynthetic", []string{"ALPHA=1"}, "X:\\SyntheticWindows"},
		}

		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				origChild := slices.Clone(tc.child)
				got := addCriticalEnvWithLookup(tc.child, func() string { return tc.parent })

				// Child input must remain unmutated
				if tc.child != nil && !slices.Equal(tc.child, origChild) {
					t.Fatalf("child input mutated")
				}

				found := false
				var foundVal string
				for _, kv := range got {
					k, v, ok := strings.Cut(kv, "=")
					if ok && strings.EqualFold(k, "SYSTEMROOT") {
						found = true
						foundVal = v
						break
					}
				}
				if !found {
					t.Fatalf("SYSTEMROOT missing from result (child len %d, parent len %d)", len(tc.child), len(tc.parent))
				}
				if foundVal != tc.parent {
					t.Fatalf("SYSTEMROOT value mismatch: got %q, want %q", foundVal, tc.parent)
				}
			})
		}
	})

	t.Run("B1_ChildNilOrEmptyYieldsNonNilBlock", func(t *testing.T) {
		// When child is nil or empty and parent SystemRoot is missing/empty,
		// the resulting environment block must be non-NULL and double-NUL terminated.
		// Raw CreateProcess(NULL environment) inherits the complete parent environment;
		// this test verifies confinement is maintained.
		for _, child := range [][]string{nil, {}} {
			got := addCriticalEnvWithLookup(child, func() string { return "" })
			ptr, block, err := envBlock(got)
			if err != nil {
				t.Fatalf("envBlock failed: %v", err)
			}
			if ptr == nil {
				t.Fatalf("envBlock returned nil pointer for child (static conditional risk: raw CreateProcess would inherit full parent env)")
			}
			if block == nil {
				t.Fatalf("envBlock returned nil block")
			}
			if len(block) < 2 {
				t.Fatalf("block too short: len %d", len(block))
			}
			if block[len(block)-1] != 0 || block[len(block)-2] != 0 {
				t.Fatalf("block not terminated with double NUL")
			}
			parsed := parseEnvBlock(block)
			if len(parsed) != 1 || parsed[0] != "SYSTEMROOT=" {
				t.Fatalf("unexpected parsed block: %v", parsed)
			}
		}
	})

	t.Run("B1_CaseAliasesPreservedWithoutDuplication", func(t *testing.T) {
		// Case aliases of SYSTEMROOT must be recognized case-insensitively.
		// Existing entries must not be duplicated or overwritten.
		aliases := []string{
			"SYSTEMROOT=C:\\Upper",
			"systemroot=C:\\Lower",
			"SystemRoot=C:\\Mixed",
			"sYsTeMrOoT=C:\\Weird",
		}
		for _, alias := range aliases {
			input := []string{"PATH=C:\\bin", alias}
			got := addCriticalEnvWithLookup(input, func() string { return "X:\\ParentWindows" })
			if len(got) != 2 {
				t.Fatalf("expected 2 entries for alias %q, got %d: %v", alias, len(got), got)
			}
			count := 0
			for _, kv := range got {
				k, _, ok := strings.Cut(kv, "=")
				if ok && strings.EqualFold(k, "SYSTEMROOT") {
					count++
					if kv != alias {
						t.Fatalf("alias entry modified: got %q, want %q", kv, alias)
					}
				}
			}
			if count != 1 {
				t.Fatalf("expected exactly 1 critical env entry, got %d", count)
			}
		}
	})

	t.Run("B1_ExplicitChildEmptyPreserved", func(t *testing.T) {
		// Existing explicit SYSTEMROOT with empty value must NOT be overwritten
		// with parent SystemRoot.
		cases := []string{"SYSTEMROOT=", "systemroot=", "SystemRoot="}
		for _, explicit := range cases {
			input := []string{"FOO=bar", explicit}
			got := addCriticalEnvWithLookup(input, func() string { return "X:\\ParentWindows" })
			if len(got) != 2 {
				t.Fatalf("expected 2 entries, got %d: %v", len(got), got)
			}
			found := false
			for _, kv := range got {
				if kv == explicit {
					found = true
				}
			}
			if !found {
				t.Fatalf("explicit empty entry %q was overwritten: %v", explicit, got)
			}
		}
	})

	t.Run("B1_ExactOutputSet", func(t *testing.T) {
		// Only SYSTEMROOT should be appended when missing; no other parent env vars
		// should ever be injected into the child environment.
		input := []string{"VAR1=val1", "VAR2=val2"}
		got := addCriticalEnvWithLookup(input, func() string { return "X:\\SyntheticWindows" })
		if len(got) != 3 {
			t.Fatalf("expected exactly 3 entries, got %d: %v", len(got), got)
		}
		want := []string{"VAR1=val1", "VAR2=val2", "SYSTEMROOT=X:\\SyntheticWindows"}
		if !slices.Equal(got, want) {
			t.Fatalf("output set mismatch: got %v, want %v", got, want)
		}
	})

	t.Run("B1_InputSpareCapacityUntouched", func(t *testing.T) {
		// When input slice has spare capacity, addCriticalEnv must not overwrite
		// the spare capacity of the input backing array.
		backing := make([]string, 2, 8)
		backing[0] = "KEY1=V1"
		backing[1] = "KEY2=V2"
		input := backing[:2]

		origBacking := make([]string, 8)
		copy(origBacking, backing[:8])

		got := addCriticalEnvWithLookup(input, func() string { return "X:\\SyntheticWindows" })

		if len(input) != 2 {
			t.Fatalf("input length mutated: got %d, want 2", len(input))
		}
		if !slices.Equal(backing[:8], origBacking) {
			t.Fatalf("input backing array mutated: got %v, want %v", backing[:8], origBacking)
		}
		if len(got) != 3 {
			t.Fatalf("expected 3 entries in output, got %d", len(got))
		}
	})

	t.Run("B1_SystemRootLookupPackageVariableSeam", func(t *testing.T) {
		// Test package-level systemRootLookup variable seam.
		orig := systemRootLookup
		t.Cleanup(func() { systemRootLookup = orig })

		systemRootLookup = func() string { return "" }
		got := addCriticalEnv(nil)
		if len(got) != 1 || got[0] != "SYSTEMROOT=" {
			t.Fatalf("addCriticalEnv(nil) with empty seam returned: %v", got)
		}
		ptr, block, err := envBlock(got)
		if err != nil || ptr == nil || block == nil {
			t.Fatalf("envBlock failed or returned nil: err=%v, ptr=%p, block=%v", err, ptr, block)
		}
	})

	t.Run("B1_MalformedEntriesAndEmbeddedNUL", func(t *testing.T) {
		// Malformed entries without '=' or leading '=' must not panic.
		malformed := []string{"MALFORMED_NO_EQUALS", "=LEADING_EQUALS", "VALID=1"}
		got := addCriticalEnvWithLookup(malformed, func() string { return "X:\\SyntheticWindows" })
		if len(got) != 4 {
			t.Fatalf("expected 4 entries, got %d: %v", len(got), got)
		}

		// Embedded NUL in child entries must be rejected by envBlock.
		badChild := []string{"SYSTEMROOT=X:\\Win\x00dows"}
		ptr, block, err := envBlock(badChild)
		if err == nil {
			t.Fatalf("expected error for embedded NUL in SYSTEMROOT, got nil (block: %v)", block)
		}
		if ptr != nil || block != nil {
			t.Fatalf("expected nil ptr and block on error")
		}
	})
}

func TestWindowsLaunchLifetimeContract(t *testing.T) {
	t.Run("FullTeardownOrder", func(t *testing.T) {
		sid := new(windows.SID)
		caps := &securityCapabilities{AppContainerSid: sid, CapabilityCount: 0}
		handles := []windows.Handle{windows.Handle(10), windows.Handle(20)}
		list := make([]byte, 64)

		var order []string
		var releasedSID *windows.SID

		lifetime := &launchLifetime{
			list:    list,
			caps:    caps,
			handles: handles,
			sid:     sid,
			deleteList: func() error {
				order = append(order, "delete_list")
				return nil
			},
			unpin: func() {
				order = append(order, "unpin")
			},
			releaseSID: func(s *windows.SID) error {
				order = append(order, "release_sid")
				releasedSID = s
				return nil
			},
		}

		if err := lifetime.Teardown(); err != nil {
			t.Fatalf("unexpected error during teardown: %v", err)
		}

		wantOrder := []string{"delete_list", "unpin", "release_sid"}
		if !slices.Equal(order, wantOrder) {
			t.Fatalf("teardown order mismatch: got %v, want %v", order, wantOrder)
		}
		if releasedSID != sid {
			t.Fatalf("released SID pointer mismatch")
		}
	})

	t.Run("TeardownOnPartialInit", func(t *testing.T) {
		sid := new(windows.SID)
		var order []string
		lifetime := &launchLifetime{
			sid: sid,
			releaseSID: func(s *windows.SID) error {
				order = append(order, "release_sid")
				return nil
			},
		}
		if err := lifetime.Teardown(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !slices.Equal(order, []string{"release_sid"}) {
			t.Fatalf("expected only release_sid, got %v", order)
		}

		order = nil
		lifetime = &launchLifetime{
			list: make([]byte, 32),
			sid:  sid,
			deleteList: func() error {
				order = append(order, "delete_list")
				return nil
			},
			releaseSID: func(s *windows.SID) error {
				order = append(order, "release_sid")
				return nil
			},
		}
		if err := lifetime.Teardown(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !slices.Equal(order, []string{"delete_list", "release_sid"}) {
			t.Fatalf("expected delete_list then release_sid, got %v", order)
		}
	})

	t.Run("TeardownDeleteErrorStillUnpinsAndReleases", func(t *testing.T) {
		sid := new(windows.SID)
		var order []string
		delErr := errors.New("delete attribute list failed")

		lifetime := &launchLifetime{
			list: make([]byte, 32),
			sid:  sid,
			deleteList: func() error {
				order = append(order, "delete_list")
				return delErr
			},
			unpin: func() {
				order = append(order, "unpin")
			},
			releaseSID: func(s *windows.SID) error {
				order = append(order, "release_sid")
				return nil
			},
		}

		err := lifetime.Teardown()
		if !errors.Is(err, delErr) {
			t.Fatalf("expected delete error, got: %v", err)
		}
		wantOrder := []string{"delete_list", "unpin", "release_sid"}
		if !slices.Equal(order, wantOrder) {
			t.Fatalf("teardown did not continue on delete error: got %v, want %v", order, wantOrder)
		}
	})

	t.Run("TeardownIdempotent", func(t *testing.T) {
		sid := new(windows.SID)
		callCount := 0
		lifetime := &launchLifetime{
			sid: sid,
			releaseSID: func(s *windows.SID) error {
				callCount++
				return nil
			},
		}
		if err := lifetime.Teardown(); err != nil {
			t.Fatalf("first teardown failed: %v", err)
		}
		if err := lifetime.Teardown(); err != nil {
			t.Fatalf("second teardown failed: %v", err)
		}
		if callCount != 1 {
			t.Fatalf("expected exactly 1 release call, got %d", callCount)
		}
	})
}

func TestPreparedStartErrorContract(t *testing.T) {
	t.Run("PreservesErrRefusedAndUnderlyingErrno", func(t *testing.T) {
		underlyingErrno := windows.Errno(203)
		innerErr := fmt.Errorf("BLOCKED: CreateProcess AppContainer: (errno %d) %w", underlyingErrno, underlyingErrno)

		got := composeStartError(innerErr)
		if got == nil {
			t.Fatal("expected non-nil error")
		}
		if !errors.Is(got, ErrRefused) {
			t.Fatalf("error does not wrap ErrRefused: %v", got)
		}
		if !errors.Is(got, underlyingErrno) {
			t.Fatalf("error does not wrap underlying errno 203: %v", got)
		}
		var extracted windows.Errno
		if !errors.As(got, &extracted) {
			t.Fatalf("errors.As failed to extract windows.Errno from %v", got)
		}
		if extracted != underlyingErrno {
			t.Fatalf("extracted errno mismatch: got %d, want %d", extracted, underlyingErrno)
		}
		if !strings.Contains(got.Error(), "refusing to start child") || !strings.Contains(got.Error(), "(errno 203)") {
			t.Fatalf("missing expected diagnostic in error message: %q", got.Error())
		}
	})

	t.Run("PreservesWrappedWin32Error", func(t *testing.T) {
		underlying := windows.ERROR_ACCESS_DENIED
		innerErr := formatCreateProcessError(underlying)

		got := composeStartError(innerErr)
		if got == nil {
			t.Fatal("expected non-nil error")
		}
		if !errors.Is(got, ErrRefused) {
			t.Fatalf("error does not wrap ErrRefused: %v", got)
		}
		if !errors.Is(got, underlying) {
			t.Fatalf("error does not wrap ERROR_ACCESS_DENIED: %v", got)
		}
		var extracted windows.Errno
		if !errors.As(got, &extracted) || extracted != underlying {
			t.Fatalf("failed to extract errno via errors.As: %v", extracted)
		}
	})

	t.Run("NilErrorReturnsNil", func(t *testing.T) {
		if got := composeStartError(nil); got != nil {
			t.Fatalf("expected nil error for nil input, got: %v", got)
		}
	})

	t.Run("PreservesPlainError", func(t *testing.T) {
		plain := errors.New("custom launch failure")
		got := composeStartError(plain)
		if !errors.Is(got, ErrRefused) {
			t.Fatalf("error does not wrap ErrRefused: %v", got)
		}
		if !errors.Is(got, plain) {
			t.Fatalf("error does not wrap plain error: %v", got)
		}
		if !strings.Contains(got.Error(), "custom launch failure") {
			t.Fatalf("diagnostic message lost: %v", got)
		}
	})
}

func TestWindowsConfirmInheritanceContract(t *testing.T) {
	t.Run("SuccessInheritanceSemanticsAndContinuation", func(t *testing.T) {
		type setterCall struct {
			handle windows.Handle
			mask   uint32
			flags  uint32
		}
		var calls []setterCall
		fakeSetter := func(h windows.Handle, mask, flags uint32) error {
			calls = append(calls, setterCall{handle: h, mask: mask, flags: flags})
			return nil
		}

		continuationCalled := false
		continuation := func() error {
			continuationCalled = true
			return nil
		}

		wHandle := windows.Handle(101)
		rHandle := windows.Handle(202)

		err := configureConfirmInheritance(wHandle, true, rHandle, true, fakeSetter, continuation)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !continuationCalled {
			t.Fatal("expected continuation to be called on success")
		}
		if len(calls) != 2 {
			t.Fatalf("expected 2 setter calls, got %d", len(calls))
		}
		if calls[0] != (setterCall{handle: 101, mask: windows.HANDLE_FLAG_INHERIT, flags: windows.HANDLE_FLAG_INHERIT}) {
			t.Fatalf("write handle call mismatch: %+v", calls[0])
		}
		if calls[1] != (setterCall{handle: 202, mask: windows.HANDLE_FLAG_INHERIT, flags: 0}) {
			t.Fatalf("read handle call mismatch: %+v", calls[1])
		}
	})

	t.Run("FailClosedOnWriteSetterFailure", func(t *testing.T) {
		fakeErr := windows.ERROR_INVALID_HANDLE
		setterCalled := 0
		fakeSetter := func(h windows.Handle, mask, flags uint32) error {
			setterCalled++
			return fakeErr
		}

		continuationCalled := false
		continuation := func() error {
			continuationCalled = true
			return nil
		}

		err := configureConfirmInheritance(windows.Handle(101), true, windows.Handle(202), true, fakeSetter, continuation)
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if continuationCalled {
			t.Fatal("continuation MUST NOT be called after setter failure")
		}
		if setterCalled != 1 {
			t.Fatalf("setter should have stopped after first failure, got %d calls", setterCalled)
		}
		if !errors.Is(err, fakeErr) {
			t.Fatalf("error does not wrap native cause: %v", err)
		}
		var errno windows.Errno
		if !errors.As(err, &errno) || errno != 6 {
			t.Fatalf("errors.As failed to extract errno 6: %v", errno)
		}
		if !strings.Contains(err.Error(), "(errno 6)") {
			t.Fatalf("error message missing numeric diagnostic: %q", err.Error())
		}
	})

	t.Run("FailClosedOnReadSetterFailure", func(t *testing.T) {
		fakeErr := windows.ERROR_ACCESS_DENIED
		setterCalled := 0
		fakeSetter := func(h windows.Handle, mask, flags uint32) error {
			setterCalled++
			if setterCalled == 2 {
				return fakeErr
			}
			return nil
		}

		continuationCalled := false
		continuation := func() error {
			continuationCalled = true
			return nil
		}

		err := configureConfirmInheritance(windows.Handle(101), true, windows.Handle(202), true, fakeSetter, continuation)
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if continuationCalled {
			t.Fatal("continuation MUST NOT be called after read setter failure")
		}
		if setterCalled != 2 {
			t.Fatalf("expected 2 setter calls, got %d", setterCalled)
		}
		if !errors.Is(err, fakeErr) {
			t.Fatalf("error does not wrap native cause: %v", err)
		}
		var errno windows.Errno
		if !errors.As(err, &errno) || errno != 5 {
			t.Fatalf("errors.As failed to extract errno 5: %v", errno)
		}
	})

	t.Run("OptionalHandles", func(t *testing.T) {
		continuationCalled := false
		err := configureConfirmInheritance(0, false, 0, false, nil, func() error {
			continuationCalled = true
			return nil
		})
		if err != nil || !continuationCalled {
			t.Fatalf("neither handle present failed: err=%v, called=%v", err, continuationCalled)
		}

		calls := 0
		fakeSetter := func(h windows.Handle, mask, flags uint32) error {
			calls++
			return nil
		}
		continuationCalled = false
		err = configureConfirmInheritance(windows.Handle(101), true, 0, false, fakeSetter, func() error {
			continuationCalled = true
			return nil
		})
		if err != nil || !continuationCalled || calls != 1 {
			t.Fatalf("only write handle failed: err=%v, called=%v, calls=%d", err, continuationCalled, calls)
		}
	})
}

func TestWindowsLaunchMetadataContract(t *testing.T) {
	// Approved closed set of exactly 28 metadata JSON keys
	approvedKeys := map[string]string{
		"creation_flags":                  "number",
		"startup_info_cb":                 "number",
		"startup_info_ex_size":            "number",
		"security_capabilities_size":      "number",
		"populated_attribute_count":       "number",
		"security_capabilities_attr_size": "number",
		"handle_list_attr_size":           "number",
		"handle_list_count":               "number",
		"capability_count":                "number",
		"capability_reserved":             "number",
		"sid_present":                     "boolean",
		"env_pointer_provided":            "boolean",
		"env_utf16_unit_count":            "number",
		"env_double_nul_valid":            "boolean",
		"env_structure_valid":             "boolean",
		"env_sorted":                      "boolean",
		"parent_systemroot_present":       "boolean",
		"parent_systemroot_nonempty":      "boolean",
		"parent_localappdata_present":     "boolean",
		"parent_localappdata_nonempty":    "boolean",
		"child_systemroot_any_present":    "boolean",
		"child_systemroot_any_nonempty":   "boolean",
		"child_localappdata_any_present":  "boolean",
		"child_localappdata_any_nonempty": "boolean",
		"app_path_category":               "string",
		"app_path_utf16_length":           "number",
		"current_dir_category":            "string",
		"current_dir_utf16_length":        "number",
	}

	t.Run("ClosedKeyAllowlistAndValueTypes", func(t *testing.T) {
		meta := &WindowsLaunchMetadata{
			CreationFlags:                0x08000404,
			StartupInfoCb:                112,
			StartupInfoExSize:            112,
			SecurityCapabilitiesSize:     32,
			PopulatedAttributeCount:      2,
			SecurityCapabilitiesAttrSize: 32,
			HandleListAttrSize:           24,
			HandleListCount:              3,
			CapabilityCount:              0,
			CapabilityReserved:           0,
			SIDPresent:                   true,
			EnvPointerProvided:           true,
			EnvUTF16UnitCount:            64,
			EnvDoubleNULValid:            true,
			EnvStructureValid:            true,
			EnvSorted:                    true,
			ParentSystemRootPresent:      true,
			ParentSystemRootNonEmpty:     true,
			ParentLocalAppDataPresent:    true,
			ParentLocalAppDataNonEmpty:   true,
			ChildSystemRootAnyPresent:    true,
			ChildSystemRootAnyNonEmpty:   true,
			ChildLocalAppDataAnyPresent:  false,
			ChildLocalAppDataAnyNonEmpty: false,
			AppPathCategory:              PathCategoryDriveAbsolute,
			AppPathUTF16Length:           28,
			CurrentDirCategory:           PathCategoryEmpty,
			CurrentDirUTF16Length:        0,
		}

		raw, err := json.Marshal(meta)
		if err != nil {
			t.Fatalf("json.Marshal failed: %v", err)
		}

		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("json.Unmarshal failed: %v", err)
		}

		if len(m) != len(approvedKeys) {
			t.Fatalf("key count mismatch: got %d keys, want %d approved keys", len(m), len(approvedKeys))
		}

		for k, val := range m {
			expectedType, ok := approvedKeys[k]
			if !ok {
				t.Fatalf("unapproved key present in metadata: %s", k)
			}
			switch expectedType {
			case "number":
				if _, ok := val.(float64); !ok {
					t.Fatalf("field %s expected number, got %T", k, val)
				}
			case "boolean":
				if _, ok := val.(bool); !ok {
					t.Fatalf("field %s expected boolean, got %T", k, val)
				}
			case "string":
				s, ok := val.(string)
				if !ok {
					t.Fatalf("field %s expected string, got %T", k, val)
				}
				switch WindowsPathCategory(s) {
				case PathCategoryEmpty, PathCategoryRelative, PathCategoryDriveAbsolute,
					PathCategoryDriveRelative, PathCategoryRootRelative, PathCategoryUNC,
					PathCategoryNTDevice, PathCategoryUnknown:
				default:
					t.Fatalf("field %s contains unapproved category enum: %s", k, s)
				}
			}
		}
	})

	t.Run("ActualFlagsSizesCountsReflected", func(t *testing.T) {
		si := &startupInfoEx{}
		si.StartupInfo.Cb = uint32(unsafe.Sizeof(*si))
		caps := &securityCapabilities{
			AppContainerSid: new(windows.SID),
			CapabilityCount: 5,
			Reserved:        9,
		}
		handles := []windows.Handle{10, 20, 30, 40}
		rawEnv := []uint16{'A', '=', '1', 0, 0}
		lookup := func(key string) (string, bool) {
			if strings.EqualFold(key, "SYSTEMROOT") {
				return "C:\\Windows", true
			}
			return "", false
		}

		meta := captureWindowsLaunchMetadata(
			0x08000404,
			si,
			caps,
			handles,
			&rawEnv[0],
			rawEnv,
			"C:\\bin\\app.exe",
			"",
			lookup,
		)

		if meta.CreationFlags != 0x08000404 {
			t.Fatalf("CreationFlags mismatch: got 0x%x, want 0x08000404", meta.CreationFlags)
		}
		if meta.StartupInfoCb != uint32(unsafe.Sizeof(*si)) {
			t.Fatalf("StartupInfoCb mismatch: got %d, want %d", meta.StartupInfoCb, unsafe.Sizeof(*si))
		}
		if meta.StartupInfoExSize != uint32(unsafe.Sizeof(*si)) {
			t.Fatalf("StartupInfoExSize mismatch: got %d, want %d", meta.StartupInfoExSize, unsafe.Sizeof(*si))
		}
		if meta.SecurityCapabilitiesSize != uint32(unsafe.Sizeof(*caps)) {
			t.Fatalf("SecurityCapabilitiesSize mismatch: got %d, want %d", meta.SecurityCapabilitiesSize, unsafe.Sizeof(*caps))
		}
		if meta.PopulatedAttributeCount != 2 {
			t.Fatalf("PopulatedAttributeCount mismatch: got %d, want 2", meta.PopulatedAttributeCount)
		}
		if meta.SecurityCapabilitiesAttrSize != uint32(unsafe.Sizeof(*caps)) {
			t.Fatalf("SecurityCapabilitiesAttrSize mismatch: got %d, want %d", meta.SecurityCapabilitiesAttrSize, unsafe.Sizeof(*caps))
		}
		expectedHandleAttrSize := uint32(uintptr(len(handles)) * unsafe.Sizeof(handles[0]))
		if meta.HandleListAttrSize != expectedHandleAttrSize {
			t.Fatalf("HandleListAttrSize mismatch: got %d, want %d", meta.HandleListAttrSize, expectedHandleAttrSize)
		}
		if meta.HandleListCount != 4 {
			t.Fatalf("HandleListCount mismatch: got %d, want 4", meta.HandleListCount)
		}
		if meta.CapabilityCount != 5 {
			t.Fatalf("CapabilityCount mismatch: got %d, want 5", meta.CapabilityCount)
		}
		if meta.CapabilityReserved != 9 {
			t.Fatalf("CapabilityReserved mismatch: got %d, want 9", meta.CapabilityReserved)
		}
		if !meta.SIDPresent {
			t.Fatalf("SIDPresent expected true, got false")
		}
	})

	t.Run("ParentChildSeparated", func(t *testing.T) {
		// Parent has SYSTEMROOT and LOCALAPPDATA; Child has neither
		parentLookup := func(k string) (string, bool) {
			if strings.EqualFold(k, "SYSTEMROOT") {
				return "C:\\Windows", true
			}
			if strings.EqualFold(k, "LOCALAPPDATA") {
				return "C:\\Users\\user\\AppData\\Local", true
			}
			return "", false
		}

		childEntries := []string{"CUSTOM_VAR=value"}
		_, childBlock, err := envBlock(childEntries)
		if err != nil {
			t.Fatalf("envBlock failed: %v", err)
		}

		meta := captureWindowsLaunchMetadata(
			0, nil, nil, nil, &childBlock[0], childBlock, "app.exe", "", parentLookup,
		)

		if !meta.ParentSystemRootPresent || !meta.ParentSystemRootNonEmpty {
			t.Fatalf("parent SYSTEMROOT presence/nonempty failed")
		}
		if !meta.ParentLocalAppDataPresent || !meta.ParentLocalAppDataNonEmpty {
			t.Fatalf("parent LOCALAPPDATA presence/nonempty failed")
		}
		if meta.ChildSystemRootAnyPresent || meta.ChildSystemRootAnyNonEmpty {
			t.Fatalf("child SYSTEMROOT unexpectedly marked present/nonempty")
		}
		if meta.ChildLocalAppDataAnyPresent || meta.ChildLocalAppDataAnyNonEmpty {
			t.Fatalf("child LOCALAPPDATA unexpectedly marked present/nonempty")
		}

		// Reverse: Parent has neither; Child has both
		emptyParent := func(k string) (string, bool) { return "", false }
		childWithBoth := []string{"LOCALAPPDATA=C:\\ChildAppData", "SYSTEMROOT=C:\\ChildWindows"}
		_, childBlockBoth, err := envBlock(childWithBoth)
		if err != nil {
			t.Fatalf("envBlock failed: %v", err)
		}

		meta2 := captureWindowsLaunchMetadata(
			0, nil, nil, nil, &childBlockBoth[0], childBlockBoth, "app.exe", "", emptyParent,
		)

		if meta2.ParentSystemRootPresent || meta2.ParentSystemRootNonEmpty {
			t.Fatalf("parent SYSTEMROOT unexpectedly marked present")
		}
		if meta2.ParentLocalAppDataPresent || meta2.ParentLocalAppDataNonEmpty {
			t.Fatalf("parent LOCALAPPDATA unexpectedly marked present")
		}
		if !meta2.ChildSystemRootAnyPresent || !meta2.ChildSystemRootAnyNonEmpty {
			t.Fatalf("child SYSTEMROOT expected present and nonempty")
		}
		if !meta2.ChildLocalAppDataAnyPresent || !meta2.ChildLocalAppDataAnyNonEmpty {
			t.Fatalf("child LOCALAPPDATA expected present and nonempty")
		}
	})

	t.Run("FixedKeyPresenceAndNonEmptyVariants", func(t *testing.T) {
		// Missing, empty, and non-empty for parent and child
		variants := []struct {
			name        string
			parentVal   string
			parentFound bool
			childVal    string
			childFound  bool
			wantPPres   bool
			wantPNonEmp bool
			wantCPres   bool
			wantCNonEmp bool
		}{
			{"BothMissing", "", false, "", false, false, false, false, false},
			{"ParentEmptyChildMissing", "", true, "", false, true, false, false, false},
			{"ParentNonEmptyChildMissing", "C:\\Sys", true, "", false, true, true, false, false},
			{"ParentMissingChildEmpty", "", false, "SYSTEMROOT=", true, false, false, true, false},
			{"ParentMissingChildNonEmpty", "", false, "SYSTEMROOT=C:\\Sys", true, false, false, true, true},
			{"BothEmpty", "", true, "SYSTEMROOT=", true, true, false, true, false},
			{"BothNonEmpty", "C:\\ParentSys", true, "SYSTEMROOT=C:\\ChildSys", true, true, true, true, true},
		}

		for _, v := range variants {
			t.Run(v.name, func(t *testing.T) {
				pLookup := func(k string) (string, bool) {
					if strings.EqualFold(k, "SYSTEMROOT") {
						return v.parentVal, v.parentFound
					}
					return "", false
				}
				var cBlock []uint16
				if v.childFound {
					_, b, err := envBlock([]string{v.childVal})
					if err != nil {
						t.Fatalf("envBlock failed: %v", err)
					}
					cBlock = b
				} else {
					_, b, err := envBlock([]string{"OTHER=1"})
					if err != nil {
						t.Fatalf("envBlock failed: %v", err)
					}
					cBlock = b
				}

				meta := captureWindowsLaunchMetadata(
					0, nil, nil, nil, &cBlock[0], cBlock, "app.exe", "", pLookup,
				)

				if meta.ParentSystemRootPresent != v.wantPPres {
					t.Fatalf("ParentSystemRootPresent mismatch: got %v, want %v", meta.ParentSystemRootPresent, v.wantPPres)
				}
				if meta.ParentSystemRootNonEmpty != v.wantPNonEmp {
					t.Fatalf("ParentSystemRootNonEmpty mismatch: got %v, want %v", meta.ParentSystemRootNonEmpty, v.wantPNonEmp)
				}
				if meta.ChildSystemRootAnyPresent != v.wantCPres {
					t.Fatalf("ChildSystemRootAnyPresent mismatch: got %v, want %v", meta.ChildSystemRootAnyPresent, v.wantCPres)
				}
				if meta.ChildSystemRootAnyNonEmpty != v.wantCNonEmp {
					t.Fatalf("ChildSystemRootAnyNonEmpty mismatch: got %v, want %v", meta.ChildSystemRootAnyNonEmpty, v.wantCNonEmp)
				}
			})
		}
	})

	t.Run("FixedKeyCaseAliasesAndDuplicateSemantics", func(t *testing.T) {
		// Case aliases: systemroot, SystemRoot, sYsTeMrOoT
		// Duplicate semantics: any-present / any-nonempty
		childWithDup := []string{
			"systemroot=",                // empty
			"SystemRoot=C:\\WinNonEmpty", // nonempty
			"LOCALAPPDATA=",              // empty
			"localappdata=",              // empty
		}
		_, b, err := envBlock(childWithDup)
		if err != nil {
			t.Fatalf("envBlock failed: %v", err)
		}

		meta := captureWindowsLaunchMetadata(
			0, nil, nil, nil, &b[0], b, "app.exe", "", nil,
		)

		if !meta.ChildSystemRootAnyPresent {
			t.Fatalf("ChildSystemRootAnyPresent expected true")
		}
		if !meta.ChildSystemRootAnyNonEmpty {
			t.Fatalf("ChildSystemRootAnyNonEmpty expected true due to any-nonempty duplicate rule")
		}
		if !meta.ChildLocalAppDataAnyPresent {
			t.Fatalf("ChildLocalAppDataAnyPresent expected true")
		}
		if meta.ChildLocalAppDataAnyNonEmpty {
			t.Fatalf("ChildLocalAppDataAnyNonEmpty expected false when all matching duplicates are empty")
		}
	})

	t.Run("DoubleNULAndMalformedBuffer", func(t *testing.T) {
		// 1. Valid double NUL
		validBlock := []uint16{'F', 'O', 'O', '=', '1', 0, 0}
		m1 := captureWindowsLaunchMetadata(0, nil, nil, nil, &validBlock[0], validBlock, "app.exe", "", nil)
		if !m1.EnvDoubleNULValid || !m1.EnvStructureValid {
			t.Fatalf("valid block marked invalid: doubleNul=%v, struct=%v", m1.EnvDoubleNULValid, m1.EnvStructureValid)
		}

		// 2. Missing double NUL (single NUL at end)
		singleNUL := []uint16{'F', 'O', 'O', '=', '1', 0}
		m2 := captureWindowsLaunchMetadata(0, nil, nil, nil, &singleNUL[0], singleNUL, "app.exe", "", nil)
		if m2.EnvDoubleNULValid || m2.EnvStructureValid {
			t.Fatalf("single NUL unexpectedly marked valid")
		}

		// 3. No NUL at end
		noNUL := []uint16{'F', 'O', 'O', '=', '1'}
		m3 := captureWindowsLaunchMetadata(0, nil, nil, nil, &noNUL[0], noNUL, "app.exe", "", nil)
		if m3.EnvDoubleNULValid || m3.EnvStructureValid {
			t.Fatalf("no NUL unexpectedly marked valid")
		}

		// 4. Malformed entry without '='
		noEquals := []uint16{'N', 'O', 'E', 'Q', 'U', 'A', 'L', 'S', 0, 0}
		m4 := captureWindowsLaunchMetadata(0, nil, nil, nil, &noEquals[0], noEquals, "app.exe", "", nil)
		if !m4.EnvDoubleNULValid {
			t.Fatalf("expected double NUL to be recognized even if entry malformed")
		}
		if m4.EnvStructureValid {
			t.Fatalf("malformed entry without '=' marked structure valid")
		}

		// 5. Nil block
		m5 := captureWindowsLaunchMetadata(0, nil, nil, nil, nil, nil, "app.exe", "", nil)
		if m5.EnvPointerProvided || m5.EnvUTF16UnitCount != 0 || m5.EnvDoubleNULValid || m5.EnvStructureValid {
			t.Fatalf("nil block marked present or valid")
		}
	})

	t.Run("SortAndStructureChecks", func(t *testing.T) {
		// Sorted entries: ALPHA < BETA < GAMMA
		_, sortedBlock, err := envBlock([]string{"ALPHA=1", "BETA=2", "GAMMA=3"})
		if err != nil {
			t.Fatalf("envBlock failed: %v", err)
		}
		mSorted := captureWindowsLaunchMetadata(0, nil, nil, nil, &sortedBlock[0], sortedBlock, "app.exe", "", nil)
		if !mSorted.EnvSorted || !mSorted.EnvStructureValid {
			t.Fatalf("sorted block marked unsorted or invalid: sorted=%v, struct=%v", mSorted.EnvSorted, mSorted.EnvStructureValid)
		}

		// Unsorted entries: BETA then ALPHA
		unsortedBlock := []uint16{
			'B', 'E', 'T', 'A', '=', '2', 0,
			'A', 'L', 'P', 'H', 'A', '=', '1', 0,
			0,
		}
		mUnsorted := captureWindowsLaunchMetadata(0, nil, nil, nil, &unsortedBlock[0], unsortedBlock, "app.exe", "", nil)
		if mUnsorted.EnvSorted {
			t.Fatalf("unsorted block unexpectedly marked sorted")
		}
		if !mUnsorted.EnvStructureValid {
			t.Fatalf("unsorted block unexpectedly marked structure invalid")
		}
	})

	t.Run("PathCategoryAndUTF16Length", func(t *testing.T) {
		cases := []struct {
			path     string
			wantCat  WindowsPathCategory
			wantULen int
		}{
			{"", PathCategoryEmpty, 0},
			{"app.exe", PathCategoryRelative, 7},
			{".\\sub\\app.exe", PathCategoryRelative, 13},
			{"C:\\Windows\\System32\\cmd.exe", PathCategoryDriveAbsolute, 27},
			{"c:/windows/system32/cmd.exe", PathCategoryDriveAbsolute, 27},
			{"C:relative.exe", PathCategoryDriveRelative, 14},
			{"\\Windows\\cmd.exe", PathCategoryRootRelative, 16},
			{"\\\\server\\share\\tool.exe", PathCategoryUNC, 23},
			{"\\\\?\\C:\\tools\\app.exe", PathCategoryNTDevice, 20},
			{"\\??\\C:\\tools\\app.exe", PathCategoryNTDevice, 20},
			{"\\\\.\\COM1", PathCategoryNTDevice, 8},
		}

		for _, tc := range cases {
			t.Run(string(tc.wantCat)+"_"+tc.path, func(t *testing.T) {
				meta := captureWindowsLaunchMetadata(0, nil, nil, nil, nil, nil, tc.path, tc.path, nil)
				if meta.AppPathCategory != tc.wantCat {
					t.Fatalf("AppPathCategory mismatch: got %q, want %q", meta.AppPathCategory, tc.wantCat)
				}
				if meta.AppPathUTF16Length != tc.wantULen {
					t.Fatalf("AppPathUTF16Length mismatch: got %d, want %d", meta.AppPathUTF16Length, tc.wantULen)
				}
				if meta.CurrentDirCategory != tc.wantCat {
					t.Fatalf("CurrentDirCategory mismatch: got %q, want %q", meta.CurrentDirCategory, tc.wantCat)
				}
				if meta.CurrentDirUTF16Length != tc.wantULen {
					t.Fatalf("CurrentDirUTF16Length mismatch: got %d, want %d", meta.CurrentDirUTF16Length, tc.wantULen)
				}
			})
		}
	})

	t.Run("InputImmutability", func(t *testing.T) {
		handles := []windows.Handle{windows.Handle(100), windows.Handle(200)}
		handlesCopy := slices.Clone(handles)

		block := []uint16{'K', 'E', 'Y', '=', 'V', 'A', 'L', 0, 0}
		blockCopy := slices.Clone(block)

		si := &startupInfoEx{}
		si.StartupInfo.Cb = 112
		siCopy := *si

		caps := &securityCapabilities{CapabilityCount: 2, Reserved: 4}
		capsCopy := *caps

		_ = captureWindowsLaunchMetadata(0x08000404, si, caps, handles, &block[0], block, "C:\\test\\app.exe", "C:\\test", nil)

		if !slices.Equal(handles, handlesCopy) {
			t.Fatalf("handles slice mutated by metadata capture")
		}
		if !slices.Equal(block, blockCopy) {
			t.Fatalf("environment block mutated by metadata capture")
		}
		if *si != siCopy {
			t.Fatalf("startupInfoEx mutated by metadata capture")
		}
		if *caps != capsCopy {
			t.Fatalf("securityCapabilities mutated by metadata capture")
		}
	})

	t.Run("NoSecretLeakageInJSON", func(t *testing.T) {
		// Provide inputs with synthetic secret tokens, passwords, accounts, and full paths
		secretToken := "s3cr3t_token_val_12345"
		secretPassword := "pass_phrase_hidden_67890"
		secretAccount := "secret_admin_user_9999"
		secretPath := "C:\\SensitiveCorpDirectory\\TopSecret\\app.exe"
		secretDir := "D:\\ClassifiedWorkDir\\ProjectX"

		envEntries := []string{
			"SECRET_TOKEN=" + secretToken,
			"SECRET_PASSWORD=" + secretPassword,
			"ACCOUNT=" + secretAccount,
			"SYSTEMROOT=C:\\Windows",
		}
		_, b, err := envBlock(envEntries)
		if err != nil {
			t.Fatalf("envBlock failed: %v", err)
		}

		parentLookup := func(k string) (string, bool) {
			if strings.EqualFold(k, "SYSTEMROOT") {
				return "C:\\Windows", true
			}
			return "", false
		}

		meta := captureWindowsLaunchMetadata(
			0x08000000,
			nil,
			nil,
			nil,
			&b[0],
			b,
			secretPath,
			secretDir,
			parentLookup,
		)

		rawJSON, err := json.Marshal(meta)
		if err != nil {
			t.Fatalf("json.Marshal failed: %v", err)
		}

		jsonStr := string(rawJSON)

		// Assert that none of the secrets appear in serialized metadata
		// Rule: Failure message MUST NOT echo the secret tokens or entire JSON
		if strings.Contains(jsonStr, secretToken) {
			t.Fatalf("security check failed: secret token leaked in metadata JSON")
		}
		if strings.Contains(jsonStr, secretPassword) {
			t.Fatalf("security check failed: secret password leaked in metadata JSON")
		}
		if strings.Contains(jsonStr, secretAccount) {
			t.Fatalf("security check failed: secret account name leaked in metadata JSON")
		}
		if strings.Contains(jsonStr, "SensitiveCorpDirectory") {
			t.Fatalf("security check failed: secret application path leaked in metadata JSON")
		}
		if strings.Contains(jsonStr, "ClassifiedWorkDir") {
			t.Fatalf("security check failed: secret current directory leaked in metadata JSON")
		}
	})
}

func TestWindowsLocalAppDataContract(t *testing.T) {
	t.Run("OwnedACRootSource", func(t *testing.T) {
		rec := &RunRecord{Root: `C:\AppContainer\Owned\Root`}
		input := []string{"PATH=C:\\bin"}
		got, err := replenishChildLocalAppData(input, rec)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		found := false
		var foundVal string
		for _, kv := range got {
			k, v, ok := strings.Cut(kv, "=")
			if ok && strings.EqualFold(k, "LOCALAPPDATA") {
				found = true
				foundVal = v
				break
			}
		}
		if !found {
			t.Fatalf("expected LOCALAPPDATA in child environment, got: %v", got)
		}
		if foundVal != rec.Root {
			t.Fatalf("LOCALAPPDATA value mismatch: got %q, want %q", foundVal, rec.Root)
		}
	})

	t.Run("NoHostLookup", func(t *testing.T) {
		rec := &RunRecord{Root: `C:\Synthetic\Package\AC`}
		got, err := replenishChildLocalAppData([]string{"PATH=C:\\bin"}, rec)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		for _, kv := range got {
			k, v, ok := strings.Cut(kv, "=")
			if ok && strings.EqualFold(k, "LOCALAPPDATA") {
				if v != rec.Root {
					t.Fatalf("LOCALAPPDATA took value from non-record source: got %q, want %q", v, rec.Root)
				}
			}
		}
	})

	t.Run("ExactOneKeyAddition", func(t *testing.T) {
		rec := &RunRecord{Root: `C:\Owned\ACRoot`}
		input := []string{"VAR1=A", "VAR2=B"}
		got, err := replenishChildLocalAppData(input, rec)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 3 {
			t.Fatalf("expected exactly 3 entries, got %d: %v", len(got), got)
		}
		want := []string{"VAR1=A", "VAR2=B", "LOCALAPPDATA=C:\\Owned\\ACRoot"}
		if !slices.Equal(got, want) {
			t.Fatalf("output mismatch: got %v, want %v", got, want)
		}
	})

	t.Run("NilEmptyNonemptyChild", func(t *testing.T) {
		rec := &RunRecord{Root: `C:\Owned\ACRoot`}
		for _, tc := range []struct {
			name  string
			child []string
			want  []string
		}{
			{"NilChild", nil, []string{"LOCALAPPDATA=C:\\Owned\\ACRoot"}},
			{"EmptyChild", []string{}, []string{"LOCALAPPDATA=C:\\Owned\\ACRoot"}},
			{"NonEmptyChild", []string{"PATH=C:\\bin"}, []string{"PATH=C:\\bin", "LOCALAPPDATA=C:\\Owned\\ACRoot"}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				got, err := replenishChildLocalAppData(tc.child, rec)
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if !slices.Equal(got, tc.want) {
					t.Fatalf("got %v, want %v", got, tc.want)
				}
			})
		}
	})

	t.Run("InputAndSpareCapacityImmutability", func(t *testing.T) {
		rec := &RunRecord{Root: `C:\Owned\ACRoot`}
		backing := make([]string, 2, 8)
		backing[0] = "KEY1=V1"
		backing[1] = "KEY2=V2"
		input := backing[:2]

		origBacking := make([]string, 8)
		copy(origBacking, backing[:8])

		got, err := replenishChildLocalAppData(input, rec)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(input) != 2 {
			t.Fatalf("input slice length mutated: got %d, want 2", len(input))
		}
		if !slices.Equal(backing[:8], origBacking) {
			t.Fatalf("input backing array mutated: got %v, want %v", backing[:8], origBacking)
		}
		if len(got) != 3 {
			t.Fatalf("expected 3 entries in output, got %d", len(got))
		}
	})

	t.Run("CaseAliasesPreservedWithoutDuplication", func(t *testing.T) {
		rec := &RunRecord{Root: `C:\Owned\ACRoot`}
		aliases := []string{
			"LOCALAPPDATA=C:\\Upper",
			"localappdata=C:\\Lower",
			"LocalAppData=C:\\Mixed",
			"lOcAlApPdAtA=C:\\Weird",
		}
		for _, alias := range aliases {
			input := []string{"PATH=C:\\bin", alias}
			got, err := replenishChildLocalAppData(input, rec)
			if err != nil {
				t.Fatalf("unexpected error for alias %q: %v", alias, err)
			}
			if len(got) != 2 {
				t.Fatalf("expected 2 entries for alias %q, got %d: %v", alias, len(got), got)
			}
			found := 0
			for _, kv := range got {
				k, _, ok := strings.Cut(kv, "=")
				if ok && strings.EqualFold(k, "LOCALAPPDATA") {
					found++
					if kv != alias {
						t.Fatalf("alias entry modified: got %q, want %q", kv, alias)
					}
				}
			}
			if found != 1 {
				t.Fatalf("expected exactly 1 LOCALAPPDATA entry, got %d", found)
			}
		}
	})

	t.Run("ExplicitEmptyAndNonEmptyRetention", func(t *testing.T) {
		rec := &RunRecord{Root: `C:\Owned\ACRoot`}
		cases := []string{"LOCALAPPDATA=", "localappdata=", "LocalAppData=", "LOCALAPPDATA=C:\\Explicit"}
		for _, explicit := range cases {
			input := []string{"VAR=1", explicit}
			got, err := replenishChildLocalAppData(input, rec)
			if err != nil {
				t.Fatalf("unexpected error for %q: %v", explicit, err)
			}
			if len(got) != 2 {
				t.Fatalf("expected 2 entries, got %d: %v", len(got), got)
			}
			found := false
			for _, kv := range got {
				if kv == explicit {
					found = true
				}
			}
			if !found {
				t.Fatalf("explicit entry %q was overwritten: %v", explicit, got)
			}
		}
	})

	t.Run("MissingRootFailclosed", func(t *testing.T) {
		child := []string{"PATH=C:\\bin"}
		for _, badRec := range []*RunRecord{nil, {}, {Root: ""}, {Root: "   "}} {
			got, err := replenishChildLocalAppData(child, badRec)
			if err == nil {
				t.Fatalf("expected error for bad record %+v, got nil (result: %v)", badRec, got)
			}
			if !errors.Is(err, ErrRefused) {
				t.Fatalf("expected error wrapping ErrRefused, got: %v", err)
			}
		}
		explicit := []string{"LOCALAPPDATA=C:\\explicit"}
		got, err := replenishChildLocalAppData(explicit, nil)
		if err != nil {
			t.Fatalf("unexpected error when explicit child already present: %v", err)
		}
		if len(got) != 1 || got[0] != "LOCALAPPDATA=C:\\explicit" {
			t.Fatalf("explicit child mutated: %v", got)
		}
	})

	t.Run("IntegrationUsesRecordRootNotRequestOrHome", func(t *testing.T) {
		rec := &RunRecord{
			Root:        `C:\AC\ControlledPackageFolder`,
			Home:        `C:\AC\ControlledPackageFolder\home`,
			ProfileName: "test-profile-contract",
			PackageSID:  "S-1-15-2-12345-67890",
		}
		p := &Prepared{
			Cmd:      &exec.Cmd{Env: []string{"PATH=C:\\bin"}},
			Confined: true,
			rec:      rec,
		}
		if err := configureConfinedLaunch(p); err != nil {
			t.Fatalf("configureConfinedLaunch error: %v", err)
		}
		found := false
		var foundVal string
		for _, kv := range p.Cmd.Env {
			k, v, ok := strings.Cut(kv, "=")
			if ok && strings.EqualFold(k, "LOCALAPPDATA") {
				found = true
				foundVal = v
				break
			}
		}
		if !found {
			t.Fatalf("LOCALAPPDATA not found in p.Cmd.Env: %v", p.Cmd.Env)
		}
		if foundVal != rec.Root {
			t.Fatalf("LOCALAPPDATA integrated value mismatch: got %q, want rec.Root %q (must not be rec.Home %q)", foundVal, rec.Root, rec.Home)
		}
		if !p.ChildLocalAppDataIsOwnedRoot {
			t.Fatalf("expected ChildLocalAppDataIsOwnedRoot == true")
		}
		if !p.ChildLocalAppDataFromOwnedRoot {
			t.Fatalf("expected ChildLocalAppDataFromOwnedRoot == true")
		}
	})

	t.Run("ProvenanceDistinguishesExplicitFromDefault", func(t *testing.T) {
		rec := &RunRecord{
			Root:        `C:\AC\OwnedRoot`,
			Home:        `C:\AC\OwnedRoot\home`,
			ProfileName: "test-profile-contract",
			PackageSID:  "S-1-15-2-12345-67890",
		}
		pExplicit := &Prepared{
			Cmd:      &exec.Cmd{Env: []string{"LOCALAPPDATA=C:\\other\\appdata"}},
			Confined: true,
			rec:      rec,
		}
		if err := configureConfinedLaunch(pExplicit); err != nil {
			t.Fatalf("configureConfinedLaunch error: %v", err)
		}
		if pExplicit.ChildLocalAppDataIsOwnedRoot {
			t.Fatalf("expected ChildLocalAppDataIsOwnedRoot == false for non-matching explicit path")
		}
		if pExplicit.ChildLocalAppDataFromOwnedRoot {
			t.Fatalf("expected ChildLocalAppDataFromOwnedRoot == false for explicit path")
		}

		pMatching := &Prepared{
			Cmd:      &exec.Cmd{Env: []string{`LOCALAPPDATA=C:\AC\OwnedRoot`}},
			Confined: true,
			rec:      rec,
		}
		if err := configureConfinedLaunch(pMatching); err != nil {
			t.Fatalf("configureConfinedLaunch error: %v", err)
		}
		if !pMatching.ChildLocalAppDataIsOwnedRoot {
			t.Fatalf("expected ChildLocalAppDataIsOwnedRoot == true for matching path")
		}
		if pMatching.ChildLocalAppDataFromOwnedRoot {
			t.Fatalf("expected ChildLocalAppDataFromOwnedRoot == false for explicit path even if matching")
		}
	})

	t.Run("PreserveProvenanceAcrossPrepareAndStartRecheck", func(t *testing.T) {
		rec := &RunRecord{
			Root:        `C:\AC\OwnedRoot`,
			Home:        `C:\AC\OwnedRoot\home`,
			ProfileName: "test-profile-contract",
			PackageSID:  "S-1-15-2-12345-67890",
		}

		// 1. Default-injected LOCALAPPDATA: must remain FromOwnedRoot=true across passes
		pDefault := &Prepared{
			Cmd:      &exec.Cmd{Env: []string{"PATH=C:\\bin"}},
			Confined: true,
			rec:      rec,
		}
		if err := configureConfinedLaunch(pDefault); err != nil {
			t.Fatalf("configureConfinedLaunch error: %v", err)
		}
		if !pDefault.ChildLocalAppDataIsOwnedRoot {
			t.Fatalf("expected ChildLocalAppDataIsOwnedRoot == true after prepare")
		}
		if !pDefault.ChildLocalAppDataFromOwnedRoot {
			t.Fatalf("expected ChildLocalAppDataFromOwnedRoot == true after prepare")
		}

		// Second pass: start recheck pass
		if err := applyChildLocalAppData(pDefault); err != nil {
			t.Fatalf("applyChildLocalAppData error: %v", err)
		}
		if !pDefault.ChildLocalAppDataIsOwnedRoot {
			t.Fatalf("expected ChildLocalAppDataIsOwnedRoot == true after start recheck")
		}
		if !pDefault.ChildLocalAppDataFromOwnedRoot {
			t.Fatalf("regression: default provenance overwritten to false after start recheck")
		}

		// 2. Explicit matching owned root: must remain FromOwnedRoot=false across passes
		pMatching := &Prepared{
			Cmd:      &exec.Cmd{Env: []string{`LOCALAPPDATA=C:\AC\OwnedRoot`}},
			Confined: true,
			rec:      rec,
		}
		if err := configureConfinedLaunch(pMatching); err != nil {
			t.Fatalf("configureConfinedLaunch error: %v", err)
		}
		if !pMatching.ChildLocalAppDataIsOwnedRoot || pMatching.ChildLocalAppDataFromOwnedRoot {
			t.Fatalf("unexpected matching provenance after prepare: is=%t from=%t", pMatching.ChildLocalAppDataIsOwnedRoot, pMatching.ChildLocalAppDataFromOwnedRoot)
		}
		if err := applyChildLocalAppData(pMatching); err != nil {
			t.Fatalf("applyChildLocalAppData error: %v", err)
		}
		if !pMatching.ChildLocalAppDataIsOwnedRoot || pMatching.ChildLocalAppDataFromOwnedRoot {
			t.Fatalf("unexpected matching provenance after start recheck: is=%t from=%t", pMatching.ChildLocalAppDataIsOwnedRoot, pMatching.ChildLocalAppDataFromOwnedRoot)
		}

		// 3. Explicit non-matching: must remain isOwnedRoot=false, fromOwnedRoot=false
		pOther := &Prepared{
			Cmd:      &exec.Cmd{Env: []string{`LOCALAPPDATA=C:\other\dir`}},
			Confined: true,
			rec:      rec,
		}
		if err := configureConfinedLaunch(pOther); err != nil {
			t.Fatalf("configureConfinedLaunch error: %v", err)
		}
		if pOther.ChildLocalAppDataIsOwnedRoot || pOther.ChildLocalAppDataFromOwnedRoot {
			t.Fatalf("unexpected other provenance after prepare: is=%t from=%t", pOther.ChildLocalAppDataIsOwnedRoot, pOther.ChildLocalAppDataFromOwnedRoot)
		}
		if err := applyChildLocalAppData(pOther); err != nil {
			t.Fatalf("applyChildLocalAppData error: %v", err)
		}
		if pOther.ChildLocalAppDataIsOwnedRoot || pOther.ChildLocalAppDataFromOwnedRoot {
			t.Fatalf("unexpected other provenance after start recheck: is=%t from=%t", pOther.ChildLocalAppDataIsOwnedRoot, pOther.ChildLocalAppDataFromOwnedRoot)
		}

		// 4. Explicit empty: must remain isOwnedRoot=false, fromOwnedRoot=false
		pEmpty := &Prepared{
			Cmd:      &exec.Cmd{Env: []string{"LOCALAPPDATA="}},
			Confined: true,
			rec:      rec,
		}
		if err := configureConfinedLaunch(pEmpty); err != nil {
			t.Fatalf("configureConfinedLaunch error: %v", err)
		}
		if pEmpty.ChildLocalAppDataIsOwnedRoot || pEmpty.ChildLocalAppDataFromOwnedRoot {
			t.Fatalf("unexpected empty provenance after prepare: is=%t from=%t", pEmpty.ChildLocalAppDataIsOwnedRoot, pEmpty.ChildLocalAppDataFromOwnedRoot)
		}
		if err := applyChildLocalAppData(pEmpty); err != nil {
			t.Fatalf("applyChildLocalAppData error: %v", err)
		}
		if pEmpty.ChildLocalAppDataIsOwnedRoot || pEmpty.ChildLocalAppDataFromOwnedRoot {
			t.Fatalf("unexpected empty provenance after start recheck: is=%t from=%t", pEmpty.ChildLocalAppDataIsOwnedRoot, pEmpty.ChildLocalAppDataFromOwnedRoot)
		}

		// 5. Case alias: must remain isOwnedRoot=false, fromOwnedRoot=false
		pAlias := &Prepared{
			Cmd:      &exec.Cmd{Env: []string{`localappdata=C:\other\alias`}},
			Confined: true,
			rec:      rec,
		}
		if err := configureConfinedLaunch(pAlias); err != nil {
			t.Fatalf("configureConfinedLaunch error: %v", err)
		}
		if pAlias.ChildLocalAppDataIsOwnedRoot || pAlias.ChildLocalAppDataFromOwnedRoot {
			t.Fatalf("unexpected alias provenance after prepare: is=%t from=%t", pAlias.ChildLocalAppDataIsOwnedRoot, pAlias.ChildLocalAppDataFromOwnedRoot)
		}
		if err := applyChildLocalAppData(pAlias); err != nil {
			t.Fatalf("applyChildLocalAppData error: %v", err)
		}
		if pAlias.ChildLocalAppDataIsOwnedRoot || pAlias.ChildLocalAppDataFromOwnedRoot {
			t.Fatalf("unexpected alias provenance after start recheck: is=%t from=%t", pAlias.ChildLocalAppDataIsOwnedRoot, pAlias.ChildLocalAppDataFromOwnedRoot)
		}
	})
}


