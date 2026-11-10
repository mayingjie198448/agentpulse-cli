package mqtt_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	mqttstore "github.com/mayingjie198448/agentpulse-cli/internal/store/mqtt"
)

// rejectedFilters mirrors a Mosquitto Dynamic Security SUBACK of 0x80 on every
// product filter. token.Error() stays nil; only Result() carries the code.
func rejectedFilters() map[string]byte {
	return map[string]byte{
		mqttstore.TopicMachinesStatus:           0x80,
		mqttstore.TopicJobsStatus:               0x80,
		mqttstore.TopicJobsLogs:                 0x80,
		mqttstore.TopicAnnounceWildcard:         0x80,
		mqttstore.TopicMachinesAnnounceWildcard: 0x80,
	}
}

type raceBaseline struct {
	machines  []string
	jobs      []string
	announces []string
}

func newRaceStore(t *testing.T) (*mqttstore.Store, *mqttstore.FakeMQTTClient, <-chan *mqttstore.SubscribeHold) {
	t.Helper()
	fake := mqttstore.NewFakeMQTTClient()
	cfg := mqttstore.DefaultConfig()
	// Long enough that the test goroutine can release both subscriptions and
	// deliver the historical dump before Refresh settles. Ordering itself is
	// gated, not timed.
	cfg.SettleTimeout = 200 * time.Millisecond
	cfg.HeartbeatTimeout = time.Hour
	cfg.HeartbeatCheckInterval = time.Hour

	st, err := mqttstore.NewWithClient(cfg, fake)
	if err != nil {
		t.Fatalf("NewWithClient: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	fake.SetSuppressRetainedOnSubscribe(true)
	gate := fake.ArmSubscribeGate()
	t.Cleanup(fake.DisarmSubscribeGate)
	return st, fake, gate
}

func awaitHold(t *testing.T, gate <-chan *mqttstore.SubscribeHold) *mqttstore.SubscribeHold {
	t.Helper()
	select {
	case h := <-gate:
		if h == nil {
			t.Fatal("nil subscribe hold")
		}
		return h
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for SubscribeMultiple")
		return nil
	}
}

func releaseHold(t *testing.T, h *mqttstore.SubscribeHold) {
	t.Helper()
	h.Release()
	select {
	case <-h.Registered():
	case <-time.After(3 * time.Second):
		t.Fatal("SubscribeMultiple did not finish registering its callback")
	}
}

func seedRaceBaseline(t *testing.T, fake *mqttstore.FakeMQTTClient) raceBaseline {
	t.Helper()
	base := raceBaseline{
		machines:  []string{"qa-race-m1", "qa-race-m2"},
		jobs:      []string{"qa-race-j1", "qa-race-j2"},
		announces: []string{"qa-race-m1", "qa-race-m2"},
	}
	machines := []struct {
		id, payload string
	}{
		{"qa-race-m1", `{"id":"qa-race-m1","name":"qa-race-m1","online":true}`},
		{"qa-race-m2", `{"id":"qa-race-m2","name":"qa-race-m2","online":true}`},
	}
	for _, m := range machines {
		fake.Deliver("agentpulse/v1/machines/"+m.id+"/status", []byte(m.payload), false)
	}
	jobs := []struct {
		machine, id, payload string
	}{
		{"qa-race-m1", "qa-race-j1", `{"id":"qa-race-j1","machine_id":"qa-race-m1","status":"running","title":"qa-race-j1"}`},
		{"qa-race-m2", "qa-race-j2", `{"id":"qa-race-j2","machine_id":"qa-race-m2","status":"running","title":"qa-race-j2"}`},
	}
	for _, j := range jobs {
		topic := fmt.Sprintf("agentpulse/v1/machines/%s/jobs/%s/status", j.machine, j.id)
		fake.Deliver(topic, []byte(j.payload), false)
	}
	for _, id := range base.announces {
		payload := fmt.Sprintf(`{"v":1,"machine_id":%q,"hostname":%q,"os":"test","rev":1,"agents":[]}`, id, id)
		fake.Deliver("agentpulse/v1/"+id+"/announce", []byte(payload), false)
	}
	return base
}

// replayHistoricalDump delivers the broker's resubscribe replay. The subscriber
// flag is RETAIN=1. That is the historical dump, not a live publish of a
// retained message (MQTT 3.1.1 forwards those with RETAIN=0).
func replayHistoricalDump(fake *mqttstore.FakeMQTTClient) {
	payloads := []struct {
		topic   string
		payload string
	}{
		{"agentpulse/v1/machines/qa-race-m1/status", `{"id":"qa-race-m1","name":"qa-race-m1","online":true}`},
		{"agentpulse/v1/machines/qa-race-m2/status", `{"id":"qa-race-m2","name":"qa-race-m2","online":true}`},
		{"agentpulse/v1/machines/qa-race-m1/jobs/qa-race-j1/status", `{"id":"qa-race-j1","machine_id":"qa-race-m1","status":"running","title":"qa-race-j1"}`},
		{"agentpulse/v1/machines/qa-race-m2/jobs/qa-race-j2/status", `{"id":"qa-race-j2","machine_id":"qa-race-m2","status":"running","title":"qa-race-j2"}`},
		{"agentpulse/v1/qa-race-m1/announce", `{"v":1,"machine_id":"qa-race-m1","hostname":"qa-race-m1","os":"test","rev":1,"agents":[]}`},
		{"agentpulse/v1/qa-race-m2/announce", `{"v":1,"machine_id":"qa-race-m2","hostname":"qa-race-m2","os":"test","rev":1,"agents":[]}`},
	}
	for _, p := range payloads {
		fake.Deliver(p.topic, []byte(p.payload), true)
	}
}

func snapshotIDs(t *testing.T, st *mqttstore.Store) (machines, jobs, announces map[string]bool) {
	t.Helper()
	ctx := context.Background()
	machines, jobs, announces = map[string]bool{}, map[string]bool{}, map[string]bool{}
	ms, err := st.ListMachines(ctx)
	if err != nil {
		t.Fatalf("ListMachines: %v", err)
	}
	for _, m := range ms {
		machines[m.ID] = true
	}
	js, err := st.ListJobs(ctx)
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	for _, j := range js {
		jobs[j.ID] = true
	}
	as, err := st.ListAnnounces(ctx)
	if err != nil {
		t.Fatalf("ListAnnounces: %v", err)
	}
	for _, a := range as {
		announces[a.MachineID] = true
	}
	return machines, jobs, announces
}

func assertIDSet(t *testing.T, what string, got map[string]bool, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s count=%d want %d (%v)", what, len(got), len(want), got)
	}
	for _, id := range want {
		if !got[id] {
			t.Fatalf("%s missing %s (have %v)", what, id, got)
		}
	}
}

func assertBaseline(t *testing.T, st *mqttstore.Store, base raceBaseline, extraMachines, extraJobs []string) {
	t.Helper()
	machines, jobs, announces := snapshotIDs(t, st)
	assertIDSet(t, "machines", machines, append(append([]string{}, base.machines...), extraMachines...))
	assertIDSet(t, "jobs", jobs, append(append([]string{}, base.jobs...), extraJobs...))
	assertIDSet(t, "announces", announces, base.announces)
}

// rejectOnce drives the Refresh subscribe and its synchronous restore, both of
// which must observe the current SUBACK injection.
func rejectOnce(t *testing.T, st *mqttstore.Store, gate <-chan *mqttstore.SubscribeHold) error {
	t.Helper()
	errCh := make(chan error, 1)
	go func() {
		errCh <- st.Refresh(context.Background())
	}()
	releaseHold(t, awaitHold(t, gate))
	releaseHold(t, awaitHold(t, gate))
	select {
	case err := <-errCh:
		return err
	case <-time.After(3 * time.Second):
		t.Fatal("Refresh did not return")
		return nil
	}
}

// TestMQTTStore_Refresh_MaintainerOverlap_DoesNotCommitEmptyOrPartialCache
// reproduces the controlled interleaving from the 6a39fbf retest:
//
//  1. Refresh and its restore are both SUBACK 0x80, so recovery stays pending.
//  2. The maintainer's next SubscribeMultiple is inside the call but has not
//     stored a callback yet.
//  3. A second Refresh unsubscribes, then its SubscribeMultiple stores the
//     refresh callback and receives a successful SUBACK.
//  4. The maintainer call then stores its callback (the later registration).
//  5. Only after both registrations does the historical RETAIN=1 dump arrive.
//
// Refresh must return nil and keep the full baseline. A live RETAIN=0 message
// during settle is merged; it must not become the only remaining cache entry.
func TestMQTTStore_Refresh_MaintainerOverlap_DoesNotCommitEmptyOrPartialCache(t *testing.T) {
	for _, liveDuringSettle := range []bool{false, true} {
		name := "empty_staging"
		if liveDuringSettle {
			name = "partial_staging"
		}
		t.Run(name, func(t *testing.T) {
			st, fake, gate := newRaceStore(t)
			base := seedRaceBaseline(t, fake)
			assertBaseline(t, st, base, nil, nil)

			fake.SetSubscribeResults(rejectedFilters())
			if err := rejectOnce(t, st, gate); err == nil {
				t.Fatal("first Refresh should fail when every filter is rejected")
			}
			assertBaseline(t, st, base, nil, nil)

			// Maintainer is pending and blocked before it can replace the callback.
			maintainer := awaitHold(t, gate)

			errCh := make(chan error, 1)
			go func() {
				errCh <- st.Refresh(context.Background())
			}()
			refreshSub := awaitHold(t, gate)

			// ACL is restored only after the second Refresh has started and is
			// inside SubscribeMultiple (unsubscribe has already completed).
			fake.SetSubscribeResults(nil)
			releaseHold(t, refreshSub)
			releaseHold(t, maintainer)

			// Historical dump goes to the callback registered last.
			replayHistoricalDump(fake)
			if liveDuringSettle {
				fake.Deliver("agentpulse/v1/machines/qa-race-m3/status", []byte(`{"id":"qa-race-m3","name":"qa-race-m3","online":true}`), false)
				fake.Deliver("agentpulse/v1/machines/qa-race-m1/jobs/qa-race-j3/status", []byte(`{"id":"qa-race-j3","machine_id":"qa-race-m1","status":"running","title":"qa-race-j3"}`), false)
			}

			select {
			case err := <-errCh:
				if err != nil {
					t.Fatalf("second Refresh returned %v; successful SUBACK must not fail closed, and must not commit an empty cache either", err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("second Refresh did not return")
			}

			extraM, extraJ := []string(nil), []string(nil)
			if liveDuringSettle {
				extraM = []string{"qa-race-m3"}
				extraJ = []string{"qa-race-j3"}
			}
			assertBaseline(t, st, base, extraM, extraJ)

			// Four maintainer ticks (50ms) must not be required to repair the
			// cache, and must not wipe it after the nil return.
			time.Sleep(200 * time.Millisecond)
			assertBaseline(t, st, base, extraM, extraJ)

			fake.Deliver("agentpulse/v1/machines/qa-race-m4/status", []byte(`{"id":"qa-race-m4","name":"qa-race-m4","online":true}`), false)
			assertBaseline(t, st, base, append(extraM, "qa-race-m4"), extraJ)
		})
	}
}

// TestMQTTStore_Refresh_MaintainerOverlap_RejectedRefreshKeepsRetry proves an
// in-flight recovery attempt cannot retire itself across a Refresh that is
// still rejected, and that the next recovery tick resumes live delivery
// without another Refresh.
func TestMQTTStore_Refresh_MaintainerOverlap_RejectedRefreshKeepsRetry(t *testing.T) {
	st, fake, gate := newRaceStore(t)
	base := seedRaceBaseline(t, fake)

	fake.SetSubscribeResults(rejectedFilters())
	if err := rejectOnce(t, st, gate); err == nil {
		t.Fatal("expected rejected Refresh")
	}
	assertBaseline(t, st, base, nil, nil)

	maintainer := awaitHold(t, gate)
	errCh := make(chan error, 1)
	go func() {
		errCh <- st.Refresh(context.Background())
	}()
	// Second Refresh is still denied. Pump its subscribe and its restore.
	releaseHold(t, awaitHold(t, gate))
	releaseHold(t, awaitHold(t, gate))
	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("second Refresh should stay failed while SUBACK is 0x80")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("second Refresh did not return")
	}
	assertBaseline(t, st, base, nil, nil)

	// The overlapped maintainer call also observes the denial and must not
	// clear the pending recovery.
	releaseHold(t, maintainer)

	fake.SetSubscribeResults(nil)
	releaseHold(t, awaitHold(t, gate))

	fake.Deliver("agentpulse/v1/machines/qa-race-m-live/status", []byte(`{"id":"qa-race-m-live","name":"qa-race-m-live","online":true}`), false)
	assertBaseline(t, st, base, []string{"qa-race-m-live"}, nil)

	// A historical replay after recovery must not replace the live cache.
	replayHistoricalDump(fake)
	if _, err := st.GetMachine(context.Background(), "qa-race-m-live"); err != nil {
		t.Fatalf("live machine lost after retained replay on the restore handler: %v", err)
	}
	assertBaseline(t, st, base, []string{"qa-race-m-live"}, nil)
}
