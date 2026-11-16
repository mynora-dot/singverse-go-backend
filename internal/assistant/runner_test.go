package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/jmoiron/sqlx"
	"github.com/mynora-dot/singverse-go-backend/internal/ai"
	"github.com/mynora-dot/singverse-go-backend/internal/platform/config"
	"github.com/mynora-dot/singverse-go-backend/internal/platform/testutil"
)

type fixtureCipher struct{}

func (fixtureCipher) Encrypt(v, p string) (string, error) { return p + ":" + v, nil }
func (fixtureCipher) Decrypt(v, p string) (string, error) { return strings.TrimPrefix(v, p+":"), nil }

type runtimeFixture struct {
	s          *Service
	db         *sqlx.DB
	models     *ai.Service
	definition Definition
	calls      atomic.Int32
	revision   atomic.Int32
	requests   atomic.Int32
}

func assistantFixture(t *testing.T) *runtimeFixture {
	t.Helper()
	f := &runtimeFixture{db: testutil.Database(t)}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := f.requests.Add(1)
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"stream":true`) {
			t.Error("Agent did not use streaming")
		}
		var request struct {
			Tools []json.RawMessage `json:"tools"`
		}
		_ = json.Unmarshal(body, &request)
		if len(request.Tools) > 9 {
			t.Error("too many visible tools", len(request.Tools))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		name, args := "", ""
		switch n {
		case 1:
			name = "tool_search"
			args = `{"query":"select:rename"}`
		case 2:
			name = "rename"
			args = `{"name":"周末客厅"}`
		}
		delta := map[string]any{"role": "assistant", "content": "已按执行回执完成处理。"}
		reason := "stop"
		if name != "" {
			reason = "tool_calls"
			delta = map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"index": 0, "id": fmt.Sprintf("call-%d", n), "type": "function", "function": map[string]any{"name": name, "arguments": args}}}}
		}
		b, _ := json.Marshal(map[string]any{"id": fmt.Sprintf("chat-%d", n), "object": "chat.completion.chunk", "created": 1, "model": "test", "choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": reason}}})
		fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", b)
	}))
	t.Cleanup(upstream.Close)
	var e error
	f.models, e = ai.New(f.db, "sqlite", config.Config{}, fixtureCipher{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if e != nil {
		t.Fatal(e)
	}
	m, e := f.models.Save(context.Background(), "MODEL", "", ai.ServiceInput{Name: "工具测试", Provider: "custom", Protocol: "CHAT_COMPLETIONS", Endpoint: upstream.URL + "/v1/chat/completions", Model: "fixture", OutputMode: "JSON_OBJECT", Enabled: true, TimeoutSeconds: 5, MaxResults: 5})
	if e != nil {
		t.Fatal(e)
	}
	feature, e := f.models.Feature(context.Background(), "assistant")
	if e != nil {
		t.Fatal(e)
	}
	feature.Enabled = true
	feature.ModelServiceID = &m.ID
	if _, e = f.models.Bind(context.Background(), "assistant", feature); e != nil {
		t.Fatal(e)
	}
	f.definition = Definition{Name: "rename", Title: "更名", Group: "曲库", Page: "/library/playlists", Write: true, Parameters: map[string]*schema.ParameterInfo{"name": {Type: schema.String, Required: true}},
		Prepare: func(_ context.Context, args json.RawMessage) (Preview, error) {
			var a map[string]string
			_ = json.Unmarshal(args, &a)
			return Preview{Summary: "更名", Targets: map[string]any{"name": "原歌单", "revision": f.revision.Load()}, Changes: a, Revision: Hash([]any{a, f.revision.Load()})}, nil
		},
		Execute: func(context.Context, json.RawMessage, string, Preview) (any, error) {
			f.calls.Add(1)
			return map[string]any{"name": "周末客厅"}, nil
		},
	}
	f.s, e = New(f.db, f.models, []Definition{f.definition})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { f.s.Close() })
	return f
}
func awaitStatus(t *testing.T, s *Service, id string, want string) Run {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		r, e := s.Run(context.Background(), id)
		if e != nil {
			t.Fatal(e)
		}
		if r.Status == want {
			return r
		}
		if r.Status == "FAILED" {
			t.Fatalf("run failed: %s", r.Error)
		}
		time.Sleep(10 * time.Millisecond)
	}
	r, _ := s.Run(context.Background(), id)
	t.Fatalf("run %s: got %#v want %s", id, r, want)
	return r
}
func begin(t *testing.T, f *runtimeFixture) (Conversation, Run, Operation) {
	t.Helper()
	ctx := context.Background()
	c, e := f.s.Create(ctx, "更名测试")
	if e != nil {
		t.Fatal(e)
	}
	r, e := f.s.Submit(ctx, c.ID, identifier(), "更名歌单")
	if e != nil {
		t.Fatal(e)
	}
	awaitStatus(t, f.s, r.ID, "WAITING")
	d, e := f.s.Detail(ctx, c.ID)
	if e != nil {
		t.Fatal(e)
	}
	if len(d.Operations) != 1 {
		t.Fatal(d)
	}
	if f.calls.Load() != 0 {
		t.Fatal("write happened before confirmation")
	}
	return c, r, d.Operations[0]
}
func TestEinoConfirmationCheckpointAndDuplicateDecision(t *testing.T) {
	f := assistantFixture(t)
	c, r, op := begin(t, f)
	ctx := context.Background()
	duplicate, e := f.s.Submit(ctx, c.ID, r.RequestID, "更名歌单")
	if e != nil || duplicate.ID != r.ID {
		t.Fatal(duplicate, e)
	}
	if _, e = f.s.Submit(ctx, c.ID, identifier(), "另一个请求"); e == nil {
		t.Fatal("concurrent run was accepted")
	}
	if e = f.s.Delete(ctx, c.ID); e == nil {
		t.Fatal("deleted active conversation")
	}
	decision := identifier()
	if _, e = f.s.Decide(ctx, r.ID, op.ID, decision, op.Revision, true); e != nil {
		t.Fatal(e)
	}
	awaitStatus(t, f.s, r.ID, "COMPLETED")
	if _, e = f.s.Decide(ctx, r.ID, op.ID, decision, op.Revision, true); e != nil {
		t.Fatal(e)
	}
	if f.calls.Load() != 1 {
		t.Fatal("duplicate business execution", f.calls.Load())
	}
	events, e := f.s.Events(ctx, r.ID, 0)
	if e != nil || len(events) < 4 {
		t.Fatal(events, e)
	}
	last := events[len(events)-1].Sequence
	replay, e := f.s.Events(ctx, r.ID, last-1)
	if e != nil || len(replay) != 1 || replay[0].Sequence != last {
		t.Fatal(replay, e)
	}
	d, e := f.s.Detail(ctx, c.ID)
	if e != nil || d.Conversation.ActiveRunID != nil || d.Operations[0].State != "SUCCEEDED" {
		t.Fatal(d, e)
	}
	if e = f.s.Delete(ctx, c.ID); e != nil {
		t.Fatal(e)
	}
	var n int
	if e = f.db.Get(&n, "SELECT COUNT(*) FROM assistant_checkpoint WHERE run_id=?", r.ID); e != nil || n != 0 {
		t.Fatal("history delete did not cascade", n, e)
	}
}
func TestEinoPendingApprovalSurvivesRestart(t *testing.T) {
	f := assistantFixture(t)
	_, r, op := begin(t, f)
	if e := f.s.Close(); e != nil {
		t.Fatal(e)
	}
	var e error
	f.s, e = New(f.db, f.models, []Definition{f.definition})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.Decide(context.Background(), r.ID, op.ID, identifier(), op.Revision, true); e != nil {
		t.Fatal(e)
	}
	awaitStatus(t, f.s, r.ID, "COMPLETED")
	if f.calls.Load() != 1 {
		t.Fatal(f.calls.Load())
	}
}
func TestRejectAndChangedTargetsNeverWrite(t *testing.T) {
	for _, change := range []bool{false, true} {
		t.Run(fmt.Sprint(change), func(t *testing.T) {
			f := assistantFixture(t)
			_, r, op := begin(t, f)
			if change {
				f.revision.Add(1)
			}
			_, e := f.s.Decide(context.Background(), r.ID, op.ID, identifier(), op.Revision, change)
			if change {
				if e == nil {
					t.Fatal("stale approval accepted")
				}
				awaitStatus(t, f.s, r.ID, "STOPPED")
			} else {
				if e != nil {
					t.Fatal(e)
				}
				awaitStatus(t, f.s, r.ID, "COMPLETED")
			}
			if f.calls.Load() != 0 {
				t.Fatal("unconfirmed write")
			}
		})
	}
}
func TestMultiTabConfirmationRaceExecutesOnce(t *testing.T) {
	f := assistantFixture(t)
	_, r, op := begin(t, f)
	var successes atomic.Int32
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, e := f.s.Decide(context.Background(), r.ID, op.ID, identifier(), op.Revision, true)
			if e == nil {
				successes.Add(1)
			}
		}()
	}
	wg.Wait()
	awaitStatus(t, f.s, r.ID, "COMPLETED")
	if successes.Load() != 1 || f.calls.Load() != 1 {
		t.Fatal(successes.Load(), f.calls.Load())
	}
}
func TestUnknownWriteIsNotReplayedOnRecovery(t *testing.T) {
	f := assistantFixture(t)
	c, r, op := begin(t, f)
	f.s.Close()
	ctx := context.Background()
	if _, e := f.db.Exec("UPDATE assistant_operation SET state='EXECUTING' WHERE id=?", op.ID); e != nil {
		t.Fatal(e)
	}
	if _, e := f.db.Exec("UPDATE assistant_run SET status='RUNNING' WHERE id=?", r.ID); e != nil {
		t.Fatal(e)
	}
	var e error
	f.s, e = New(f.db, f.models, []Definition{f.definition})
	if e != nil {
		t.Fatal(e)
	}
	d, e := f.s.Detail(ctx, c.ID)
	if e != nil {
		t.Fatal(e)
	}
	if d.Runs[0].Status != "INTERRUPTED" || d.Operations[0].State != "UNCERTAIN" || d.Conversation.ActiveRunID != nil || f.calls.Load() != 0 {
		t.Fatal(d)
	}
}

func TestStopKeepsAcceptedWriteReceiptAndProtectsHistory(t *testing.T) {
	f := assistantFixture(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	f.s.tools[0].Execute = func(context.Context, json.RawMessage, string, Preview) (any, error) {
		close(entered)
		<-release
		f.calls.Add(1)
		return map[string]any{"saved": true}, nil
	}
	c, r, op := begin(t, f)
	if _, e := f.s.Decide(t.Context(), r.ID, op.ID, identifier(), op.Revision, true); e != nil {
		t.Fatal(e)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("write did not start")
	}
	if e := f.s.Stop(t.Context(), r.ID); e != nil {
		close(release)
		t.Fatal(e)
	}
	if e := f.s.Delete(t.Context(), c.ID); e == nil {
		close(release)
		t.Fatal("deleted an outstanding business receipt")
	}
	close(release)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		d, e := f.s.Detail(t.Context(), c.ID)
		if e != nil {
			t.Fatal(e)
		}
		if d.Operations[0].State == "SUCCEEDED" {
			if d.Runs[0].Status != "STOPPED" || f.calls.Load() != 1 {
				t.Fatal(d)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("accepted write did not save its receipt after stopping")
}

func TestDeletedTargetInvalidatesApproval(t *testing.T) {
	f := assistantFixture(t)
	_, r, op := begin(t, f)
	d := f.s.byName[op.ToolName]
	d.Prepare = func(context.Context, json.RawMessage) (Preview, error) {
		return Preview{}, fail(404, "对象已删除")
	}
	f.s.byName[op.ToolName] = d
	if _, e := f.s.Decide(t.Context(), r.ID, op.ID, identifier(), op.Revision, true); e == nil {
		t.Fatal("deleted target accepted")
	}
	saved, _ := f.s.Run(t.Context(), r.ID)
	if saved.Status != "STOPPED" || f.calls.Load() != 0 {
		t.Fatal(saved)
	}
}

func TestUncertainWriteCannotBeSubmittedAgain(t *testing.T) {
	f := assistantFixture(t)
	_, r, op := begin(t, f)
	if e := f.s.Stop(t.Context(), r.ID); e != nil {
		t.Fatal(e)
	}
	if _, e := f.db.Exec("UPDATE assistant_operation SET state='UNCERTAIN' WHERE id=?", op.ID); e != nil {
		t.Fatal(e)
	}
	tool := businessTool{s: f.s, run: r, definition: f.definition}
	result, e := tool.InvokableRun(t.Context(), string(op.Arguments))
	if e != nil || !strings.Contains(result, "不会重新提交") || f.calls.Load() != 0 {
		t.Fatal(result, e)
	}
}
