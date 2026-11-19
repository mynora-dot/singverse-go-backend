package metadata

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

func searchFixture(t *testing.T) (*fixture, map[string]any) {
	t.Helper()
	f := setup(t)
	_, ids := f.local(t, "A-Song.mkv")
	request(t, f.router, "PUT", "/api/v1/metadata/settings", Settings{Enabled: true, Providers: append([]string{}, providers...), Threshold: .95}, 200)
	song, err := f.s.song(context.Background(), f.db, ids[0])
	if err != nil {
		t.Fatal(err)
	}
	return f, song
}

func TestSearchStartsIndependentProvidersTogether(t *testing.T) {
	f, song := searchFixture(t)
	started := make(chan string, len(providers))
	release := make(chan struct{})
	for _, name := range providers {
		f.s.providers[name] = contextProvider(func(ctx context.Context) ([]Track, error) {
			started <- name
			select {
			case <-release:
				track := testTrack("Song")
				track.Provider = ptr(name)
				return []Track{track}, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		})
	}
	type response struct {
		result SearchResult
		err    error
	}
	done := make(chan response, 1)
	go func() {
		result, err := f.s.search(context.Background(), song, SearchInput{}, providers)
		done <- response{result, err}
	}()
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	count := 0
collect:
	for count < len(providers) {
		select {
		case <-started:
			count++
		case <-timer.C:
			break collect
		}
	}
	close(release)
	out := <-done
	if count != len(providers) {
		t.Fatalf("only %d/%d providers started while the first provider was waiting", count, len(providers))
	}
	if out.err != nil || len(out.result.Candidates) != len(providers) {
		t.Fatal("concurrent search lost candidates", out.result, out.err)
	}
}

func TestSearchFanoutLatency(t *testing.T) {
	f, song := searchFixture(t)
	for _, name := range providers {
		f.s.providers[name] = contextProvider(func(ctx context.Context) ([]Track, error) {
			timer := time.NewTimer(100 * time.Millisecond)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-timer.C:
			}
			track := testTrack("Song")
			track.Provider = ptr(name)
			return []Track{track}, nil
		})
	}
	start := time.Now()
	result, err := f.s.search(context.Background(), song, SearchInput{}, providers)
	t.Logf("six providers, 100 ms each: total=%s, candidates=%d", time.Since(start).Round(time.Millisecond), len(result.Candidates))
	if err != nil || len(result.Candidates) != len(providers) {
		t.Fatal(result, err)
	}
}

func TestCanceledProviderDoesNotConsumeRateLimit(t *testing.T) {
	f := setup(t)
	f.settings(t, false)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls := 0
	provider := contextProvider(func(context.Context) ([]Track, error) { calls++; return nil, nil })
	for range 32 {
		f.s.guards["NETEASE"].next = time.Time{}
		_, err := f.s.callProvider(ctx, "NETEASE", provider, "Song")
		if !errors.Is(err, context.Canceled) || calls != 0 || !f.s.guards["NETEASE"].next.IsZero() {
			t.Fatal("canceled query entered the provider or advanced its rate limit", err, calls)
		}
	}
}

