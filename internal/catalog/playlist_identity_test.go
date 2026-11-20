package catalog

import "testing"

func TestPlaylistSongsExposeRecordingIdentity(t *testing.T) {
	s, r := fixture(t)
	lib := s.library.(*fakeLibrary)
	lib.songs[0]["sourceName"] = "WebDAV 歌曲"
	lib.songs[0]["metadata"] = map[string]any{
		"fields":  []string{"title", "artists", "album", "releaseDate", "durationMs"},
		"track":   map[string]any{"title": "晴天", "artists": []string{"周杰伦"}, "album": "演唱会", "releaseDate": "2020-01-01", "durationMs": 300000},
		"media":   map[string]any{"durationMs": 298000},
		"variant": map[string]any{"kind": "LIVE"},
	}
	desc := "录音版本检查"
	saved := request(t, r, "POST", "/api/v1/playlists", PlaylistSave{RequestID: id(), Name: "同名歌曲", Description: &desc, SongIDs: []string{"song-a", "song-b"}}, 201)
	got := request(t, r, "GET", "/api/v1/playlists/"+str(saved["id"]), nil, 200)
	songs := got["songs"].([]any)
	first := obj(songs[0])
	if str(first["album"]) != "演唱会" || str(first["releaseDate"]) != "2020-01-01" || str(first["version"]) != "LIVE" || num(first["durationMs"]) != 298000 || str(first["sourceName"]) != "WebDAV 歌曲" || str(first["relativePath"]) != "周杰伦-晴天.mp4" {
		t.Fatalf("missing recording identity: %#v", first)
	}
	if str(obj(songs[1])["id"]) != "song-b" {
		t.Fatal("song order changed", songs)
	}
}
