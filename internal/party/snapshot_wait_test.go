package party

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/mynora-dot/singverse-go-backend/internal/platform/store"
)

type snapshotResponse struct {
	status int
	body   document
	err    error
}

func pendingSnapshot(t *testing.T, f *fairFixture, path, token string, revision int64, wait int) (<-chan snapshotResponse, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	t.Cleanup(cancel)
	request, err := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("%s%s/snapshot?afterStateRevision=%d&waitMs=%d", f.server.URL, path, revision, wait), nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	result := make(chan snapshotResponse, 1)
	go func() {
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			result <- snapshotResponse{err: err}
			return
		}
		defer response.Body.Close()
		var body document
		decoder := json.NewDecoder(response.Body)
		decoder.UseNumber()
		err = decoder.Decode(&body)
		result <- snapshotResponse{response.StatusCode, body, err}
	}()
	return result, cancel
}

func snapshotWaiterCount(s *Service, id string) int {
	s.snapshotMu.Lock()
	defer s.snapshotMu.Unlock()
	return len(s.waiters[id])
}

func awaitSnapshotWaiter(t *testing.T, f *fairFixture, count int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for snapshotWaiterCount(f.s, str(f.state, "id")) != count {
		if time.Now().After(deadline) {
			t.Fatalf("snapshot waiter count did not become %d", count)
		}
		time.Sleep(time.Millisecond)
	}
}

func receiveSnapshot(t *testing.T, result <-chan snapshotResponse, status int) document {
	t.Helper()
	select {
	case response := <-result:
		if response.err != nil || response.status != status {
			t.Fatalf("snapshot status=%d, error=%v, body=%v", response.status, response.err, response.body)
		}
		return response.body
	case <-time.After(time.Second):
		t.Fatal("snapshot did not wake promptly")
		return nil
	}
}

func TestSnapshotWaitDeliversRemoteCommandAndExecutionWithoutPollingDelay(t *testing.T) {
	f := newFairFixture(t)
	f.send(t, f.guest, "ADD", document{"songId": "song-a"}, 200)
	baseline := want(t, f.request(t, "GET", f.base+"/snapshot", nil, f.device, false, false), 200)
	if number(baseline, "snapshotWaitMs") != 8000 {
		t.Fatal("server did not advertise bounded snapshot waiting")
	}
	television, _ := pendingSnapshot(t, f, f.base, f.device, number(baseline, "stateRevision"), 8000)
	awaitSnapshotWaiter(t, f, 1)
	select {
	case <-television:
		t.Fatal("unchanged snapshot returned before an event")
	case <-time.After(25 * time.Millisecond):
	}
	started := time.Now()
	f.send(t, f.guest, "PAUSE", nil, 200)
	tv := receiveSnapshot(t, television, 200)
	delivery := time.Since(started)
	var pause document
	for _, raw := range array(tv, "commands") {
		if command := raw.(map[string]any); str(command, "action") == "PAUSE" {
			pause = command
		}
	}
	if pause == nil || str(pause, "attemptId") != str(object(f.state, "playback"), "attemptId") {
		t.Fatal("TV did not receive the attempt-bound command", tv)
	}
	partyPath := strings.Replace(f.base, "/tv/", "/party/", 1)
	phone, _ := pendingSnapshot(t, f, partyPath, f.guest, number(tv, "stateRevision"), 8000)
	awaitSnapshotWaiter(t, f, 1)
	started = time.Now()
	want(t, f.request(t, "POST", f.base+"/playback-events", document{
		"commandId": pause["id"], "commandStatus": "EXECUTED", "attemptId": pause["attemptId"],
		"eventSeq": 1, "state": "PAUSED", "positionMs": 1000,
	}, f.device, false, false), 200)
	ack := receiveSnapshot(t, phone, 200)
	feedback := time.Since(started)
	if _, exposed := ack["commands"]; exposed || str(object(ack, "playback"), "state") != "PAUSED" {
		t.Fatal("participant view is not authoritative/redacted", ack)
	}
	found := false
	for _, raw := range array(ack, "commandResults") {
		result := raw.(map[string]any)
		found = found || str(result, "id") == str(pause, "id") && str(result, "status") == "EXECUTED"
	}
	if !found || delivery >= 500*time.Millisecond || feedback >= 500*time.Millisecond {
		t.Fatalf("command=%v delivery=%v feedback=%v", found, delivery, feedback)
	}
	t.Logf("local HTTP command delivery %s; TV execution feedback %s", delivery, feedback)
}

