package app

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestPartyCatalogFavoritesAndMissingFeedbackHTTP(t *testing.T) {
	a := application(t)
	admin := newBusinessClient(t, a)
	admin.call("POST", "/api/v1/auth/login", map[string]any{"username": "admin", "password": "admin"}, "", 200)
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(directory, "歌手 - 亲友验收.mp4"), []byte("isolated source"), 0600); err != nil {
		t.Fatal(err)
	}
	source := admin.call("POST", "/api/v1/media-sources", map[string]any{"name": "亲友验收", "type": "LOCAL", "location": directory}, "", 201)
	admin.call("POST", "/api/v1/media-sources/"+stringField(source, "id")+"/scan", map[string]any{}, "", 200)
	page := admin.call("GET", "/api/v1/media-sources/"+stringField(source, "id")+"/songs", nil, "", 200)
	songID := stringField(object(page["items"].([]any)[0]), "id")
	grant := admin.call("POST", "/api/v1/auth/tv-session", map[string]any{"name": "客厅"}, "", 200)
	device := stringField(grant, "token")
	session := admin.call("POST", "/api/v1/tv/sessions", map[string]any{}, device, 200)
	invite := admin.call("POST", "/api/v1/tv/sessions/"+stringField(session, "id")+"/invitations", map[string]any{}, device, 200)
	participant := admin.call("POST", "/api/v1/party/join", map[string]any{"invitation": invite["invitation"], "name": "小明"}, "", 200)
	token := stringField(participant, "token")
	guest := &businessClient{t: t, origin: admin.origin, client: &http.Client{}}
	for _, path := range []string{"library/songs", "library/artists", "library/recent", "library/popular", "favorites", "favorites/ids"} {
		guest.call("GET", "/api/v1/party/"+path, nil, token, 200)
	}
	guest.call("PUT", "/api/v1/party/favorites", map[string]any{"songIds": []string{songID}, "selected": true}, token, 200)
	state := admin.call("GET", "/api/v1/favorites/ids", nil, "", 200)
	if len(state["songIds"].([]any)) != 1 {
		t.Fatal(state)
	}
	admin.call("PUT", "/api/v1/favorites", map[string]any{"songIds": []string{songID}, "selected": false}, "", 200)
	state = guest.call("GET", "/api/v1/party/favorites/ids", nil, token, 200)
	if len(state["songIds"].([]any)) != 0 {
		t.Fatal(state)
	}
	input := map[string]any{"requestId": requestID(), "title": "想唱的歌曲", "submittedBy": "伪造身份"}
	first := guest.call("POST", "/api/v1/party/missing-song-feedback", input, token, 201)
	again := guest.call("POST", "/api/v1/party/missing-song-feedback", input, token, 201)
	if first["id"] != again["id"] || first["submittedBy"] != "小明" || first["source"] != "PARTY" {
		t.Fatal(first, again)
	}
	found := admin.call("GET", "/api/v1/missing-song-feedback?q=想唱的歌曲", nil, "", 200)
	if found["totalElements"] != float64(1) {
		t.Fatal(found)
	}
	guest.call("GET", "/api/v1/missing-song-feedback", nil, token, 401)
	admin.call("DELETE", "/api/v1/admin/tv-devices/"+stringField(grant, "deviceId"), nil, "", 200)
	guest.call("GET", "/api/v1/party/favorites/ids", nil, token, 401)
}
