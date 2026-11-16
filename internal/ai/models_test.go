package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestModelEndpointBasesAndExactOperations(t *testing.T) {
	for _, test := range []struct{ protocol, input, want string }{
		{"CHAT_COMPLETIONS", "http://127.0.0.1:8080/v1", "http://127.0.0.1:8080/v1/chat/completions"},
		{"CHAT_COMPLETIONS", "https://example.test", "https://example.test/v1/chat/completions"},
		{"RESPONSES", "https://example.test/proxy/v1/", "https://example.test/proxy/v1/responses"},
		{"ANTHROPIC", "https://example.test/v1", "https://example.test/v1/messages"},
		{"GEMINI", "https://example.test", "https://example.test/v1beta"},
		{"GEMINI", "https://example.test/proxy/v1beta/", "https://example.test/proxy/v1beta"},
		{"CHAT_COMPLETIONS", "https://example.test/compatible-mode/v1/chat/completions", "https://example.test/compatible-mode/v1/chat/completions"},
		{"CHAT_COMPLETIONS", "https://example.test/custom/exact/operation", "https://example.test/custom/exact/operation"},
		{"CHAT_COMPLETIONS", "https://example.test/v1/responses", ""},
		{"RESPONSES", "https://example.test/v1/chat/completions", ""},
		{"GEMINI", "https://example.test/v1/messages", ""},
		{"CHAT_COMPLETIONS", "https://user:secret@example.test/v1", ""},
		{"CHAT_COMPLETIONS", "https://example.test/v1?key=secret", ""},
		{"OTHER", "https://example.test/v1", ""},
	} {
		t.Run(test.protocol+test.input, func(t *testing.T) {
			got, err := ModelEndpoint(test.protocol, test.input)
			if test.want == "" {
				if err == nil {
					t.Fatal("invalid address accepted")
				}
			} else if err != nil || got != test.want {
				t.Fatalf("got %q, %v; want %q", got, err, test.want)
			}
		})
	}
}

func TestDiscoverModelsProtocolsPaginationAndHeaders(t *testing.T) {
	for _, protocol := range []string{"CHAT_COMPLETIONS", "RESPONSES", "ANTHROPIC", "GEMINI"} {
		t.Run(protocol, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != "GET" || r.ContentLength > 0 {
					t.Error("discovery generated content")
				}
				wantPath, authHeader, authValue := "/proxy/v1/models", "Authorization", "Bearer draft-key"
				if protocol == "GEMINI" {
					wantPath, authHeader, authValue = "/proxy/v1beta/models", "X-Goog-Api-Key", "draft-key"
				}
				if protocol == "ANTHROPIC" {
					authHeader, authValue = "X-Api-Key", "draft-key"
					if r.Header.Get("anthropic-version") != "2023-06-01" {
						t.Error("missing version")
					}
				}
				if r.URL.Path != wantPath || r.Header.Get(authHeader) != authValue {
					t.Error("wrong endpoint or auth", r.URL.Path)
				}
				w.Header().Set("Content-Type", "application/json")
				switch protocol {
				case "GEMINI":
					if calls == 1 {
						fmt.Fprint(w, `{"models":[{"name":"models/z-model","displayName":"Z","supportedGenerationMethods":["generateContent"]},{"name":"models/embedding","supportedGenerationMethods":["embedContent"]}],"nextPageToken":"next"}`)
					} else {
						if r.URL.Query().Get("pageToken") != "next" {
							t.Error("missing page token")
						}
						fmt.Fprint(w, `{"models":[{"name":"models/a-model","displayName":"A"},{"name":"models/z-model"}]}`)
					}
				case "ANTHROPIC":
					if calls == 1 {
						fmt.Fprint(w, `{"data":[{"id":"z-model","display_name":"Z"}],"has_more":true,"last_id":"z-model"}`)
					} else {
						if r.URL.Query().Get("after_id") != "z-model" {
							t.Error("missing after_id")
						}
						fmt.Fprint(w, `{"data":[{"id":"a-model","display_name":"A"},{"id":"z-model"}],"has_more":false}`)
					}
				default:
					fmt.Fprint(w, `{"data":[{"id":"z-model","name":"Z"},{"id":"a-model","name":"A"},{"id":"z-model"},{"id":""},{"id":"bad\nvalue"}]}`)
				}
			}))
			defer server.Close()
			endpoint := server.URL + "/proxy/v1"
			if protocol == "GEMINI" {
				endpoint = server.URL + "/proxy/v1beta"
			}
			key := "draft-key"
			out, err := (&Service{}).DiscoverModels(context.Background(), ModelDiscoveryInput{Provider: "custom", Protocol: protocol, Endpoint: endpoint, APIKey: &key, TimeoutSeconds: 5})
			if err != nil || out.Truncated || !reflect.DeepEqual(out.Items, []ModelOption{{ID: "a-model", Name: "A"}, {ID: "z-model", Name: "Z"}}) {
				t.Fatal(out, err)
			}
			wantCalls := 1
			if protocol == "GEMINI" || protocol == "ANTHROPIC" {
				wantCalls = 2
			}
			if calls != wantCalls {
				t.Fatal("unexpected request count", calls)
			}
		})
	}
}

