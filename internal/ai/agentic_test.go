package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
)

// The fixtures exercise the real, pinned SDKs and Eino stream conversion. No
// live provider, business service, database or task executor participates.
func TestAgenticNativeToolRoundTrip(t *testing.T) {
	for _, protocol := range []string{"CHAT_COMPLETIONS", "RESPONSES", "ANTHROPIC", "GEMINI"} {
		t.Run(protocol, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := calls.Add(1)
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
				}
				wantPath := "/custom/exact/operation"
				if protocol == "GEMINI" {
					wantPath = "/custom/v1beta/models/test-model:streamGenerateContent"
					if r.URL.RawQuery != "alt=sse" {
						t.Error("Gemini stream query", r.URL.RawQuery)
					}
				} else if r.URL.RawQuery != "" {
					t.Error("unexpected query", r.URL.RawQuery)
				}
				if r.URL.Path != wantPath {
					t.Errorf("endpoint changed: got %q want %q", r.URL.Path, wantPath)
				}
				for _, h := range []string{"Authorization", "X-Api-Key", "X-Goog-Api-Key"} {
					if r.Header.Get(h) != "" {
						t.Errorf("keyless server received SDK/environment auth %s", h)
					}
				}
				nonce := regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f-]{27,}`).FindString(string(body))
				if nonce == "" || !strings.Contains(string(body), "singverse_connection_probe") {
					t.Error("missing tool schema or challenge")
				}
				if n == 2 {
					receiptKey := map[string]string{"CHAT_COMPLETIONS": `"role":"tool"`, "RESPONSES": `"type":"function_call_output"`, "ANTHROPIC": `"type":"tool_result"`, "GEMINI": `"functionResponse"`}[protocol]
					compact := strings.ReplaceAll(string(body), " ", "")
					if !strings.Contains(compact, receiptKey) {
						t.Errorf("missing native tool result %s: %s", receiptKey, compact)
					}
				}
				writeAgenticFixture(w, protocol, nonce, n == 1)
			}))
			defer server.Close()
			endpoint := server.URL + "/custom/exact/operation"
			if protocol == "GEMINI" {
				endpoint = server.URL + "/custom/v1beta"
			}
			// Ambient credentials must not reach a configured keyless local model.
			t.Setenv("OPENAI_API_KEY", "ambient-openai-secret")
			t.Setenv("ANTHROPIC_API_KEY", "ambient-anthropic-secret")
			t.Setenv("GEMINI_API_KEY", "ambient-gemini-secret")
			s := &Service{}
			err := s.CheckToolCalling(context.Background(), Snapshot{ID: protocol, Protocol: protocol, Endpoint: endpoint, Model: "test-model", Enabled: true, TimeoutSeconds: 5})
			if err != nil {
				t.Fatal(err)
			}
			if calls.Load() != 2 {
				t.Fatalf("got %d requests, want exactly two", calls.Load())
			}
		})
	}
}

func TestAgenticCredentialsAndUnsupportedTools(t *testing.T) {
	for _, protocol := range []string{"CHAT_COMPLETIONS", "RESPONSES", "ANTHROPIC", "GEMINI"} {
		t.Run(protocol, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				wantHeader, wantValue := "Authorization", "Bearer configured-secret"
				if protocol == "ANTHROPIC" {
					wantHeader, wantValue = "X-Api-Key", "configured-secret"
				} else if protocol == "GEMINI" {
					wantHeader, wantValue = "X-Goog-Api-Key", "configured-secret"
				}
				if r.Header.Get(wantHeader) != wantValue {
					t.Error("configured credential was not applied to the native protocol")
				}
				if r.URL.RawQuery != "" && r.URL.RawQuery != "alt=sse" {
					t.Error("credential leaked into endpoint query")
				}
				// A provider may accept the request yet answer with text instead of
				// the requested native tool. Report capability failure, never success.
				writeAgenticFixture(w, protocol, "tools are unavailable", false)
			}))
			defer server.Close()
			secret, err := (testCipher{}).Encrypt("configured-secret", "model-purpose")
			if err != nil {
				t.Fatal(err)
			}
			s := &Service{cipher: testCipher{}}
			err = s.CheckToolCalling(t.Context(), Snapshot{ID: protocol, Protocol: protocol, Endpoint: server.URL + "/configured", Model: "test-model", Enabled: true, TimeoutSeconds: 5, EncryptedKey: &secret, CredentialPurpose: "model-purpose"})
			if err == nil || !strings.Contains(err.Error(), "原生工具调用") || strings.Contains(err.Error(), "configured-secret") || calls.Load() != 1 {
				t.Fatal("wrong unsupported-tool feedback or unexpected retry", err, calls.Load())
			}
		})
	}
}

func writeAgenticFixture(w http.ResponseWriter, protocol, nonce string, toolCall bool) {
	w.Header().Set("Content-Type", "text/event-stream")
	args, _ := json.Marshal(map[string]string{"token": nonce})
	emit := func(event string, body any) {
		if event != "" {
			fmt.Fprintf(w, "event: %s\n", event)
		}
		b, _ := json.Marshal(body)
		fmt.Fprintf(w, "data: %s\n\n", b)
	}
	switch protocol {
	case "CHAT_COMPLETIONS":
		delta := map[string]any{"role": "assistant", "content": nonce}
		reason := "stop"
		if toolCall {
			delta = map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"index": 0, "id": "call-1", "type": "function", "function": map[string]any{"name": "singverse_connection_probe", "arguments": string(args)}}}}
			reason = "tool_calls"
		}
		emit("", map[string]any{"id": "chat-1", "object": "chat.completion.chunk", "created": 1, "model": "test-model", "choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": reason}}})
		fmt.Fprint(w, "data: [DONE]\n\n")
	case "RESPONSES":
		response := map[string]any{"id": "resp-1", "object": "response", "created_at": 1, "model": "test-model", "status": "in_progress", "output": []any{}}
		emit("response.created", map[string]any{"type": "response.created", "sequence_number": 0, "response": response})
		var item map[string]any
		if toolCall {
			item = map[string]any{"type": "function_call", "id": "fc-1", "call_id": "call-1", "name": "singverse_connection_probe", "arguments": ""}
			emit("response.output_item.added", map[string]any{"type": "response.output_item.added", "sequence_number": 1, "output_index": 0, "item": item})
			emit("response.function_call_arguments.delta", map[string]any{"type": "response.function_call_arguments.delta", "sequence_number": 2, "output_index": 0, "item_id": "fc-1", "delta": string(args)})
			item["arguments"] = string(args)
		} else {
			item = map[string]any{"type": "message", "id": "msg-1", "role": "assistant", "status": "in_progress", "content": []any{}}
			emit("response.output_item.added", map[string]any{"type": "response.output_item.added", "sequence_number": 1, "output_index": 0, "item": item})
			emit("response.content_part.added", map[string]any{"type": "response.content_part.added", "sequence_number": 2, "output_index": 0, "item_id": "msg-1", "content_index": 0, "part": map[string]any{"type": "output_text", "text": "", "annotations": []any{}}})
			emit("response.output_text.delta", map[string]any{"type": "response.output_text.delta", "sequence_number": 3, "output_index": 0, "item_id": "msg-1", "content_index": 0, "delta": nonce})
			item["content"] = []any{map[string]any{"type": "output_text", "text": nonce, "annotations": []any{}}}
			item["status"] = "completed"
		}
		emit("response.output_item.done", map[string]any{"type": "response.output_item.done", "sequence_number": 4, "output_index": 0, "item": item})
		response["status"] = "completed"
		response["output"] = []any{item}
		emit("response.completed", map[string]any{"type": "response.completed", "sequence_number": 5, "response": response})
	case "ANTHROPIC":
		emit("message_start", map[string]any{"type": "message_start", "message": map[string]any{"id": "msg-1", "type": "message", "role": "assistant", "model": "test-model", "content": []any{}, "usage": map[string]int{"input_tokens": 1, "output_tokens": 0}}})
		reason := "end_turn"
		if toolCall {
			reason = "tool_use"
			emit("content_block_start", map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "tool_use", "id": "call-1", "name": "singverse_connection_probe", "input": map[string]any{}}})
			emit("content_block_delta", map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "input_json_delta", "partial_json": string(args)}})
		} else {
			emit("content_block_start", map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "text", "text": ""}})
			emit("content_block_delta", map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": nonce}})
		}
		emit("content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
		emit("message_delta", map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": reason}, "usage": map[string]int{"output_tokens": 1}})
		emit("message_stop", map[string]any{"type": "message_stop"})
	case "GEMINI":
		part := map[string]any{"text": nonce}
		if toolCall {
			part = map[string]any{"functionCall": map[string]any{"id": "call-1", "name": "singverse_connection_probe", "args": map[string]string{"token": nonce}}}
		}
		emit("", map[string]any{"candidates": []any{map[string]any{"index": 0, "content": map[string]any{"role": "model", "parts": []any{part}}, "finishReason": "STOP"}}})
	}
}

func TestAgenticTransportFailureAndLimits(t *testing.T) {
	t.Run("cooldown and redaction", func(t *testing.T) {
		var calls atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			w.WriteHeader(429)
			fmt.Fprint(w, `{"error":{"message":"secret-key-and-upstream-content"}}`)
		}))
		defer server.Close()
		s := &Service{}
		m, err := s.Agentic(context.Background(), Snapshot{ID: "rate-limited", Protocol: "RESPONSES", Endpoint: server.URL + "/exact", Model: "model", Enabled: true, TimeoutSeconds: 5})
		if err != nil {
			t.Fatal(err)
		}
		for range 2 {
			_, err = m.Generate(context.Background(), []*schema.AgenticMessage{schema.UserAgenticMessage("test")})
			var f *Failure
			if !errors.As(err, &f) || f.Status != 429 || strings.Contains(err.Error(), "secret") {
				t.Fatal("unsafe or wrong error", err)
			}
		}
		if calls.Load() != 1 {
			t.Fatal("cooldown allowed a second request", calls.Load())
		}
	})
	t.Run("size and invalid responses", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"choices":[{"message":{"content":"`+strings.Repeat("x", 128001)+`"}}]}`)
		}))
		defer server.Close()
		m, err := (&Service{}).Agentic(context.Background(), Snapshot{Protocol: "CHAT_COMPLETIONS", Endpoint: server.URL, Model: "model", Enabled: true, TimeoutSeconds: 5})
		if err != nil {
			t.Fatal(err)
		}
		_, err = m.Generate(context.Background(), []*schema.AgenticMessage{schema.UserAgenticMessage("test")})
		if err == nil || !strings.Contains(err.Error(), "大小限制") {
			t.Fatal(err)
		}
	})
	t.Run("cancellation", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
		defer server.Close()
		m, err := (&Service{}).Agentic(context.Background(), Snapshot{Protocol: "RESPONSES", Endpoint: server.URL, Model: "model", Enabled: true, TimeoutSeconds: 5})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err = m.Generate(ctx, []*schema.AgenticMessage{schema.UserAgenticMessage("test")})
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	})
	t.Run("timeout", func(t *testing.T) {
		release := make(chan struct{})
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			io.Copy(io.Discard, r.Body)
			select {
			case <-r.Context().Done():
			case <-release:
			}
		}))
		defer server.Close()
		defer close(release)
		m, err := (&Service{}).Agentic(context.Background(), Snapshot{Protocol: "RESPONSES", Endpoint: server.URL, Model: "model", Enabled: true, TimeoutSeconds: 5})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		_, err = m.Generate(ctx, []*schema.AgenticMessage{schema.UserAgenticMessage("test")})
		var f *Failure
		if !errors.As(err, &f) || f.Status != 504 {
			t.Fatal(err)
		}
	})
	t.Run("disabled invalid endpoint and timeout", func(t *testing.T) {
		s := &Service{}
		for _, c := range []Snapshot{
			{Protocol: "RESPONSES", Endpoint: "http://1.1.1.1", Model: "model", Enabled: true, TimeoutSeconds: 5},
			{Protocol: "RESPONSES", Endpoint: "https://example.com", Model: "model", Enabled: false, TimeoutSeconds: 5},
			{Protocol: "RESPONSES", Endpoint: "https://example.com", Model: "model", Enabled: true, TimeoutSeconds: 181},
		} {
			if _, err := s.Agentic(context.Background(), c); err == nil {
				t.Fatal("invalid configuration accepted", c.Protocol)
			}
		}
	})
	t.Run("body can be read again after limit failure", func(t *testing.T) {
		b := &agenticBody{ReadCloser: io.NopCloser(strings.NewReader("abcdef")), remaining: 1, attempt: &agenticAttempt{}}
		for range 2 {
			if _, err := b.Read(make([]byte, 8)); err == nil {
				t.Fatal("expected size failure")
			}
		}
	})
}

