package wrapper_test

import (
	"testing"
	"time"

	"github.com/mayingjie198448/agentpulse-cli/internal/wrapper"
)

func TestProcessIdentity_SameStartMatches(t *testing.T) {
	start := time.Unix(1700000000, 0).UTC()
	same, known := wrapper.SameProcess(
		wrapper.ProcessIdentity{PID: 4242, Start: start},
		wrapper.ProcessIdentity{PID: 4242, Start: start},
	)
	if !known || !same {
		t.Fatalf("same=%v known=%v, want same and known", same, known)
	}
}

func TestProcessIdentity_DifferentStartIsReuse(t *testing.T) {
	first := time.Unix(1700000000, 0).UTC()
	second := first.Add(time.Second)
	same, known := wrapper.SameProcess(
		wrapper.ProcessIdentity{PID: 4242, Start: first},
		wrapper.ProcessIdentity{PID: 4242, Start: second},
	)
	if !known || same {
		t.Fatalf("same=%v known=%v, want a known PID reuse", same, known)
	}
}

func TestProcessIdentity_UnknownStartDoesNotClaimMatch(t *testing.T) {
	start := time.Unix(1700000000, 0).UTC()
	same, known := wrapper.SameProcess(
		wrapper.ProcessIdentity{PID: 4242, Start: start},
		wrapper.ProcessIdentity{PID: 4242, Start: time.Time{}},
	)
	if known || same {
		t.Fatalf("same=%v known=%v, want an unknown comparison", same, known)
	}
}

func TestObserveExit_ReusedDropsCode(t *testing.T) {
	got := wrapper.ObserveExit(true, false, wrapper.ExitObservation{Code: 7, Known: true})
	if got.Known || got.Code != 0 {
		t.Fatalf("reused observation = %+v, want unknown", got)
	}
}

func TestObserveExit_AliveDropsCode(t *testing.T) {
	got := wrapper.ObserveExit(false, true, wrapper.ExitObservation{Code: 7, Known: true})
	if got.Known || got.Code != 0 {
		t.Fatalf("live observation = %+v, want unknown", got)
	}
}

func TestObserveExit_KnownCodeKept(t *testing.T) {
	got := wrapper.ObserveExit(false, false, wrapper.ExitObservation{Code: 7, Known: true})
	if !got.Known || got.Code != 7 {
		t.Fatalf("exited observation = %+v, want code 7", got)
	}
}

func TestAcceptCWD_UnreadIsEmpty(t *testing.T) {
	if got := wrapper.AcceptCWD(`C:\guessed\workdir`, false); got != "" {
		t.Fatalf("unread cwd = %q, want empty", got)
	}
}

func TestAllowOpen_NilAndEmptyOpenNothing(t *testing.T) {
	if wrapper.AllowOpen(nil, 4242) {
		t.Fatal("nil allowlist opened a pid")
	}
	if wrapper.AllowOpen(map[int]bool{}, 4242) {
		t.Fatal("empty allowlist opened a pid")
	}
	if wrapper.AllowOpen(nil, 0) || wrapper.AllowOpen(map[int]bool{1: true}, 0) {
		t.Fatal("pid 0 was opened")
	}
}

func TestAllowOpen_OnlyListedPID(t *testing.T) {
	allow := map[int]bool{4242: true}
	if !wrapper.AllowOpen(allow, 4242) {
		t.Fatal("listed pid was refused")
	}
	if wrapper.AllowOpen(allow, 4243) {
		t.Fatal("pid outside the allowlist was opened")
	}
}

func TestBoundExit_UnknownOpenDoesNotReport(t *testing.T) {
	start := time.Unix(1700000000, 0).UTC()
	got := wrapper.BoundExit(
		wrapper.ProcessIdentity{PID: 4242, Start: start},
		wrapper.ProcessIdentity{PID: 4242},
		false,
		false,
		wrapper.ExitObservation{Code: 7, Known: true},
	)
	if got.Known || got.Code != 0 {
		t.Fatalf("unopened query = %+v, want no code", got)
	}
}

func TestBoundExit_UnknownStartDoesNotReport(t *testing.T) {
	start := time.Unix(1700000000, 0).UTC()
	got := wrapper.BoundExit(
		wrapper.ProcessIdentity{PID: 4242, Start: start},
		wrapper.ProcessIdentity{PID: 4242, Start: time.Time{}},
		true,
		false,
		wrapper.ExitObservation{Code: 7, Known: true},
	)
	if got.Known || got.Code != 0 {
		t.Fatalf("unknown start = %+v, want no code", got)
	}
}

func TestBoundExit_ReuseDoesNotReport(t *testing.T) {
	start := time.Unix(1700000000, 0).UTC()
	got := wrapper.BoundExit(
		wrapper.ProcessIdentity{PID: 4242, Start: start},
		wrapper.ProcessIdentity{PID: 4242, Start: start.Add(time.Second)},
		true,
		false,
		wrapper.ExitObservation{Code: 7, Known: true},
	)
	if got.Known || got.Code != 0 {
		t.Fatalf("reused pid = %+v, want no code", got)
	}
}

func TestBoundExit_AliveDoesNotReport(t *testing.T) {
	start := time.Unix(1700000000, 0).UTC()
	got := wrapper.BoundExit(
		wrapper.ProcessIdentity{PID: 4242, Start: start},
		wrapper.ProcessIdentity{PID: 4242, Start: start},
		true,
		true,
		wrapper.ExitObservation{Code: 7, Known: true},
	)
	if got.Known || got.Code != 0 {
		t.Fatalf("live process = %+v, want no code", got)
	}
}

func TestBoundExit_KnownExitKept(t *testing.T) {
	start := time.Unix(1700000000, 0).UTC()
	got := wrapper.BoundExit(
		wrapper.ProcessIdentity{PID: 4242, Start: start},
		wrapper.ProcessIdentity{PID: 4242, Start: start},
		true,
		false,
		wrapper.ExitObservation{Code: 7, Known: true},
	)
	if !got.Known || got.Code != 7 {
		t.Fatalf("same exited process = %+v, want code 7", got)
	}
}

func TestAcceptCWD_ReadKeepsPath(t *testing.T) {
	const path = `C:\real\workdir`
	if got := wrapper.AcceptCWD(path, true); got != path {
		t.Fatalf("read cwd = %q, want %q", got, path)
	}
}
