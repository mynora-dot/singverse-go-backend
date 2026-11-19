package qishui

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type roundTrip func(*http.Request) (*http.Response, error)

func (fn roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return fn(r) }

func response(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func newTestClient(t *testing.T, fn roundTrip) *Client {
	t.Helper()
	c, err := New(&http.Client{Transport: fn})
	if err != nil {
		t.Fatal(err)
	}
	c.interval = 0
	t.Cleanup(func() { c.Close() })
	return c
}

const searchResult = `{"err_no":0,"data":{"result_groups":[{"id":"tracks","data":[{"entity":{"track":{"base_info":{"id":7146244493748996128,"name":"传奇"}}}}]}]}}`

func TestAnonymousSearchSharesTokenAndKeepsIdentity(t *testing.T) {
	tokens, searches := 0, 0
	deviceID := ""
	c := newTestClient(t, func(r *http.Request) (*http.Response, error) {
		q := r.URL.Query()
		if r.URL.Scheme != "https" || r.Header.Get("User-Agent") != userAgent || q.Get("aid") != "765213" || q.Get("app_name") != "byte_autoservice_lite" {
			t.Error("incorrect car protocol")
		}
		if !regexp.MustCompile(`^[0-9]{19}$`).MatchString(q.Get("device_id")) || q.Get("did") != q.Get("device_id") || q.Get("iid") != q.Get("device_id") || q.Get("_timestamp") == "" || q.Get("_nonce") == "" {
			t.Error("missing car device parameters")
		}
		if deviceID != "" && deviceID != q.Get("device_id") {
			t.Error("device identity changed during a token generation")
		}
		deviceID = q.Get("device_id")
		if r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" || r.Header.Get("X-Helios") != "" || r.Header.Get("X-Medusa") != "" {
			t.Error("public catalogue used account or signing credentials")
		}
		if r.URL.Hostname() == "open.qishui.com" {
			tokens++
			if r.Method != "GET" || r.URL.Path != prefix+"/client/token/get/" || q.Get("trigger_type") != "cold_start" || r.Header.Get("Access-Token") != "" {
				t.Error("incorrect anonymous token exchange")
			}
			return response(200, `{"err_no":0,"client_token":"anonymous-token"}`), nil
		}
		searches++
		if r.URL.Hostname() != "open.douyin.com" || r.URL.Path != prefix+"/search/" || r.Method != "POST" || r.Header.Get("Access-Token") != "anonymous-token" {
			t.Error("incorrect catalogue endpoint or credential")
		}
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil || body["q"] != "传奇 郭沁" || body["cursor"] != "0" || body["search_id"] == "" || body["search_id"] != body["from_search_id"] || len(object(body["common_params"])) != 0 {
			t.Error("incorrect search body")
		}
		if body["search_type"] != []string{"track", "artist"}[searches-1] {
			t.Error("song and artist searches did not use their own kind")
		}
		return response(200, searchResult), nil
	})
	for _, kind := range []string{"track", "artist"} {
		root, err := c.Search(context.Background(), "传奇 郭沁", kind)
		if err != nil {
			t.Fatal(err)
		}
		encoded, _ := json.Marshal(root)
		if !strings.Contains(string(encoded), "7146244493748996128") {
			t.Fatal("long music ID lost precision")
		}
	}
	if tokens != 1 || searches != 2 {
		t.Fatal("song and artist reads did not share the cached token", tokens, searches)
	}
}

