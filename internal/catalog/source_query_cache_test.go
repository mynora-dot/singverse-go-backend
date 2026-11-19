package catalog

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func resetSourceWait(host string) {
	sourceGate.Lock()
	defer sourceGate.Unlock()
	if sourceGate.next == nil {
		sourceGate.next = map[string]time.Time{}
	}
	sourceGate.next[host] = time.Time{}
}

func TestArtistSourceCacheSkipsNetworkAndIsolatesPersonalKey(t *testing.T) {
	prior := sourceClient
	t.Cleanup(func() { sourceClient = prior })
	var calls atomic.Int32
	sourceClient = &http.Client{Transport: artworkTransport(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		resetSourceWait(r.URL.Hostname())
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"artistbackground":[{"url":"https://assets.fanart.tv/image.jpg"}]}`))}, nil
	})}
	resetSourceWait("webservice.fanart.tv")
	for range 2 {
		out, err := sourceJSON(t.Context(), "https://webservice.fanart.tv/v3.2/music/artist", "webservice.fanart.tv", "first-key")
		if err != nil || len(list(out["artistbackground"])) != 1 {
			t.Fatal(out, err)
		}
		out["artistbackground"] = nil
	}
	if calls.Load() != 1 {
		t.Fatal("source hit fetched upstream", calls.Load())
	}
	if _, err := sourceJSON(t.Context(), "https://webservice.fanart.tv/v3.2/music/artist", "webservice.fanart.tv", "replacement"); err != nil || calls.Load() != 2 {
		t.Fatal("personal key change reused cache", calls.Load(), err)
	}
}

func TestIndependentArtistSourceHostsDoNotWaitForEachOther(t *testing.T) {
	prior := sourceClient
	t.Cleanup(func() { sourceClient = prior })
	started, release := make(chan string, 2), make(chan struct{})
	sourceClient = &http.Client{Transport: artworkTransport(func(r *http.Request) (*http.Response, error) {
		started <- r.URL.Hostname()
		select {
		case <-release:
		case <-r.Context().Done():
			return nil, r.Context().Err()
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"artists":[]}`))}, nil
	})}
	resetSourceWait("musicbrainz.org")
	resetSourceWait("www.theaudiodb.com")
	done := make(chan error, 2)
	for _, host := range []string{"musicbrainz.org", "www.theaudiodb.com"} {
		go func() { _, err := sourceJSON(context.Background(), "https://"+host+"/fixture", host, ""); done <- err }()
	}
	count := 0
	for count < 2 {
		select {
		case <-started:
			count++
		case <-time.After(time.Second):
			close(release)
			<-done
			<-done
			t.Fatal("unrelated hosts serialized behind a global network lock")
		}
	}
	close(release)
	for range 2 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}

func TestReplacingArtistSourceClientCannotReuseOldCache(t *testing.T) {
	prior := sourceClient
	t.Cleanup(func() { sourceClient = prior })
	for _, status := range []int{200, 503} {
		sourceClient = &http.Client{Transport: artworkTransport(func(r *http.Request) (*http.Response, error) {
			resetSourceWait(r.URL.Hostname())
			return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"artists":[]}`))}, nil
		})}
		resetSourceWait("www.theaudiodb.com")
		_, err := sourceJSON(t.Context(), "https://www.theaudiodb.com/fixture", "www.theaudiodb.com", "")
		if (err == nil) != (status == 200) {
			t.Fatal("new client reused the old client's cached success", err)
		}
	}
}
