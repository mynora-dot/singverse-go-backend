package party

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/mynora-dot/singverse-go-backend/internal/platform/store"
)

type fairFixture struct {
	*fixture
	device, guest, other, base string
	revision                   int64
	state                      document
}

func newFairFixture(t *testing.T) *fairFixture {
	f := newPartyFixture(t)
	device, _ := f.device(t)
	session := want(t, f.request(t, "POST", "/api/v1/tv/sessions", nil, device, false, false), 200)
	base := "/api/v1/tv/sessions/" + str(session, "id")
	want(t, f.request(t, "POST", base+"/capabilities", document{"capabilities": []string{"AUTO_ADVANCE", "SEEK", "AUDIO_ROLE", "APPLY_AUDIO_ROLES", "REACTIONS"}}, device, false, false), 200)
	invite := want(t, f.request(t, "POST", base+"/invitations", nil, device, false, false), 200)
	join := func(name string) string {
		return str(want(t, f.request(t, "POST", "/api/v1/party/join", document{"invitation": invite["invitation"], "name": name}, "", false, false), 200), "token")
	}
	return &fairFixture{fixture: f, device: device, guest: join("小明"), other: join("小红"), base: base, state: session}
}
func (f *fairFixture) send(t *testing.T, token, action string, fields document, status int) document {
	t.Helper()
	input := document{"requestId": id(), "expectedRevision": f.revision, "action": action, "attemptId": object(f.state, "playback")["attemptId"]}
	for k, v := range fields {
		input[k] = v
	}
	result := want(t, f.request(t, "POST", f.base+"/commands", input, token, false, false), status)
	if status == 200 {
		f.state = result
		f.revision = number(result, "revision")
	}
	return result
}
func waiting(state document) []document {
	out := []document{}
	for _, raw := range array(state, "queue") {
		v := raw.(map[string]any)
		if str(v, "status") == "WAITING" {
			out = append(out, v)
		}
	}
	return out
}
func fifoByOwner(t *testing.T, queue []document) {
	t.Helper()
	last := map[string]int64{}
	for _, v := range queue {
		if boolean(v, "priority") {
			continue
		}
		owner := str(v, "ownerId")
		if number(v, "sequence") < last[owner] {
			t.Fatal("personal FIFO changed", queue)
		}
		last[owner] = number(v, "sequence")
	}
}

func TestFairPartyAutomaticallyStartsAndRestoresNormalTurns(t *testing.T) {
	f := newFairFixture(t)
	f.send(t, f.guest, "ADD", document{"songId": "song-a"}, 200)
	first := object(f.state, "playback")
	if !boolean(first, "confirmed") || str(first, "state") != "PREPARING" || len(array(f.state, "commandResults")) != 1 {
		t.Fatal("first request did not LOAD automatically", f.state)
	}
	for _, who := range []string{f.guest, f.guest, f.other, f.other} {
		f.send(t, who, "ADD", document{"songId": "song-b"}, 200)
	}
	q := waiting(f.state)
	if str(q[0], "ownerName") != "小红" || str(q[1], "ownerName") != "小明" {
		t.Fatal("new person did not enter this round", q)
	}
	fifoByOwner(t, q)
	originalAttempt := str(first, "attemptId")
	f.send(t, f.guest, "SHUFFLE", nil, 200)
	fifoByOwner(t, waiting(f.state))
	if str(object(f.state, "playback"), "attemptId") != originalAttempt {
		t.Fatal("shuffle changed current song")
	}
	q = waiting(f.state)
	a, b := str(q[len(q)-1], "id"), str(q[len(q)-2], "id")
	f.send(t, f.guest, "TOP", document{"itemId": a}, 200)
	f.send(t, f.other, "TOP", document{"itemId": b}, 200)
	q = waiting(f.state)
	count := 0
	for _, v := range q {
		if boolean(v, "priority") {
			count++
		}
	}
	if count != 1 || str(q[0], "id") != b {
		t.Fatal("priority replacement did not retain one slot", q)
	}
	f.send(t, f.guest, "NEXT", nil, 200)
	if str(object(f.state, "playback"), "itemId") != b {
		t.Fatal("priority was not consumed")
	}
	for _, v := range waiting(f.state) {
		if boolean(v, "priority") {
			t.Fatal("priority survived consumption")
		}
	}
	fifoByOwner(t, waiting(f.state))
	f.send(t, f.other, "REMOVE", document{"itemId": a}, func() int {
		for _, v := range waiting(f.state) {
			if str(v, "id") == a && str(v, "ownerName") == "小红" {
				return 200
			}
		}
		return 403
	}())
}

