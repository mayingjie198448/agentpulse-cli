package mqtt

import (
	"fmt"
	"testing"
	"time"

	"github.com/mayingjie198448/agentpulse-cli/internal/model"
)

func TestLogRingBuffer_BasicAndCapacity(t *testing.T) {
	rb := newLogRingBuffer(3)
	if rb.Size() != 0 {
		t.Fatalf("expected initial size 0, got %d", rb.Size())
	}

	// Empty slice test
	empty := rb.Slice(10)
	if len(empty) != 0 {
		t.Fatalf("expected empty slice, got %d", len(empty))
	}

	now := time.Now()
	makeEntry := func(i int) model.LogEntry {
		return model.LogEntry{
			Timestamp: now.Add(time.Duration(i) * time.Second),
			JobID:     "job-1",
			Message:   fmt.Sprintf("msg-%d", i),
		}
	}

	// Add 1
	rb.Append(makeEntry(1))
	if rb.Size() != 1 {
		t.Fatalf("expected size 1, got %d", rb.Size())
	}
	s := rb.Slice(0)
	if len(s) != 1 || s[0].Message != "msg-1" {
		t.Fatalf("unexpected slice after 1 insert: %+v", s)
	}

	// Add 2
	rb.Append(makeEntry(2))
	if rb.Size() != 2 {
		t.Fatalf("expected size 2, got %d", rb.Size())
	}
	s = rb.Slice(0)
	if len(s) != 2 || s[0].Message != "msg-1" || s[1].Message != "msg-2" {
		t.Fatalf("unexpected slice after 2 inserts: %+v", s)
	}

	// Add 3 (reaches capacity)
	rb.Append(makeEntry(3))
	if rb.Size() != 3 {
		t.Fatalf("expected size 3, got %d", rb.Size())
	}
	s = rb.Slice(0)
	if len(s) != 3 || s[0].Message != "msg-1" || s[1].Message != "msg-2" || s[2].Message != "msg-3" {
		t.Fatalf("unexpected slice after 3 inserts: %+v", s)
	}

	// Add 4 (overwrites oldest msg-1)
	rb.Append(makeEntry(4))
	if rb.Size() != 3 {
		t.Fatalf("expected size 3 after overflow, got %d", rb.Size())
	}
	s = rb.Slice(0)
	if len(s) != 3 || s[0].Message != "msg-2" || s[1].Message != "msg-3" || s[2].Message != "msg-4" {
		t.Fatalf("unexpected slice after 4 inserts: %+v", s)
	}

	// Add 5 (overwrites msg-2)
	rb.Append(makeEntry(5))
	s = rb.Slice(0)
	if len(s) != 3 || s[0].Message != "msg-3" || s[1].Message != "msg-4" || s[2].Message != "msg-5" {
		t.Fatalf("unexpected slice after 5 inserts: %+v", s)
	}

	// Test limit < size
	sLimit2 := rb.Slice(2)
	if len(sLimit2) != 2 || sLimit2[0].Message != "msg-4" || sLimit2[1].Message != "msg-5" {
		t.Fatalf("expected last 2 messages [msg-4, msg-5], got %+v", sLimit2)
	}

	sLimit1 := rb.Slice(1)
	if len(sLimit1) != 1 || sLimit1[0].Message != "msg-5" {
		t.Fatalf("expected last 1 message [msg-5], got %+v", sLimit1)
	}

	// Test limit > size
	sLimit5 := rb.Slice(5)
	if len(sLimit5) != 3 || sLimit5[0].Message != "msg-3" || sLimit5[2].Message != "msg-5" {
		t.Fatalf("expected all 3 messages when limit=5, got %+v", sLimit5)
	}

	// Test Clear
	rb.Clear()
	if rb.Size() != 0 || len(rb.Slice(0)) != 0 {
		t.Fatalf("expected size 0 after Clear, got %d", rb.Size())
	}
}

func TestLogRingBuffer_DefaultCapacity(t *testing.T) {
	rb := newLogRingBuffer(0)
	if rb.capacity != 500 {
		t.Fatalf("expected default capacity 500, got %d", rb.capacity)
	}

	for i := 0; i < 600; i++ {
		rb.Append(model.LogEntry{
			Message: fmt.Sprintf("line-%d", i),
		})
	}

	if rb.Size() != 500 {
		t.Fatalf("expected size 500, got %d", rb.Size())
	}

	all := rb.Slice(0)
	if len(all) != 500 {
		t.Fatalf("expected 500 entries, got %d", len(all))
	}
	if all[0].Message != "line-100" {
		t.Errorf("expected first line to be line-100, got %s", all[0].Message)
	}
	if all[499].Message != "line-599" {
		t.Errorf("expected last line to be line-599, got %s", all[499].Message)
	}
}