func TestExpiredTokenRefreshIsBounded(t *testing.T) {
	for _, tc := range []struct {
		name, rejection string
		always, wantErr bool
		wantTokens      int
		wantSearches    int
	}{
		{"expired then renewed", `{"err_no":28001008}`, false, false, 2, 2},
		{"nested expired", `{"err_no":0,"data":{"error_code":28001008}}`, false, false, 2, 2},
		{"expired twice", `{"err_no":28001008}`, true, true, 2, 2},
		{"other rejection", `{"err_no":28001003,"message":"private-details"}`, true, true, 1, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tokens, searches := 0, 0
			c := newTestClient(t, func(r *http.Request) (*http.Response, error) {
				if r.Method == "GET" {
					tokens++
					return response(200, `{"client_token":"token"}`), nil
				}
				searches++
				if searches == 1 || tc.always {
					return response(200, tc.rejection), nil
				}
				return response(200, searchResult), nil
			})
			_, err := c.Search(context.Background(), "Song", "track")
			if (err != nil) != tc.wantErr || tokens != tc.wantTokens || searches != tc.wantSearches {
				t.Fatal("unexpected refresh behavior", err, tokens, searches)
			}
			if err != nil && strings.Contains(err.Error(), "private-details") {
				t.Fatal("upstream error body leaked")
			}
		})
	}
}

