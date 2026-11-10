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

func TestAcceptCWD_ReadKeepsPath(t *testing.T) {
	const path = `C:\real\workdir`
	if got := wrapper.AcceptCWD(path, true); got != path {
		t.Fatalf("read cwd = %q, want %q", got, path)
	}
}