func TestAgenticEveryProtocolStreamingFailure(t *testing.T) {
	for _, protocol := range []string{"CHAT_COMPLETIONS", "RESPONSES", "ANTHROPIC", "GEMINI"} {
		for _, kind := range []string{"timeout", "rate-limit"} {
			t.Run(protocol+"/"+kind, func(t *testing.T) {
				release := make(chan struct{})
				var calls atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					io.Copy(io.Discard, r.Body)
					if kind == "rate-limit" {
						w.WriteHeader(429)
						fmt.Fprint(w, `{"error":{"message":"never-expose-this-key"}}`)
						return
					}
					select {
					case <-release:
					case <-r.Context().Done():
					}
				}))
				defer server.Close()
				defer close(release)
				m, e := (&Service{}).Agentic(t.Context(), Snapshot{ID: protocol, Protocol: protocol, Endpoint: server.URL + "/configured", Model: "fixture", Enabled: true, TimeoutSeconds: 5})
				if e != nil {
					t.Fatal(e)
				}
				ctx, cancel := context.WithTimeout(t.Context(), 40*time.Millisecond)
				defer cancel()
				stream, e := m.Stream(ctx, []*schema.AgenticMessage{schema.UserAgenticMessage("fixture")})
				if e == nil {
					_, e = stream.Recv()
					stream.Close()
				}
				want := 504
				if kind == "rate-limit" {
					want = 429
				}
				var f *Failure
				if !errors.As(e, &f) || f.Status != want || strings.Contains(e.Error(), "never-expose") {
					t.Fatal(e)
				}
				if calls.Load() != 1 {
					t.Fatal("SDK retried a model request", calls.Load())
				}
			})
		}
	}
}
