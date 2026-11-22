package metadata

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/mynora-dot/singverse-go-backend/internal/metastandard"
)

func TestCandidateLyricsReviewPersistsSelectedRecording(t *testing.T) {
	f := setup(t)
	f.settings(t, false)
	_, ids := f.local(t, "Artist - Test.mp3", "Artist - Other.mp3")
	id := ids[0]
	base := "/api/v1/metadata/songs/" + id
	songID, variantID, err := f.lib.FormalIDs(t.Context(), f.db, id)
	if err != nil {
		t.Fatal(err)
	}
	existing := map[string]any{"timing": "none", "text": "已有的歌词"}
	if err = f.lib.Formal().Decide(t.Context(), f.db, variantID, map[string]any{"lyrics": existing}, metastandard.Origin{Kind: "MANUAL"}); err != nil {
		t.Fatal(err)
	}
	candidate := f.candidate(t, id, testTrack("候选歌名"))
	previewPath := base + "/candidates/" + candidate + "/lyrics"
	applyPath := base + "/review/apply"
	review := request(t, f.router, "GET", base+"/review", nil, 200)
	if encode(path(review, "values", "lyrics")) != encode(existing) {
		t.Fatal("missing current lyrics", review["values"])
	}
	input := ReviewApply{RequestID: uuid(), CandidateID: &candidate, Fingerprint: text(review["fingerprint"]), Version: text(review["version"]), Fields: []string{"lyrics"}}
	beforePreview := request(t, f.router, "POST", applyPath, input, 200)
	if encode(beforePreview["skipped"]) != `["lyrics"]` || encode(path(beforePreview, "after", "lyrics")) != encode(existing) {
		t.Fatal("an unavailable preview replaced current lyrics", beforePreview)
	}

	previewText := "[00:01.23]测试歌词第一句\n[00:07.89]测试歌词第二句"
	calls := 0
	f.s.guards["NETEASE"].next = time.Time{}
	f.s.client.Transport = artworkTransport(func(req *http.Request) (*http.Response, error) {
		calls++
		if req.URL.Host != "music.163.com" || req.URL.Path != "/api/song/lyric" || req.URL.Query().Get("id") != "42" || req.Header.Get("Cookie") != "" {
			t.Error("unexpected lyric request", req.URL)
		}
		return lyricFixture(encode(map[string]any{"code": 200, "lrc": map[string]string{"lyric": previewText}})), nil
	})
	request(t, f.router, "POST", "/api/v1/metadata/songs/"+ids[1]+"/candidates/"+candidate+"/lyrics", nil, 400)
	preview := request(t, f.router, "POST", previewPath, nil, 200)
	if preview["status"] != "AVAILABLE" || path(preview, "lyrics", "timing") != "lrc" || path(preview, "lyrics", "text") != previewText {
		t.Fatal("timed text was not preserved", preview)
	}
	if cached := request(t, f.router, "POST", previewPath, nil, 200); encode(cached) != encode(preview) || calls != 1 {
		t.Fatal("the preview changed after the first fetch", cached, calls)
	}
	if unchanged := request(t, f.router, "GET", base+"/review", nil, 200); unchanged["version"] != review["version"] || encode(path(unchanged, "values", "lyrics")) != encode(existing) {
		t.Fatal("preview wrote to the formal lyrics field", unchanged["values"])
	}
	input.RequestID = uuid()
	input.Edits = map[string]any{"lyrics": map[string]any{"timing": "none", "text": "客户端提供的歌词"}}
	request(t, f.router, "POST", applyPath, input, 400)
	input.Edits = nil
	input.Fields = []string{"album"}
	otherApplication := request(t, f.router, "POST", applyPath, input, 200)
	if encode(path(otherApplication, "after", "lyrics")) != encode(existing) {
		t.Fatal("applying another field changed lyrics", otherApplication["after"])
	}
	input.RequestID = uuid()
	input.Fields = []string{"lyrics"}
	request(t, f.router, "POST", applyPath, input, 409)
	review = request(t, f.router, "GET", base+"/review", nil, 200)
	input.Version = text(review["version"])
	applied := request(t, f.router, "POST", applyPath, input, 200)
	if encode(applied["updated"]) != `["lyrics"]` || path(applied, "after", "lyrics", "text") != previewText || path(applied, "metadata", "variant", "lyrics", "text") != previewText {
		t.Fatal("application and preview differ", applied)
	}
	if replay := request(t, f.router, "POST", applyPath, input, 200); encode(replay) != encode(applied) || calls != 1 {
		t.Fatal("retry did not use the receipt", replay, calls)
	}
	if formal := request(t, f.router, "GET", "/api/v1/metadata/entities/"+id+"/lyrics", nil, 200); path(formal, "lyrics", "text") != previewText {
		t.Fatal("formal lyric reader did not get the confirmed text", formal)
	}
	variant, err := f.lib.Formal().Read(t.Context(), f.db, variantID)
	if err != nil {
		t.Fatal(err)
	}
	origin := variant.States["lyrics"].Origin
	if variant.States["lyrics"].Decision != "SET" || origin.Kind != "PLATFORM" || origin.Provider != "NETEASE" || origin.ExternalID != "42" {
		t.Fatal("missing recording provenance", variant.States["lyrics"])
	}
	song, err := f.lib.Formal().Read(t.Context(), f.db, songID)
	if err != nil || song.Fields["lyrics"] != nil {
		t.Fatal("lyrics were not scoped to the recording variant", song.Fields, err)
	}
}

