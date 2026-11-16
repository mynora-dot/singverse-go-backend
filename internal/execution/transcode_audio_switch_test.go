package execution

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestBrowserRolePlaybackKeepsAlternateAudioInOneHLSResource(t *testing.T) {
	media := map[string]any{"duration": 120.25, "streams": append([]any{
		map[string]any{"id": "stream:0", "kind": "video", "codec": "h264"},
	}, audioStreams(2)...)}
	targets := make(chan map[string]any, 1)
	var executions sync.Map
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/internal/v1/transcoding/")
		switch path {
		case "negotiate":
			json.NewEncoder(w).Encode(map[string]any{"mode": "DIRECT", "media": media})
		case "executions":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			selected := obj(body["target"])
			targets <- selected
			mapping := []any{}
			for _, item := range array(media["streams"]) {
				stream := obj(item)
				if str(stream, "kind") == "audio" && selected["audio_id"] != nil && str(stream, "id") != str(selected, "audio_id") {
					continue
				}
				mapping = append(mapping, map[string]any{"sourceId": str(stream, "id"), "kind": str(stream, "kind"), "outputIndex": len(mapping)})
			}
			value := map[string]any{"id": str(body, "id"), "state": "RUNNING", "ready": true, "offset": 0, "trackMapping": mapping}
			executions.Store(str(body, "id"), value)
			json.NewEncoder(w).Encode(value)
		default:
			parts := strings.Split(path, "/")
			if len(parts) == 3 && parts[0] == "executions" && parts[2] == "lease" {
				if value, ok := executions.Load(parts[1]); ok {
					json.NewEncoder(w).Encode(value)
					return
				}
			}
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer node.Close()
	f := executionFixture(t, node.URL)
	f.s.testProbe(t, f.song, array(media["streams"]))
	if _, err := f.s.correctRoles(t.Context(), f.song, "stream:2", "stream:1", 0); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, owner, family, role, explicitAudio, selected string
		force, legacy                                      bool
	}{
		{name: "browser original", owner: "admin", family: "browser", role: "original"},
		{name: "browser backing", owner: "admin", family: "browser", role: "backing"},
		{name: "browser compatibility retry", owner: "admin", family: "browser", role: "backing", force: true},
		{name: "older browser keeps selected stream", owner: "admin", family: "browser", role: "backing", legacy: true, selected: "stream:1"},
		{name: "TV keeps existing role selection", owner: "tv:one", family: "browser", role: "backing", selected: "stream:1"},
		{name: "other clients keep selected stream", owner: "admin", family: "android", role: "original", selected: "stream:2"},
		{name: "explicit track preview stays single track", owner: "admin", family: "browser", explicitAudio: "stream:1", selected: "stream:1"},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := map[string]any{"client": map[string]any{"family": test.family, "hls": true}, "force": test.force, "switchableAudio": !test.legacy}
			if test.role != "" {
				body["audioRole"] = test.role
			}
			if test.explicitAudio != "" {
				body["audio_id"] = test.explicitAudio
			}
			value, err := f.s.Negotiate(t.Context(), f.song, test.owner, body, "/original", "/sessions")
			if err != nil || str(value, "mode") != "HLS" || str(value, "status") != "READY" {
				t.Fatal("switchable playback must not fall through to raw default audio", value, err)
			}
			selected := <-targets
			mode := "REMUX"
			if test.force {
				mode = "TRANSCODE"
			}
			if str(selected, "audio_id") != test.selected || str(selected, "mode") != mode {
				t.Fatal("unexpected compute target", selected)
			}
			mapping := array(obj(value["execution"])["trackMapping"])
			count := 3
			if test.selected != "" {
				count = 2
			}
			if len(mapping) != count || str(obj(value["audioRoles"]), "originalId") != "stream:2" {
				t.Fatal("role snapshot or alternate audio mapping lost", value)
			}
		})
	}
}

func TestBrowserAlternateAudioDoesNotSilentlyUseRawDefaultWhenComputeUnavailable(t *testing.T) {
	f := executionFixture(t, "")
	f.s.testProbe(t, f.song, audioStreams(2))
	if _, err := f.s.correctRoles(t.Context(), f.song, "stream:2", "stream:1", 0); err != nil {
		t.Fatal(err)
	}
	_, err := f.s.Negotiate(t.Context(), f.song, "admin", map[string]any{
		"audioRole": "original", "switchableAudio": true, "client": map[string]any{"family": "browser", "hls": true},
	}, "/original", "/sessions")
	requireErrorStatus(t, err, 503)
}
