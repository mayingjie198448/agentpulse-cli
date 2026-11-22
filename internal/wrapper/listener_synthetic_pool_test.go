package wrapper_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mayingjie198448/agentpulse-cli/internal/model"
	"github.com/mayingjie198448/agentpulse-cli/internal/store/mqtt"
	"github.com/mayingjie198448/agentpulse-cli/internal/wrapper"
)

const poolHold = 400 * time.Millisecond

type poolEvent struct {
	kind   string
	id     string
	status model.JobStatus
	err    string
}

type poolHarness struct {
	t         *testing.T
	l         *wrapper.Listener
	client    *mockMQTTClient
	machine   string
	release   chan struct{}
	releaseMu sync.Once
	runDone   chan error
	cancel    context.CancelFunc
	listening chan struct{}
	mu        sync.Mutex
	events    []poolEvent
}

func (h *poolHarness) open() {
	h.releaseMu.Do(func() { close(h.release) })
}

func (h *poolHarness) record(ev poolEvent) {
	h.mu.Lock()
	h.events = append(h.events, ev)
	h.mu.Unlock()
}

func (h *poolHarness) snapshot() []poolEvent {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]poolEvent, len(h.events))
	copy(out, h.events)
	return out
}

func startPool(t *testing.T, machine string, n int) *poolHarness {
	t.Helper()
	restoreIP := wrapper.SetLocalIPDetectorForTest(func() string { return "203.0.113.10" })
	restoreGit := wrapper.SetRepoGitLookupForTest(func(context.Context, string, ...string) ([]byte, error) {
		return nil, errors.New("synthetic listener pool does not run git")
	})
	h := &poolHarness{
		t:         t,
		client:    newMockMQTTClient(),
		machine:   machine,
		release:   make(chan struct{}),
		runDone:   make(chan error, 1),
		listening: make(chan struct{}),
	}
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	var err error
	h.l, err = wrapper.NewListener(wrapper.ListenerConfig{
		MachineID:         machine,
		StatusFile:        filepath.Join(t.TempDir(), "status.json"),
		MQTTClient:        h.client,
		Stub:              true,
		Quiet:             true,
		DisableBareAttach: true,
		MaxConcurrentJobs: n,
		BaseEnviron:       []string{},
		OnListening:       func(string) { close(h.listening) },
		OnJobStart: func(j model.Job) {
			h.record(poolEvent{kind: "start", id: j.ID, status: j.Status})
			<-h.release
		},
		OnJobComplete: func(j model.Job, runErr error) {
			msg := ""
			if runErr != nil {
				msg = runErr.Error()
			}
			h.record(poolEvent{kind: "done", id: j.ID, status: j.Status, err: msg})
		},
	})
	if err != nil {
		t.Fatalf("NewListener: %v", err)
	}
	if h.l.AttachManager() != nil {
		t.Fatal("DisableBareAttach still constructed an attach manager")
	}
	go func() { h.runDone <- h.l.Run(ctx) }()
	select {
	case <-h.listening:
	case <-time.After(2 * time.Second):
		t.Fatal("listener did not subscribe before dispatch")
	}
	t.Cleanup(func() {
		h.open()
		cancel()
		select {
		case <-h.runDone:
		case <-time.After(3 * time.Second):
			t.Errorf("Listener.Run did not return within 3s")
		}
		restoreGit()
		restoreIP()
	})
	return h
}

func (h *poolHarness) dispatch(ids []string) {
	h.t.Helper()
	topic := mqtt.MachineDispatchTopic(h.machine)
	for _, id := range ids {
		cmd := model.DispatchCommand{
			CommandID: "cmd-" + id,
			JobID:     id,
			Engine:    model.EngineType("synth"),
			Command:   "synth-lifecycle",
			Title:     id,
			Repo:      "synth/repo",
			Project:   "synth",
			Cwd:       filepath.Join(h.t.TempDir(), "work"),
		}
		payload, err := json.Marshal(cmd)
		if err != nil {
			h.t.Fatalf("marshal %s: %v", id, err)
		}
		h.client.Publish(topic, 1, false, payload)
	}
}

func (h *poolHarness) waitStarts(n int) []poolEvent {
	h.t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		starts := h.starts()
		if len(starts) >= n {
			return starts
		}
		time.Sleep(10 * time.Millisecond)
	}
	h.t.Fatalf("timed out waiting for %d job starts, saw %s", n, h.format(h.snapshot()))
	return nil
}

func (h *poolHarness) starts() []poolEvent {
	var out []poolEvent
	for _, ev := range h.snapshot() {
		if ev.kind == "start" {
			out = append(out, ev)
		}
	}
	return out
}

func (h *poolHarness) waitDone(n int) []poolEvent {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var done []poolEvent
		for _, ev := range h.snapshot() {
			if ev.kind == "done" {
				done = append(done, ev)
			}
		}
		if len(done) >= n {
			return done
		}
		time.Sleep(10 * time.Millisecond)
	}
	h.t.Fatalf("timed out waiting for %d completions, saw %s", n, h.format(h.snapshot()))
	return nil
}

func (h *poolHarness) format(evs []poolEvent) string {
	out := make([]string, len(evs))
	for i, ev := range evs {
		out[i] = ev.kind + " " + ev.id + " " + string(ev.status)
		if ev.err != "" {
			out[i] += " err=" + ev.err
		}
	}
	return strings.Join(out, " | ")
}

