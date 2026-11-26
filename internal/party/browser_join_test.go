package party

import (
	"testing"
	"time"
)

func TestBrowserSelectsOnlineTelevisionAndJoinsAsParticipant(t *testing.T) {
	f := newPartyFixture(t)
	const list = "/api/v1/admin/party/televisions"
	livingToken, livingID := f.device(t)
	otherToken, otherID := f.device(t)
	offlineToken, offlineID := f.device(t)
	revokedToken, revokedID := f.device(t)
	_, _ = f.device(t)
	create := func(token string) string {
		return str(want(t, f.request(t, "POST", "/api/v1/tv/sessions", nil, token, false, false), 200), "id")
	}
	heartbeat := func(token, sid string) document {
		return want(t, f.request(t, "GET", "/api/v1/tv/sessions/"+sid+"/snapshot", nil, token, false, false), 200)
	}
	living, other, offline, revoked := create(livingToken), create(otherToken), create(offlineToken), create(revokedToken)
	heartbeat(offlineToken, offline)
	f.clock = f.clock.Add(15 * time.Second)
	heartbeat(livingToken, living)
	heartbeat(otherToken, other)
	heartbeat(revokedToken, revoked)
	want(t, f.request(t, "DELETE", "/api/v1/admin/tv-devices/"+revokedID, nil, "", true, true), 200)
	want(t, f.request(t, "GET", list, nil, "", false, false), 401)
	want(t, f.request(t, "GET", list, nil, livingToken, false, false), 401)
	response := f.request(t, "GET", list, nil, "", true, false)
	items := array(want(t, response, 200), "items")
	if len(items) != 2 || response.header.Get("Cache-Control") != "no-store" {
		t.Fatal("expected only televisions with current heartbeats", items)
	}
	seen := map[string]string{}
	for _, item := range items {
		v := item.(map[string]any)
		if len(v) != 4 || str(v, "deviceName") == "" || !boolean(v, "acceptingRequests") {
			t.Fatal("unexpected browser picker payload", v)
		}
		seen[str(v, "deviceId")] = str(v, "sessionId")
	}
	if seen[livingID] != living || seen[otherID] != other {
		t.Fatal("online televisions associated with the wrong session", seen)
	}
	join := list + "/" + otherID + "/join"
	input := document{"sessionId": other, "name": "网页点歌"}
	want(t, f.request(t, "POST", join, input, "", true, false), 403)
	want(t, f.request(t, "POST", list+"/"+offlineID+"/join", document{"sessionId": offline, "name": "网页点歌"}, "", true, true), 409)
	want(t, f.request(t, "POST", list+"/"+revokedID+"/join", document{"sessionId": revoked, "name": "网页点歌"}, "", true, true), 409)
	invite := want(t, f.request(t, "POST", "/api/v1/tv/sessions/"+other+"/invitations", nil, otherToken, false, false), 200)
	guest := want(t, f.request(t, "POST", join, input, "", true, true), 200)
	guestToken := str(guest, "token")
	if str(guest, "deviceName") == "" || str(guest, "sessionId") != other || guestToken == "" || boolean(guest, "host") {
		t.Fatal("browser join must yield an ordinary participant in the selected session")
	}
	want(t, f.request(t, "POST", "/api/v1/party/invitations/preview", invite, "", false, false), 200)
	want(t, f.request(t, "GET", "/api/v1/party/sessions/"+living+"/snapshot", nil, guestToken, false, false), 403)
	commands := "/api/v1/party/sessions/" + other + "/commands"
	want(t, f.request(t, "POST", commands, document{"action": "LOCK", "expectedRevision": 0, "requestId": "browser-lock"}, guestToken, false, false), 403)
	want(t, f.request(t, "POST", commands, document{"action": "ADD", "expectedRevision": 0, "requestId": "browser-add", "songId": "song-a"}, guestToken, false, false), 200)
	if len(array(heartbeat(otherToken, other), "queue")) != 1 || len(array(heartbeat(livingToken, living), "queue")) != 0 {
		t.Fatal("the browser request must only reach the selected television")
	}
	want(t, f.request(t, "POST", "/api/v1/tv/sessions/"+other+"/commands", document{"action": "END", "expectedRevision": 1, "requestId": "end-party"}, otherToken, false, false), 200)
	want(t, f.request(t, "POST", join, input, "", true, true), 409)
	heartbeat(otherToken, create(otherToken))
	want(t, f.request(t, "POST", join, input, "", true, true), 409)
}
