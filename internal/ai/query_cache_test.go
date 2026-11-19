package ai

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestAssistantSearchCacheIsolatesConfigurationAndTestsStayLive(t *testing.T) {
	var calls atomic.Int32
	var fail atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if fail.Load() {
			w.WriteHeader(503)
			return
		}
		fmt.Fprint(w, `{"results":[{"url":"https://example.org/article","title":"Result"}]}`)
	}))
	defer server.Close()
	s := &Service{cipher: testCipher{}}
	key, _ := s.cipher.Encrypt("first-key", "purpose")
	c := Snapshot{ID: "search-service", Protocol: "CUSTOM-SEARCH", Endpoint: server.URL, EncryptedKey: &key, CredentialPurpose: "purpose", MaxResults: 5, TimeoutSeconds: 5}
	for range 2 {
		out, err := s.Search(t.Context(), c, "query")
		if err != nil || len(out) != 1 {
			t.Fatal(out, err)
		}
		out[0].Title = "edited"
	}
	if calls.Load() != 1 {
		t.Fatal("cache hit fetched upstream", calls.Load())
	}
	for _, changed := range []Snapshot{
		{ID: c.ID, Protocol: c.Protocol, Endpoint: c.Endpoint + "/other", EncryptedKey: c.EncryptedKey, CredentialPurpose: c.CredentialPurpose, MaxResults: 5, TimeoutSeconds: 5},
		{ID: c.ID, Protocol: c.Protocol, Endpoint: c.Endpoint, EncryptedKey: c.EncryptedKey, CredentialPurpose: c.CredentialPurpose, MaxResults: 1, TimeoutSeconds: 5},
	} {
		if _, err := s.Search(t.Context(), changed, "query"); err != nil {
			t.Fatal(err)
		}
	}
	key, _ = s.cipher.Encrypt("second-key", "purpose")
	if _, err := s.Search(t.Context(), c, "query"); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 4 {
		t.Fatal("endpoint, limit or credential change reused cache", calls.Load())
	}
	for range 2 {
		if _, err := s.testSearch(t.Context(), c, "query"); err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 6 {
		t.Fatal("connection test used cached success", calls.Load())
	}
	fail.Store(true)
	for range 2 {
		if _, err := s.Search(t.Context(), c, "failure"); err == nil {
			t.Fatal("upstream failure lost")
		}
	}
	fail.Store(false)
	if _, err := s.Search(t.Context(), c, "failure"); err != nil || calls.Load() != 9 {
		t.Fatal("failure cached", calls.Load(), err)
	}
}

func TestModelListCacheIsolatesCredentialsAndCanonicalEndpoint(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if !strings.HasSuffix(r.URL.Path, "/models") {
			t.Error("unexpected endpoint")
		}
		fmt.Fprint(w, `{"data":[{"id":"model","name":"Model"}]}`)
	}))
	defer server.Close()
	s := &Service{}
	key := "key"
	v := ModelDiscoveryInput{Provider: "custom", Protocol: "CHAT_COMPLETIONS", Endpoint: server.URL + "/v1", APIKey: &key, TimeoutSeconds: 5}
	for range 2 {
		out, err := s.DiscoverModels(t.Context(), v)
		if err != nil || len(out.Items) != 1 || out.Items[0].ID != "model" {
			t.Fatal(out, err)
		}
		out.Items[0].ID = "edited"
	}
	if calls.Load() != 1 {
		t.Fatal("model cache hit fetched upstream", calls.Load())
	}
	key = "replacement"
	if _, err := s.DiscoverModels(t.Context(), v); err != nil || calls.Load() != 2 {
		t.Fatal("new credentials reused model list", calls.Load(), err)
	}
	v.Endpoint = server.URL + "/other/v1"
	if _, err := s.DiscoverModels(t.Context(), v); err != nil || calls.Load() != 3 {
		t.Fatal("new endpoint reused model list", calls.Load(), err)
	}
}