func TestFairPartyPlaybackCommandsBindAttemptAndHaveExecutionReceipts(t *testing.T) {
	f := newFairFixture(t)
	f.send(t, f.guest, "ADD", document{"songId": "song-a"}, 200)
	f.send(t, f.other, "ADD", document{"songId": "song-b"}, 200)
	revision := f.revision
	attempt := str(object(f.state, "playback"), "attemptId")
	request := document{"requestId": "stable-seek", "action": "SEEK", "attemptId": attempt, "positionMs": 1234}
	result := want(t, f.request(t, "POST", f.base+"/commands", request, f.guest, false, false), 200)
	if number(result, "revision") != revision {
		t.Fatal("seek changed queue revision")
	}
	tv := want(t, f.request(t, "GET", f.base+"/snapshot", nil, f.device, false, false), 200)
	commands := array(tv, "commands")
	command := commands[len(commands)-1].(map[string]any)
	if number(command, "positionMs") != 1234 || str(command, "attemptId") != attempt {
		t.Fatal("seek lost binding", command)
	}
	ack := want(t, f.request(t, "POST", f.base+"/playback-events", document{"commandId": command["id"], "commandStatus": "FAILED"}, f.device, false, false), 200)
	results := array(ack, "commandResults")
	if str(results[len(results)-1].(map[string]any), "status") != "FAILED" {
		t.Fatal("TV failure hidden", ack)
	}
	f.send(t, f.other, "NEXT", nil, 200)
	want(t, f.request(t, "POST", f.base+"/commands", request, f.guest, false, false), 200) // committed retry, no second seek
	f.send(t, f.guest, "SEEK", document{"attemptId": attempt, "positionMs": 999}, 409)
	f.send(t, f.guest, "CLEAR", nil, 403)
	f.send(t, f.guest, "END", nil, 403)
}

func TestFairPartyNaturalEndAndSkipRaceAdvanceOnlyOnce(t *testing.T) {
	f := newFairFixture(t)
	for _, who := range []string{f.guest, f.other, f.guest, f.other} {
		f.send(t, who, "ADD", document{"songId": "song-a"}, 200)
	}
	before := waiting(f.state)
	attempt := str(object(f.state, "playback"), "attemptId")
	sessionID := str(f.state, "id")
	device, _ := f.s.Authenticate(t.Context(), f.device)
	guest, _ := f.s.Authenticate(t.Context(), f.guest)
	var group sync.WaitGroup
	errors := make(chan error, 2)
	for i := range 2 {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			_, err := f.s.transact(context.Background(), func(tx store.DBTX) (any, error) {
				if index == 0 {
					return f.s.recordPlaybackEvent(t.Context(), tx, device, sessionID, document{"attemptId": attempt, "eventSeq": 0, "state": "ENDED", "positionMs": 1000})
				}
				return f.s.changeQueue(t.Context(), tx, guest, sessionID, document{"requestId": "race-next", "expectedRevision": f.revision, "attemptId": attempt, "action": "NEXT"})
			})
			errors <- err
		}(i)
	}
	group.Wait()
	close(errors)
	successful := 0
	for err := range errors {
		if err == nil {
			successful++
		} else if problem, ok := err.(*failure); !ok || problem.status != 409 {
			t.Fatal(err)
		}
	}
	if successful != 1 {
		t.Fatal("race successes", successful)
	}
	result := want(t, f.request(t, "GET", f.base+"/snapshot", nil, f.guest, false, false), 200)
	if len(waiting(result)) != len(before)-1 || str(object(result, "playback"), "itemId") != str(before[0], "id") {
		t.Fatal("race advanced more than one song", result)
	}
}

func TestFairPartyStopPersistsAndReactionsHaveIndependentCursor(t *testing.T) {
	f := newFairFixture(t)
	f.send(t, f.guest, "ADD", document{"songId": "song-a"}, 200)
	f.send(t, f.guest, "STOP", nil, 200)
	old := str(object(f.state, "playback"), "attemptId")
	f.send(t, f.other, "ADD", document{"songId": "song-b"}, 200)
	if !boolean(f.state, "manualStopped") || str(object(f.state, "playback"), "attemptId") != old {
		t.Fatal("add resumed stopped session")
	}
	f.send(t, f.guest, "START", nil, 200)
	if boolean(f.state, "manualStopped") || str(object(f.state, "playback"), "attemptId") == old {
		t.Fatal("explicit start did not resume queue")
	}
	path := "/api/v1/party/sessions/" + str(f.state, "id") + "/reactions"
	revision := f.revision
	for i := range 3 {
		want(t, f.request(t, "POST", path, document{"requestId": fmt.Sprint(i), "kind": "LIKE"}, f.guest, false, false), 200)
	}
	want(t, f.request(t, "POST", path, document{"requestId": "0", "kind": "LIKE"}, f.guest, false, false), 200)
	want(t, f.request(t, "POST", path, document{"requestId": "4", "kind": "CLAP"}, f.guest, false, false), 429)
	result := want(t, f.request(t, "GET", f.base+"/snapshot", nil, f.guest, false, false), 200)
	if number(result, "reactionCursor") != 3 || number(result, "revision") != revision || len(array(result, "reactions")) != 3 {
		t.Fatal("interaction changed queue or replayed duplicate", result)
	}
	f.clock = f.clock.Add(16 * time.Second)
	result = want(t, f.request(t, "GET", f.base+"/snapshot", nil, f.guest, false, false), 200)
	if len(array(result, "reactions")) != 0 || boolean(result, "tvOnline") {
		t.Fatal("expired effect or TV presence survived", result)
	}
}