func (h *poolHarness) latestStatus(id string) (model.Job, bool) {
	topic := mqtt.JobStatusTopic(h.machine, id)
	var job model.Job
	found := false
	for _, msg := range h.client.GetPublished() {
		if msg.Topic != topic {
			continue
		}
		var parsed model.Job
		if json.Unmarshal(msg.Payload, &parsed) == nil && parsed.ID == id {
			job = parsed
			found = true
		}
	}
	return job, found
}

func TestListener_SyntheticPool_ThreeRunningOneQueued(t *testing.T) {
	h := startPool(t, "pool-n3", 3)
	ids := []string{"job-n3-1", "job-n3-2", "job-n3-3", "job-n3-4"}
	h.dispatch(ids)

	starts := h.waitStarts(3)
	if len(starts) != 3 {
		t.Fatalf("starts = %s, want exactly 3", h.format(starts))
	}
	started := map[string]model.JobStatus{}
	for _, ev := range starts {
		if _, dup := started[ev.id]; dup {
			t.Fatalf("job %s started twice", ev.id)
		}
		if ev.status != model.StatusRunning {
			t.Fatalf("job %s entered the pool as %s, want running", ev.id, ev.status)
		}
		started[ev.id] = ev.status
	}
	var queuedID string
	for _, id := range ids {
		if _, ok := started[id]; !ok {
			if queuedID != "" {
				t.Fatalf("more than one job stayed out of the pool: %s and %s", queuedID, id)
			}
			queuedID = id
		}
	}
	if queuedID == "" {
		t.Fatalf("all four jobs entered a pool of 3: %s", h.format(starts))
	}

	deadline := time.Now().Add(poolHold)
	for time.Now().Before(deadline) {
		if extra := h.starts(); len(extra) > 3 {
			t.Fatalf("job started while 3 workers were held: %s", h.format(extra))
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := len(h.starts()); got != 3 {
		t.Fatalf("held starts = %d, want 3", got)
	}
	queued, ok := h.latestStatus(queuedID)
	if !ok {
		t.Fatalf("queued job %s has no status publication", queuedID)
	}
	if queued.Status != model.StatusQueued || queued.ID != queuedID {
		t.Fatalf("job %s status = %+v, want queued", queuedID, queued)
	}
	if h.l.Machine().JobCount != 4 {
		t.Fatalf("machine job count = %d, want 4", h.l.Machine().JobCount)
	}

	h.open()
	done := h.waitDone(4)
	seen := map[string]poolEvent{}
	for _, ev := range done {
		if prev, dup := seen[ev.id]; dup {
			t.Fatalf("job %s completed twice: %+v then %+v", ev.id, prev, ev)
		}
		seen[ev.id] = ev
	}
	for _, id := range ids {
		ev, ok := seen[id]
		if !ok {
			t.Fatalf("job %s missing from completions %s", id, h.format(done))
		}
		if ev.status != model.StatusDone || ev.err != "" {
			t.Fatalf("job %s completion = %+v, want done with no error", id, ev)
		}
		final, ok := h.latestStatus(id)
		if !ok || final.Status != model.StatusDone || final.ID != id {
			t.Fatalf("job %s final publication = %+v", id, final)
		}
	}
	if h.l.Machine().JobCount != 0 {
		t.Fatalf("machine job count after completion = %d, want 0", h.l.Machine().JobCount)
	}
}

func TestListener_SyntheticPool_OneSerial(t *testing.T) {
	h := startPool(t, "pool-n1", 1)
	ids := []string{"job-n1-a", "job-n1-b"}
	h.dispatch(ids)

	first := h.waitStarts(1)
	if len(first) != 1 || first[0].status != model.StatusRunning {
		t.Fatalf("first start = %s, want one running job", h.format(first))
	}
	runningID := first[0].id
	var waitingID string
	for _, id := range ids {
		if id != runningID {
			waitingID = id
		}
	}
	deadline := time.Now().Add(poolHold)
	for time.Now().Before(deadline) {
		if got := h.starts(); len(got) != 1 {
			t.Fatalf("second job started before the first was released: %s", h.format(got))
		}
		time.Sleep(10 * time.Millisecond)
	}
	queued, ok := h.latestStatus(waitingID)
	if !ok || queued.Status != model.StatusQueued || queued.ID != waitingID {
		t.Fatalf("waiting job %s = %+v, want queued", waitingID, queued)
	}
	if h.l.Machine().JobCount != 2 {
		t.Fatalf("machine job count = %d, want 2", h.l.Machine().JobCount)
	}

	h.open()
	h.waitDone(2)
	got := h.snapshot()
	want := []poolEvent{
		{kind: "start", id: runningID, status: model.StatusRunning},
		{kind: "done", id: runningID, status: model.StatusDone},
		{kind: "start", id: waitingID, status: model.StatusRunning},
		{kind: "done", id: waitingID, status: model.StatusDone},
	}
	if len(got) != len(want) {
		t.Fatalf("event order = %s, want start/done %s then start/done %s", h.format(got), runningID, waitingID)
	}
	for i := range want {
		if got[i].kind != want[i].kind || got[i].id != want[i].id || got[i].status != want[i].status || got[i].err != "" {
			t.Fatalf("event[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}