func TestDiscoverModelsStoredCredentialIsolationAndNoWrites(t *testing.T) {
	s := fixture(t)
	calls, wantKey := 0, "Bearer stored-secret"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") != wantKey {
			t.Error("wrong credential")
		}
		fmt.Fprint(w, `{"data":[{"id":"selected-model"}]}`)
	}))
	defer server.Close()
	key := "stored-secret"
	input := ServiceInput{Name: "saved", Provider: "custom", Protocol: "CHAT_COMPLETIONS", Endpoint: server.URL + "/v1", Model: "selected-model", OutputMode: "JSON_SCHEMA", APIKey: &key, Enabled: true, TimeoutSeconds: 5, MaxResults: 5}
	saved, err := s.Save(context.Background(), "MODEL", "", input)
	if err != nil || saved.Endpoint != server.URL+"/v1/chat/completions" {
		t.Fatal(saved, err)
	}
	v := ModelDiscoveryInput{ServiceID: saved.ID, Revision: saved.Revision, Provider: "custom", Protocol: "CHAT_COMPLETIONS", Endpoint: server.URL + "/v1", TimeoutSeconds: 5}
	if _, err := s.DiscoverModels(context.Background(), v); err != nil {
		t.Fatal(err)
	}
	changed := v
	changed.Endpoint = server.URL + "/other/v1"
	if _, err := s.DiscoverModels(context.Background(), changed); err == nil {
		t.Fatal("credential moved to another endpoint")
	}
	changed = v
	changed.Protocol = "RESPONSES"
	if _, err := s.DiscoverModels(context.Background(), changed); err == nil {
		t.Fatal("credential moved to another protocol")
	}
	changed = v
	changed.Revision++
	if _, err := s.DiscoverModels(context.Background(), changed); err == nil {
		t.Fatal("stale discovery accepted")
	}
	if calls != 1 {
		t.Fatal("unsafe request reached upstream", calls)
	}
	v.ClearKey = true
	wantKey = ""
	if _, err := s.DiscoverModels(context.Background(), v); err != nil {
		t.Fatal(err)
	}
	after, err := s.List(context.Background(), "MODEL")
	if err != nil || len(after) != 1 || after[0] != saved {
		t.Fatal("discovery changed saved configuration", after, err)
	}
	// Equivalent base/full URLs do not require the user to re-enter a stored key.
	input.APIKey = nil
	input.Revision = saved.Revision
	if _, err := s.Save(context.Background(), "MODEL", saved.ID, input); err != nil {
		t.Fatal(err)
	}
}

func TestDiscoverModelsMalformedAndUnsupportedLists(t *testing.T) {
	for _, test := range []struct {
		name, body   string
		status, want int
	}{
		{"empty", `{"data":[]}`, 200, 0},
		{"unsupported", `{"message":"private-provider-detail"}`, 404, 404},
		{"auth", `{"message":"private-provider-detail"}`, 401, 401},
		{"malformed", `{"unexpected":[]}`, 200, 502},
		{"oversized", strings.Repeat(" ", 4*1024*1024+1), 200, 502},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(test.status); fmt.Fprint(w, test.body) }))
			defer server.Close()
			out, err := (&Service{}).DiscoverModels(context.Background(), ModelDiscoveryInput{Provider: "custom", Protocol: "CHAT_COMPLETIONS", Endpoint: server.URL + "/v1", TimeoutSeconds: 5})
			if test.want == 0 {
				if err != nil || out.Items == nil || len(out.Items) != 0 {
					t.Fatal(out, err)
				}
			} else {
				var f *Failure
				if !errors.As(err, &f) || f.Status != test.want || strings.Contains(err.Error(), "private-provider-detail") {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestGenerateLegacyBaseAddress(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Error("base was not expanded", r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["model"] != "local-model" {
			t.Error(body, err)
		}
		fmt.Fprint(w, `{"choices":[{"finish_reason":"stop","message":{"content":"OK"}}]}`)
	}))
	defer server.Close()
	text, err := (&Service{}).Generate(context.Background(), Snapshot{Protocol: "CHAT_COMPLETIONS", Endpoint: server.URL + "/v1", Model: "local-model", TimeoutSeconds: 5}, "test", "test", nil)
	if err != nil || text != "OK" {
		t.Fatal(text, err)
	}
}