func TestSearchCancellationStopsAllProviders(t *testing.T) {
	f, song := searchFixture(t)
	started := make(chan struct{}, len(providers))
	for _, name := range providers {
		f.s.providers[name] = contextProvider(func(ctx context.Context) ([]Track, error) {
			started <- struct{}{}
			<-ctx.Done()
			return nil, ctx.Err()
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := f.s.search(ctx, song, SearchInput{}, providers); done <- err }()
	for range providers {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("a provider blocked independent searches")
		}
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal("search ignored request cancellation", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled searches did not release their provider gates")
	}
	for _, guard := range f.s.guards {
		if len(guard.permit) != 0 {
			t.Fatal("a canceled search retained the platform permit")
		}
	}
}

func TestConcurrentSearchRetainsDeduplicationPartialErrorsAndLimit(t *testing.T) {
	f, song := searchFixture(t)
	var calls atomic.Int32
	f.s.providers["NETEASE"] = contextProvider(func(context.Context) ([]Track, error) {
		calls.Add(1)
		tracks := []Track{}
		for i := range 8 {
			track := testTrack("Song")
			track.ExternalID = ptr(fmt.Sprint(i))
			tracks = append(tracks, track, track)
		}
		return tracks, nil
	})
	f.s.providers["QQ"] = contextProvider(func(context.Context) ([]Track, error) {
		tracks := []Track{}
		for i := range 8 {
			track := testTrack("Other")
			track.Provider, track.ExternalID = ptr("QQ"), ptr(fmt.Sprint(i))
			tracks = append(tracks, track)
		}
		return tracks, nil
	})
	f.s.providers["MIGU"] = fakeProvider{err: &problem{503, "平台暂时不可用"}}
	result, err := f.s.search(context.Background(), song, SearchInput{Providers: []string{"NETEASE", "QQ", "MIGU", "NETEASE"}}, providers)
	if err != nil || calls.Load() != 1 || len(result.Candidates) != 10 || len(result.Errors) != 1 || result.Errors[0].Provider != "MIGU" {
		t.Fatal("fanout changed candidate or partial-failure semantics", result, err, calls.Load())
	}
	seen := map[string]bool{}
	for i, candidate := range result.Candidates {
		key := *candidate.Track.Provider + ":" + *candidate.Track.ExternalID
		if seen[key] || i > 0 && candidate.Score > result.Candidates[i-1].Score {
			t.Fatal("candidates are duplicated or no longer ranked by similarity", result)
		}
		seen[key] = true
	}
	_, err = f.s.search(context.Background(), song, SearchInput{Providers: []string{"UNKNOWN"}}, providers)
	if code(err) != 400 || calls.Load() != 1 {
		t.Fatal("invalid platform selection reached upstream", err, calls.Load())
	}
}

type timedSearchProvider struct {
	Provider
	t     *testing.T
	name  string
	calls *atomic.Int32
}

func (p timedSearchProvider) Search(ctx context.Context, keyword string, limit int) ([]Track, error) {
	p.calls.Add(1)
	start := time.Now()
	tracks, err := p.Provider.Search(ctx, keyword, limit)
	message := "ok"
	if err != nil {
		message = safeDetail(err)
	}
	p.t.Logf("%s: %s, candidates=%d, status=%s", p.name, time.Since(start).Round(time.Millisecond), len(tracks), message)
	return tracks, err
}

// Opt-in diagnostics use temporary data and public metadata endpoints only.
func TestLiveMetadataSearchTimings(t *testing.T) {
	if os.Getenv("SINGVERSE_METADATA_SEARCH_LIVE_TEST") != "1" {
		t.Skip("set SINGVERSE_METADATA_SEARCH_LIVE_TEST=1 to query public music catalogues")
	}
	f, song := searchFixture(t)
	counts := map[string]*atomic.Int32{}
	for _, name := range providers {
		counts[name] = &atomic.Int32{}
		f.s.providers[name] = timedSearchProvider{f.s.providers[name], t, name, counts[name]}
	}
	keyword := "你还要我怎样 薛之谦"
	start := time.Now()
	result, err := f.s.search(context.Background(), song, SearchInput{Keyword: &keyword}, providers)
	t.Logf("total=%s, candidates=%d, failed providers=%d", time.Since(start).Round(time.Millisecond), len(result.Candidates), len(result.Errors))
	if err != nil {
		t.Fatal(err)
	}
	before := int32(0)
	for _, count := range counts {
		before += count.Load()
	}
	start = time.Now()
	warm, err := f.s.search(context.Background(), song, SearchInput{Keyword: &keyword}, providers)
	after := int32(0)
	for _, count := range counts {
		after += count.Load()
	}
	t.Logf("warm total=%s, candidates=%d, failed providers=%d, new upstream calls=%d", time.Since(start).Round(time.Millisecond), len(warm.Candidates), len(warm.Errors), after-before)
	if err != nil || len(result.Errors) == 0 && after != before {
		t.Fatal("warm successful queries did not use cache", err)
	}
}
