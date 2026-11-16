package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestModelDiscoveryHTTPAuthCSRFAndUnsavedDraft(t *testing.T) {
	a := application(t)
	c := newBusinessClient(t, a)
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != "GET" || r.URL.Path != "/v1/models" || r.Header.Get("Authorization") != "Bearer draft-secret" {
			t.Error("unexpected catalog request", r.Method, r.URL.Path)
		}
		fmt.Fprint(w, `{"data":[{"id":"local-model","name":"Local"}]}`)
	}))
	defer upstream.Close()
	input := map[string]any{"provider": "custom", "protocol": "CHAT_COMPLETIONS", "endpoint": upstream.URL + "/v1", "apiKey": "draft-secret", "timeoutSeconds": 5}
	c.call("POST", "/api/v1/ai/models/discover", input, "", 401)
	if calls.Load() != 0 {
		t.Fatal("anonymous request reached upstream")
	}
	c.call("POST", "/api/v1/auth/login", map[string]any{"username": "admin", "password": "admin"}, "", 200)
	request, _ := http.NewRequest("POST", c.origin+"/api/v1/ai/models/discover", strings.NewReader(`{}`))
	request.Header.Set("Content-Type", "application/json")
	response, err := c.client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 403 || calls.Load() != 0 {
		t.Fatal("CSRF boundary failed", response.StatusCode)
	}
	out := c.call("POST", "/api/v1/ai/models/discover", input, "", 200)
	items, _ := out["items"].([]any)
	if len(items) != 1 || object(items[0])["id"] != "local-model" || calls.Load() != 1 {
		t.Fatal(out, calls.Load())
	}
	encoded, _ := json.Marshal(out)
	if strings.Contains(string(encoded), "draft-secret") {
		t.Fatal("credential in public response")
	}
	response, err = c.client.Get(c.origin + "/api/v1/ai/models")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var services []any
	if err := json.NewDecoder(response.Body).Decode(&services); err != nil || response.StatusCode != 200 || len(services) != 0 {
		t.Fatal("discovery saved a draft", services, err)
	}
}
