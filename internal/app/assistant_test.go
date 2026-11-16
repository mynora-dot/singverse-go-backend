package app

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestAssistantRealSongQueryPlaylistConfirmationHTTP(t *testing.T) {
	a := application(t)
	c := newBusinessClient(t, a)
	c.call("POST", "/api/v1/auth/login", map[string]any{"username": "admin", "password": "admin"}, "", 200)
	dir, e := filepath.EvalSymlinks(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	for _, name := range []string{"十年", "好久不见", "爱情转移"} {
		if e = os.WriteFile(filepath.Join(dir, "陈奕迅 - "+name+".mp4"), []byte("isolated media"), 0600); e != nil {
			t.Fatal(e)
		}
	}
	source := c.call("POST", "/api/v1/media-sources", map[string]any{"name": "助手验收曲库", "type": "LOCAL", "location": dir}, "", 201)
	c.call("POST", "/api/v1/media-sources/"+stringField(source, "id")+"/scan", map[string]any{}, "", 200)
	songs := c.call("GET", "/api/v1/media-sources/"+stringField(source, "id")+"/songs", nil, "", 200)
	ids := []string{}
	for _, item := range songs["items"].([]any) {
		ids = append(ids, stringField(object(item), "id"))
	}
	if len(ids) != 3 {
		t.Fatal(songs)
	}
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "password_encrypted") {
			t.Error("credential leaked")
		}
		name := ""
		var arguments any
		switch n {
		case 1:
			name = "tool_search"
			arguments = map[string]any{"query": "select:songs_search,playlist_create"}
		case 2:
			name = "songs_search"
			arguments = map[string]any{"artist": "陈奕迅"}
		case 3:
			name = "playlist_create"
			arguments = map[string]any{"name": "周末客厅", "description": "陈奕迅国语歌", "songIds": ids}
		}
		if n == 3 && !strings.Contains(string(body), "十年") {
			t.Error("query result did not reach model")
		}
		delta := map[string]any{"role": "assistant", "content": "已创建周末客厅，包含 3 首歌曲。"}
		reason := "stop"
		if name != "" {
			args, _ := json.Marshal(arguments)
			delta = map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"index": 0, "id": fmt.Sprintf("agent-call-%d", n), "type": "function", "function": map[string]any{"name": name, "arguments": string(args)}}}}
			reason = "tool_calls"
		}
		b, _ := json.Marshal(map[string]any{"id": fmt.Sprintf("agent-chat-%d", n), "object": "chat.completion.chunk", "created": 1, "model": "fixture", "choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": reason}}})
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", b)
	}))
	defer upstream.Close()
	model := c.call("POST", "/api/v1/ai/models", map[string]any{"name": "Agent 验收模型", "provider": "custom", "protocol": "CHAT_COMPLETIONS", "endpoint": upstream.URL + "/chat/completions", "model": "fixture", "outputMode": "JSON_OBJECT", "enabled": true, "timeoutSeconds": 5, "maxResults": 5, "revision": 0}, "", 201)
	var featureRevision int64
	if e = a.db.Get(&featureRevision, "SELECT revision FROM ai_feature WHERE id=?", "assistant"); e != nil {
		t.Fatal(e)
	}
	c.call("PUT", "/api/v1/ai/features/assistant", map[string]any{"id": "assistant", "modelServiceId": model["id"], "searchServiceId": nil, "enabled": true, "intervalSeconds": 3, "revision": featureRevision, "displayMode": "PET", "petId": "music-sprite"}, "", 200)
	c.call("GET", "/api/v1/ai/assistant/status", nil, "", 200)
	conversation := c.call("POST", "/api/v1/ai/assistant/conversations", map[string]any{"title": "真实业务链路"}, "", 200)
	id := stringField(conversation, "id")
	run := c.call("POST", "/api/v1/ai/assistant/conversations/"+id+"/messages", map[string]any{"requestId": requestID(), "content": "查询陈奕迅，创建周末客厅歌单"}, "", 200)
	runID := stringField(run, "id")
	waitAssistantHTTP(t, c, runID, "WAITING")
	var before int
	if e = a.db.Get(&before, "SELECT COUNT(*) FROM playlist"); e != nil || before != 0 {
		t.Fatal("write before confirmation", before, e)
	}
	detail := c.call("GET", "/api/v1/ai/assistant/conversations/"+id, nil, "", 200)
	ops := detail["operations"].([]any)
	if len(ops) != 1 {
		t.Fatal(detail)
	}
	op := object(ops[0])
	opID := stringField(op, "id")
	preview := object(op["preview"])
	targets := object(preview["targets"])
	if len(targets["songs"].([]any)) != 3 {
		t.Fatal(preview)
	}
	// A valid Cookie does not waive CSRF. A forged browser origin also fails.
	for _, origin := range []string{"", "https://untrusted.example"} {
		req, _ := http.NewRequest("POST", c.origin+"/api/v1/ai/assistant/runs/"+runID+"/operations/"+opID+"/decision", strings.NewReader(`{"approve":true}`))
		req.Header.Set("Content-Type", "application/json")
		if origin != "" {
			req.Header.Set("Origin", origin)
			req.Header.Set("X-CSRF-Token", c.csrf)
		}
		response, e := c.client.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		response.Body.Close()
		if response.StatusCode != 403 {
			t.Fatal("confirmation bypassed CSRF/origin", response.StatusCode)
		}
	}
	decision := map[string]any{"requestId": requestID(), "revision": op["revision"], "approve": true}
	path := "/api/v1/ai/assistant/runs/" + runID + "/operations/" + opID + "/decision"
	c.call("POST", path, decision, "", 200)
	waitAssistantHTTP(t, c, runID, "COMPLETED")
	c.call("POST", path, decision, "", 200)
	var count int
	if e = a.db.Get(&count, "SELECT COUNT(*) FROM playlist"); e != nil || count != 1 {
		t.Fatal(count, e)
	}
	list := c.call("GET", "/api/v1/playlists", nil, "", 200)
	created := object(list["items"].([]any)[0])
	if created["name"] != "周末客厅" || created["count"] != float64(3) {
		t.Fatal(created)
	}
	receipt := c.call("GET", "/api/v1/ai/assistant/conversations/"+id, nil, "", 200)
	if object(receipt["operations"].([]any)[0])["state"] != "SUCCEEDED" {
		t.Fatal(receipt)
	}
	response, e := c.client.Get(c.origin + "/api/v1/ai/assistant/runs/" + runID + "/events?after=0")
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if !strings.Contains(string(raw), "approval_required") || !strings.Contains(string(raw), "operation_result") || !strings.Contains(string(raw), "周末客厅") {
		t.Fatal("SSE history missing", string(raw))
	}
	c.call("DELETE", "/api/v1/ai/assistant/conversations/"+id, nil, "", 204)
	if e = a.db.Get(&count, "SELECT COUNT(*) FROM playlist"); e != nil || count != 1 {
		t.Fatal("deleting history deleted business data", count, e)
	}
}
func waitAssistantHTTP(t *testing.T, c *businessClient, id, status string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		r := c.call("GET", "/api/v1/ai/assistant/runs/"+id, nil, "", 200)
		if r["status"] == status {
			return
		}
		if r["status"] == "FAILED" {
			t.Fatal(r)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("assistant did not reach", status)
}

func TestAssistantCatalogueMatchesCompleteFeatureInventory(t *testing.T) {
	a := application(t)
	c := newBusinessClient(t, a)
	c.call("POST", "/api/v1/auth/login", map[string]any{"username": "admin", "password": "admin"}, "", 200)
	response, e := c.client.Get(c.origin + "/api/v1/ai/assistant/tools")
	if e != nil {
		t.Fatal(e)
	}
	defer response.Body.Close()
	var tools []struct {
		Name, Title, Group, Page string
		Write                    bool
		InputSchema              map[string]any
	}
	if e = json.NewDecoder(response.Body).Decode(&tools); e != nil {
		t.Fatal(e)
	}
	raw, e := os.ReadFile("../../contracts/assistant-tool-coverage.json")
	if e != nil {
		t.Fatal(e)
	}
	var inventory struct {
		Tools    []struct{ Name, Mode, Page string }
		Features []struct {
			Feature, Page string
			Tools         []string
		}
	}
	if e = json.Unmarshal(raw, &inventory); e != nil {
		t.Fatal(e)
	}
	registered := map[string]bool{}
	for _, tool := range tools {
		if tool.Name == "" || registered[tool.Name] || tool.Title == "" || tool.Group == "" || tool.Page == "" || tool.InputSchema == nil {
			t.Fatal("incomplete tool declaration", tool)
		}
		registered[tool.Name] = true
		matched := false
		for _, entry := range inventory.Tools {
			if entry.Name == tool.Name {
				matched = true
				if tool.Write != (entry.Mode == "CONFIRM") || tool.Page != entry.Page {
					t.Fatal("catalogue policy drift", tool, entry)
				}
			}
		}
		if !matched {
			t.Fatal("tool missing from feature inventory", tool.Name)
		}
	}
	if len(tools) != len(inventory.Tools) || len(inventory.Features) < 25 {
		t.Fatal("incomplete feature coverage", len(tools), len(inventory.Tools))
	}
	used := map[string]bool{}
	for _, f := range inventory.Features {
		if len(f.Tools) == 0 {
			t.Fatal("feature has no tool or form", f.Feature)
		}
		for _, name := range f.Tools {
			if !registered[name] {
				t.Fatal("unregistered feature tool", f.Feature, name)
			}
			used[name] = true
		}
	}
	if len(used) != len(tools) {
		t.Fatal("catalogue entries lack a feature mapping")
	}
}