func TestPlatformLyricsProtocols(t *testing.T) {
	const body = "[00:01.234]测试歌词"
	const kugouHash = "a123456789012345678901234567890b"
	const miguPath = "/data/oss/resource/a1/b2/c3/test"
	tests := []struct {
		provider, id, host, ref, response, expected string
	}{
		{"NETEASE", "13", "music.163.com", "/api/song/lyric", `{"code":200,"lrc":{"lyric":"` + body + `"}}`, body},
		{"QQ", "00Four123", "c.y.qq.com", "/lyric/fcgi-bin/fcg_query_lyric_new.fcg", `{"code":0,"retcode":0,"lyric":"` + body + ` &amp; 一起"}`, body + " & 一起"},
		{"KUGOU", kugouHash, "lyrics.kugou.com", "/download", `{"status":200,"content":"` + base64.StdEncoding.EncodeToString([]byte(body)) + `"}`, body},
		{"KUWO", "22", "www.kuwo.cn", "/openapi/v1/www/lyric/getlyric", `{"code":200,"data":{"lrclist":[{"time":"1.234","lineLyric":"测试歌词"}]}}`, body},
		{"MIGU", "31", miguLyricsHost, miguPath, "\ufeff" + body, body},
	}
	for _, tt := range tests {
		t.Run(tt.provider, func(t *testing.T) {
			s := &Service{client: &http.Client{Transport: artworkTransport(func(req *http.Request) (*http.Response, error) {
				if req.URL.Scheme != "https" || req.Header.Get("Cookie") != "" {
					t.Error("unexpected authentication or scheme", req.URL)
				}
				q := req.URL.Query()
				if tt.provider == "KUGOU" && req.URL.Host == "krcs.kugou.com" {
					if req.URL.Path != "/search" || q.Get("hash") != kugouHash {
						t.Error("lyrics search did not use the recording hash", req.URL)
					}
					return lyricFixture(`{"status":200,"candidates":[{"id":"44","accesskey":"abc"}]}`), nil
				}
				if req.URL.Host != tt.host || req.URL.Path != tt.ref {
					t.Error("unexpected lyrics endpoint", req.URL)
				}
				if tt.provider == "QQ" && (q.Get("songmid") != tt.id || q.Get("nobase64") != "1") || tt.provider == "KUWO" && q.Get("musicId") != tt.id || tt.provider == "KUGOU" && (q.Get("id") != "44" || q.Get("accesskey") != "abc" || q.Get("fmt") != "lrc") {
					t.Error("unexpected lyrics query", req.URL)
				}
				return lyricFixture(tt.response), nil
			})}}
			track := Track{ExternalID: &tt.id, LyricsPath: miguPath}
			lyrics, err := (&musicProvider{service: s, name: tt.provider}).lyrics(context.Background(), track)
			if err != nil || lyrics["timing"] != "lrc" || lyrics["text"] != tt.expected {
				t.Fatal("lyrics and timeline were not retained", lyrics, err)
			}
		})
	}
}

func TestPlatformLyricsRejectInvalidOrEmptyContent(t *testing.T) {
	for _, raw := range []string{"", "[ti:歌名]\r\n[00:00.00]", "[id:abc]\n[00:00.00]暂无歌词"} {
		if value, err := lyricsFromText(raw); value != nil || err != nil {
			t.Fatal("blank platform lyrics were treated as available", value, err)
		}
	}
	for _, raw := range []string{"<html>错误</html>", "{\"error\":true}", "\x00非法内容", "\xff", strings.Repeat("a", platformLyricsLimit)} {
		if value, err := lyricsFromText(raw); value != nil || err == nil {
			t.Fatal("invalid platform lyrics were accepted", len(raw), err)
		}
	}
	for _, raw := range []string{
		"https://other.example/data/oss/resource/file", "https://d.musicapp.migu.cn:8443/data/oss/resource/file",
		"https://d.musicapp.migu.cn/data/oss/resource/%2e%2e/file", "https://d.musicapp.migu.cn/data/oss/resource/file?url=http://invalid",
	} {
		if miguLyricsPath(raw) != "" {
			t.Fatal("unexpected Migu lyric resource accepted", raw)
		}
	}
}

func lyricFixture(body string) *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}