func TestFairPartyPriorityRestoresTurnAndSingleOwnerSurvivesRemoval(t *testing.T) {
	f := newFairFixture(t)
	for _, who := range []string{f.guest, f.guest, f.guest, f.other, f.other} {
		f.send(t, who, "ADD", document{"songId": "song-a"}, 200)
	}
	q := waiting(f.state)
	if str(q[0], "ownerName") != "小红" || str(q[1], "ownerName") != "小明" {
		t.Fatal("round rotation", q)
	}
	normalNext, remainingOther := str(q[0], "id"), str(q[2], "id")
	f.send(t, f.guest, "TOP", document{"itemId": q[3]["id"]}, 200)
	f.send(t, f.other, "NEXT", nil, 200)
	if str(waiting(f.state)[0], "id") != normalNext {
		t.Fatal("priority consumed the normal turn", f.state)
	}
	f.send(t, f.guest, "REMOVE", document{"itemId": remainingOther}, 403)
	f.send(t, f.device, "REMOVE", document{"itemId": remainingOther}, 200)
	f.send(t, f.guest, "NEXT", nil, 200)
	if str(object(f.state, "playback"), "itemId") != normalNext || len(waiting(f.state)) != 1 {
		t.Fatal("removal lost remaining owner", f.state)
	}
	f.send(t, f.guest, "SHUFFLE", nil, 200)
	f.send(t, f.guest, "NEXT", nil, 200)
	f.send(t, f.guest, "NEXT", nil, 200)
	if len(waiting(f.state)) != 0 || str(object(f.state, "playback"), "state") != "ENDED" {
		t.Fatal("single-owner queue failed to end", f.state)
	}
	f.send(t, f.guest, "ADD", document{"songId": "song-a"}, 200)
	if str(object(f.state, "playback"), "state") != "PREPARING" {
		t.Fatal("new song after natural queue end did not start")
	}
}

func TestFairPartyRecoveryAndLegacyMigrationKeepSongsAndInvalidateCommands(t *testing.T) {
	f := newFairFixture(t)
	f.send(t, f.guest, "ADD", document{"songId": "song-a"}, 200)
	old := str(object(f.state, "playback"), "attemptId")
	recovered := want(t, f.request(t, "POST", f.base+"/recover", nil, f.device, false, false), 200)
	if !boolean(recovered, "manualStopped") || len(waiting(recovered)) != 1 || len(array(recovered, "capabilities")) != 0 || str(recovered, "queuePolicy") != "LEGACY" {
		t.Fatal("recovery retained old capability or lost song", recovered)
	}
	for _, raw := range array(recovered, "commandResults") {
		if str(raw.(map[string]any), "status") == "SENT" {
			t.Fatal("recovery left an execution pending forever")
		}
	}
	f.state, f.revision = recovered, number(recovered, "revision")
	f.send(t, f.guest, "SEEK", document{"attemptId": old, "positionMs": 1000}, 409)
	// An older TV can leave a song handed off but unconfirmed. Capability
	// migration returns that exact song to WAITING, ready for explicit start.
	f.send(t, f.device, "NEXT", nil, 200)
	if boolean(object(f.state, "playback"), "confirmed") {
		t.Fatal("legacy handoff unexpectedly confirmed")
	}
	f.state = want(t, f.request(t, "POST", f.base+"/capabilities", document{"capabilities": []string{"AUTO_ADVANCE"}}, f.device, false, false), 200)
	f.revision = number(f.state, "revision")
	if len(waiting(f.state)) != 1 || len(object(f.state, "playback")) != 0 || !boolean(f.state, "manualStopped") {
		t.Fatal("unconfirmed legacy song was lost during migration", f.state)
	}
	f.send(t, f.guest, "START", nil, 200)
	if !boolean(object(f.state, "playback"), "confirmed") || boolean(f.state, "manualStopped") {
		t.Fatal("explicit start after migration failed")
	}
}