func TestSnapshotWaitTimeoutCancellationAndLegacyReads(t *testing.T) {
	f := newFairFixture(t)
	baseline := want(t, f.request(t, "GET", f.base+"/snapshot", nil, f.device, false, false), 200)
	started := time.Now()
	result, _ := pendingSnapshot(t, f, f.base, f.device, number(baseline, "stateRevision"), 60)
	timed := receiveSnapshot(t, result, 200)
	if time.Since(started) < 50*time.Millisecond || !boolean(timed, "tvOnline") || number(timed, "stateRevision") <= number(baseline, "stateRevision") {
		t.Fatal("timeout must return a fresh presence sample", timed)
	}
	result, cancel := pendingSnapshot(t, f, f.base, f.device, number(timed, "stateRevision"), 8000)
	awaitSnapshotWaiter(t, f, 1)
	cancel()
	select {
	case response := <-result:
		if response.err == nil {
			t.Fatal("cancelled request completed normally")
		}
	case <-time.After(time.Second):
		t.Fatal("snapshot cancellation hung")
	}
	awaitSnapshotWaiter(t, f, 0)
	// A stale cursor returns immediately, and clients without query parameters
	// keep the original immediate snapshot behavior.
	result, _ = pendingSnapshot(t, f, f.base, f.device, number(baseline, "stateRevision"), 8000)
	receiveSnapshot(t, result, 200)
	want(t, f.request(t, "GET", f.base+"/snapshot", nil, f.device, false, false), 200)
}

func TestSnapshotWaitSignalsOnlyCommittedChangesAndOnlyItsSession(t *testing.T) {
	f := newFairFixture(t)
	sid := str(f.state, "id")
	changed, release := f.s.watchSnapshot(sid)
	defer release()
	other, releaseOther := f.s.watchSnapshot("another-session")
	defer releaseOther()
	rolledBack := errors.New("rollback")
	_, err := f.s.transact(context.Background(), func(tx store.DBTX) (any, error) {
		session, err := f.s.get(context.Background(), tx, sid)
		if err != nil {
			return nil, err
		}
		if err = f.s.save(context.Background(), tx, session); err != nil {
			return nil, err
		}
		return nil, rolledBack
	})
	if !errors.Is(err, rolledBack) {
		t.Fatal(err)
	}
	select {
	case <-changed:
		t.Fatal("rolled back change woke a subscriber")
	default:
	}
	f.send(t, f.guest, "ADD", document{"songId": "song-a"}, 200)
	select {
	case <-changed:
	default:
		t.Fatal("committed change did not wake its subscriber")
	}
	select {
	case <-other:
		t.Fatal("unrelated session woke")
	default:
	}
}

func TestSnapshotWaitReauthenticatesAfterParticipantRevocation(t *testing.T) {
	f := newFairFixture(t)
	actor, err := f.s.Authenticate(context.Background(), f.guest)
	if err != nil {
		t.Fatal(err)
	}
	baseline := want(t, f.request(t, "GET", f.base+"/snapshot", nil, f.device, false, false), 200)
	phone, _ := pendingSnapshot(t, f, strings.Replace(f.base, "/tv/", "/party/", 1), f.guest, number(baseline, "stateRevision"), 8000)
	awaitSnapshotWaiter(t, f, 1)
	f.send(t, f.device, "REVOKE", document{"participantId": actor["id"]}, 200)
	receiveSnapshot(t, phone, 401)
	for _, query := range []string{"?waitMs=9000&afterStateRevision=0", "?waitMs=8", "?waitMs=8&afterStateRevision=-1", "?waitMs=no&afterStateRevision=0"} {
		want(t, f.request(t, "GET", f.base+"/snapshot"+query, nil, f.device, false, false), 400)
	}
}