func TestConcurrentReadsShareTokenAndCanceledQueueDoesNotCallUpstream(t *testing.T) {
	var tokens, searches atomic.Int32
	c := newTestClient(t, func(r *http.Request) (*http.Response, error) {
		if r.Method == "GET" {
			tokens.Add(1)
			return response(200, `{"client_token":"token"}`), nil
		}
		searches.Add(1)
		return response(200, searchResult), nil
	})
	var wg sync.WaitGroup
	for range 6 {
		wg.Go(func() {
			if _, err := c.Search(context.Background(), "Song", "track"); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if tokens.Load() != 1 || searches.Load() != 1 {
		t.Fatal("concurrent identical requests did not share credential and result", tokens.Load(), searches.Load())
	}
	c.permit <- struct{}{}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := c.Search(ctx, "Uncached song", "track"); err == nil {
		t.Fatal("canceled queued search succeeded")
	}
	<-c.permit
	if searches.Load() != 1 {
		t.Fatal("canceled queued request reached upstream")
	}
}

func TestInvalidSearchResponsesAreFailures(t *testing.T) {
	for _, body := range []string{"", "null", "[]", `{} {}`, `{"data":{}}`, `{"data":[]}`, `{"data":{"result_groups":null}}`, `{"err_no":"invalid"}`, strings.Repeat("x", maxJSON+1)} {
		c := newTestClient(t, func(r *http.Request) (*http.Response, error) {
			if r.Method == "GET" {
				return response(200, `{"client_token":"token"}`), nil
			}
			return response(200, body), nil
		})
		if _, err := c.Search(context.Background(), "Song", "track"); err == nil {
			t.Fatal("invalid response became no matches")
		}
	}
	for _, body := range []string{`{}`, `{"client_token":""}`, `{"client_token":"invalid\nheader"}`} {
		c := newTestClient(t, func(r *http.Request) (*http.Response, error) {
			if r.Method != "GET" {
				t.Error("invalid token reached catalogue")
			}
			return response(200, body), nil
		})
		if _, err := c.Search(context.Background(), "Song", "track"); err == nil {
			t.Fatal("invalid token accepted")
		}
	}
}

func TestRateLimitAndServiceCooldownKeepStatusAndDoNotRetry(t *testing.T) {
	for _, status := range []int{429, 503} {
		calls := 0
		c := newTestClient(t, func(*http.Request) (*http.Response, error) {
			calls++
			reply := response(status, "private-token")
			reply.Header.Set("Retry-After", "120")
			return reply, nil
		})
		for range 2 {
			_, err := c.Search(context.Background(), "Song", "track")
			var failed *Failure
			if !errors.As(err, &failed) || failed.StatusCode() != status || failed.RetryAfter < time.Minute || strings.Contains(failed.Error(), "private-token") {
				t.Fatal("cooldown lost status or leaked upstream body", err)
			}
		}
		if calls != 1 {
			t.Fatal("cooldown retried upstream", calls)
		}
	}
}

func TestRedirectDoesNotForwardAnonymousToken(t *testing.T) {
	calls := 0
	c := newTestClient(t, func(r *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return response(200, `{"client_token":"token"}`), nil
		}
		if r.URL.Hostname() != "open.douyin.com" {
			t.Error("credential forwarded to redirect host")
		}
		reply := response(302, "")
		reply.Header.Set("Location", "https://other.example/private")
		return reply, nil
	})
	if _, err := c.Search(context.Background(), "Song", "track"); err == nil || calls != 2 {
		t.Fatal("redirect was followed or accepted", calls, err)
	}
}

func TestPublicTrackAndPlaylistReadContract(t *testing.T) {
	tokens, reads := 0, 0
	c := newTestClient(t, func(r *http.Request) (*http.Response, error) {
		if r.Method == "GET" {
			tokens++
			return response(200, `{"client_token":"anonymous-token"}`), nil
		}
		reads++
		var input map[string]any
		if json.NewDecoder(r.Body).Decode(&input) != nil {
			t.Fatal("invalid JSON body")
		}
		if _, ok := input["common_params"].(map[string]any); !ok {
			t.Fatal("missing anonymous common parameters")
		}
		if r.Header.Get("Access-Token") != "anonymous-token" || r.Header.Get("Cookie") != "" {
			t.Fatal("incorrect public credentials")
		}
		switch r.URL.Path {
		case prefix + "/media/mget/":
			if object(arrayOf(input["media_list"])[0])["id"] != "7146244493748996128" {
				t.Fatal("long ID changed in request")
			}
			return response(200, `{"data":{"media_list":[{"media_id":7146244493748996128,"track_entity":{"base_info":{"id":7146244493748996128,"name":"fixture"}}}]}}`), nil
		case prefix + "/playlist/detail/":
			if input["playlist_id"] != "123" || input["cursor"] != "opaque-next" {
				t.Fatal("playlist cursor changed")
			}
			return response(200, `{"data":{"playlist":{"base_info":{"id":"123"}},"media_entities":[],"has_more":false}}`), nil
		}
		t.Fatal("unexpected public endpoint")
		return nil, nil
	})
	row, err := c.Track(t.Context(), "7146244493748996128")
	if err != nil || object(row["base_info"])["id"].(json.Number).String() != "7146244493748996128" {
		t.Fatal("long ID changed in detail", err)
	}
	if _, err = c.Playlist(t.Context(), "123", "opaque-next"); err != nil {
		t.Fatal(err)
	}
	if _, err = c.Track(t.Context(), "7146244493748996128"); err != nil {
		t.Fatal(err)
	}
	if _, err = c.Playlist(t.Context(), "123", "opaque-next"); err != nil {
		t.Fatal(err)
	}
	if tokens != 1 || reads != 2 {
		t.Fatal("public reads did not share token", tokens, reads)
	}
}
func arrayOf(v any) []any { a, _ := v.([]any); return a }

func TestSearchPageCacheSeparatesKindCursorAndQuery(t *testing.T) {
	reads := 0
	c := newTestClient(t, func(r *http.Request) (*http.Response, error) {
		if r.Method == "GET" {
			return response(200, `{"client_token":"token"}`), nil
		}
		reads++
		return response(200, searchResult), nil
	})
	root, err := c.Search(t.Context(), "Song", "track")
	if err != nil {
		t.Fatal(err)
	}
	root["result_groups"] = nil
	for _, input := range [][3]string{{"Song", "track", "0"}, {"Song", "artist", "0"}, {"Song", "track", "47"}, {"Other", "track", "0"}} {
		root, err := c.SearchPage(t.Context(), input[0], input[1], input[2])
		if err != nil || root["result_groups"] == nil {
			t.Fatal("mutation escaped cache copy", root, err)
		}
	}
	if reads != 4 {
		t.Fatal("cache key mixed kinds, cursors or keywords", reads)
	}
	c.permit <- struct{}{}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if _, err := c.Search(ctx, "Song", "track"); err != nil || reads != 4 {
		t.Fatal("cache hit queued behind network gate", reads, err)
	}
	<-c.permit
}
