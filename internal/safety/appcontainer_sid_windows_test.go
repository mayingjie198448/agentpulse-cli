//go:build windows

package safety

import (
	"errors"
	"fmt"
	"reflect"
	"testing"

	"golang.org/x/sys/windows"
)

func TestWindowsSIDFreeContract(t *testing.T) {
	t.Run("NilNoCall", func(t *testing.T) {
		calls := 0
		fakeReleaser := func(s *windows.SID) error {
			calls++
			return nil
		}

		if err := freeSIDWith(nil, fakeReleaser); err != nil {
			t.Fatalf("freeSIDWith(nil, fakeReleaser) unexpected error: %v", err)
		}
		if calls != 0 {
			t.Fatalf("expected 0 calls for nil SID, got %d", calls)
		}

		sid := new(windows.SID)
		if err := freeSIDWith(sid, nil); err != nil {
			t.Fatalf("freeSIDWith(sid, nil) unexpected error: %v", err)
		}
	})

	t.Run("OneCallSameObject", func(t *testing.T) {
		sid := new(windows.SID)
		var calledSID *windows.SID
		calls := 0
		fakeReleaser := func(s *windows.SID) error {
			calls++
			calledSID = s
			return nil
		}

		if err := freeSIDWith(sid, fakeReleaser); err != nil {
			t.Fatalf("freeSIDWith unexpected error: %v", err)
		}
		if calls != 1 {
			t.Fatalf("expected exactly 1 call, got %d", calls)
		}
		if calledSID != sid {
			t.Fatalf("releaser received different SID pointer than supplied")
		}
	})

	t.Run("ReturnedWrappedErrorChain", func(t *testing.T) {
		sid := new(windows.SID)
		rootErr := errors.New("simulated release failure")
		wrappedErr := fmt.Errorf("fake free failed: %w", rootErr)

		fakeReleaser := func(s *windows.SID) error {
			return wrappedErr
		}

		err := freeSIDWith(sid, fakeReleaser)
		if err == nil {
			t.Fatalf("expected error from freeSIDWith, got nil")
		}
		if !errors.Is(err, rootErr) {
			t.Fatalf("returned error chain does not match root error: %v", err)
		}
		if !errors.Is(err, wrappedErr) {
			t.Fatalf("returned error chain does not match wrapped error: %v", err)
		}
	})

	t.Run("ReleaseAPIBinding", func(t *testing.T) {
		// Safe function identity observation without invoking the API.
		// Never log addresses.
		got := reflect.ValueOf(sidFreeReleaser).Pointer()
		want := reflect.ValueOf(windows.FreeSid).Pointer()
		if got != want {
			t.Fatalf("sidFreeReleaser is not bound to windows.FreeSid")
		}
	})
}
