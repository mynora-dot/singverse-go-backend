package assistant

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/mynora-dot/singverse-go-backend/internal/ai"
	"github.com/mynora-dot/singverse-go-backend/internal/platform/store"
)

//go:embed sql/*.sql
var statements embed.FS

type Service struct {
	db      *sqlx.DB
	q       map[string]string
	models  *ai.Service
	tools   []Definition
	byName  map[string]Definition
	ctx     context.Context
	cancel  context.CancelFunc
	mu      sync.Mutex
	running map[string]*activeRun
	wg      sync.WaitGroup
}

func New(db *sqlx.DB, models *ai.Service, definitions []Definition) (*Service, error) {
	q, e := store.ReadQueries(statements, "sql")
	if e != nil {
		return nil, e
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Service{db: db, q: q, models: models, tools: definitions, byName: map[string]Definition{}, ctx: ctx, cancel: cancel, running: map[string]*activeRun{}}
	for i := range s.tools {
		d := &s.tools[i]
		if d.Name == "" || s.byName[d.Name].Name != "" || (d.Write && (d.Prepare == nil || d.Execute == nil)) || (!d.Write && d.Read == nil) {
			cancel()
			return nil, fail(500, "助手工具目录配置无效")
		}
		info := toolInfo(*d)
		js, e := info.ParamsOneOf.ToJSONSchema()
		if e != nil {
			cancel()
			return nil, e
		}
		d.InputSchema = js
		s.byName[d.Name] = *d
	}
	e = store.WithinTx(ctx, db, func(tx store.DBTX) error {
		for _, name := range []string{"recoverOperations", "recoverUndecided", "recoverRuns"} {
			if _, e := tx.ExecContext(ctx, q[name], now()); e != nil {
				return e
			}
		}
		_, e := tx.ExecContext(ctx, q["recoverConversations"])
		return e
	})
	if e != nil {
		cancel()
		return nil, e
	}
	return s, nil
}

type activeRun struct {
	cancel context.CancelFunc
	done   chan struct{}
}

func now() string                        { return time.Now().UTC().Format("2006-01-02T15:04:05.000000000Z") }
func identifier() string                 { return uuid.NewString() }
func (s *Service) Close() error          { s.mu.Lock(); s.cancel(); s.mu.Unlock(); s.wg.Wait(); return nil }
func (s *Service) Catalog() []Definition { return s.tools }
func (s *Service) Conversations(ctx context.Context) ([]Conversation, error) {
	v := []Conversation{}
	e := s.db.SelectContext(ctx, &v, s.q["conversations"])
	return v, e
}
func (s *Service) Create(ctx context.Context, title string) (Conversation, error) {
	if len([]rune(title)) > 100 {
		return Conversation{}, fail(400, "会话名称最多 100 个字")
	}
	if title == "" {
		title = "新的对话"
	}
	v := Conversation{ID: identifier(), Title: title, CreatedAt: now(), UpdatedAt: now()}
	_, e := s.db.ExecContext(ctx, s.q["createConversation"], v.ID, v.Title, v.CreatedAt, v.UpdatedAt)
	return v, e
}
func missing(e error) error {
	if errors.Is(e, sql.ErrNoRows) {
		return fail(404, "会话或运行不存在")
	}
	return e
}
func (s *Service) conversation(ctx context.Context, id string) (Conversation, error) {
	var v Conversation
	e := s.db.GetContext(ctx, &v, s.q["conversation"], id)
	return v, missing(e)
}
func (s *Service) Run(ctx context.Context, id string) (Run, error) {
	var v Run
	e := s.db.GetContext(ctx, &v, s.q["run"], id)
	return v, missing(e)
}
func (s *Service) Detail(ctx context.Context, id string) (Detail, error) {
	v := Detail{Messages: []Message{}, Runs: []Run{}, Operations: []Operation{}}
	e := store.WithinTx(ctx, s.db, func(tx store.DBTX) error {
		if e := sqlx.GetContext(ctx, tx, &v.Conversation, s.q["conversation"], id); e != nil {
			return missing(e)
		}
		if e := sqlx.SelectContext(ctx, tx, &v.Messages, s.q["messages"], id); e != nil {
			return e
		}
		if e := sqlx.SelectContext(ctx, tx, &v.Runs, s.q["runs"], id); e != nil {
			return e
		}
		return sqlx.SelectContext(ctx, tx, &v.Operations, s.q["operations"], id)
	})
	return v, e
}
func (s *Service) Delete(ctx context.Context, id string) error {
	if _, e := s.conversation(ctx, id); e != nil {
		return e
	}
	r, e := s.db.ExecContext(ctx, s.q["deleteConversation"], id)
	return changed(r, e)
}
func changed(r sql.Result, e error) error {
	if e != nil {
		return e
	}
	n, e := r.RowsAffected()
	if e == nil && n != 1 {
		return fail(409, "状态已变化，请重新加载后操作")
	}
	return e
}
func (s *Service) event(ctx context.Context, id, kind string, data any) error {
	return store.WithinTx(ctx, s.db, func(tx store.DBTX) error {
		r, e := tx.ExecContext(ctx, s.q["nextSequence"], id)
		if e = changed(r, e); e != nil {
			return e
		}
		var seq int64
		if e = tx.QueryRowContext(ctx, s.q["sequence"], id).Scan(&seq); e != nil {
			return e
		}
		_, e = tx.ExecContext(ctx, s.q["createEvent"], id, seq, kind, encode(data), now())
		return e
	})
}
func (s *Service) Events(ctx context.Context, id string, after int64) ([]Event, error) {
	if after < 0 {
		return nil, fail(400, "事件游标无效")
	}
	v := []Event{}
	e := s.db.SelectContext(ctx, &v, s.q["events"], id, after)
	return v, e
}

type checkpointStore struct {
	s   *Service
	run string
}

func (c checkpointStore) Get(ctx context.Context, key string) ([]byte, bool, error) {
	var data []byte
	e := c.s.db.GetContext(ctx, &data, c.s.q["checkpoint"], key, c.run)
	if errors.Is(e, sql.ErrNoRows) {
		return nil, false, nil
	}
	return data, e == nil, e
}
func (c checkpointStore) Set(ctx context.Context, key string, data []byte) error {
	return store.WithinTx(ctx, c.s.db, func(tx store.DBTX) error {
		if _, e := tx.ExecContext(ctx, c.s.q["deleteCheckpoint"], key, c.run); e != nil {
			return e
		}
		_, e := tx.ExecContext(ctx, c.s.q["insertCheckpoint"], key, c.run, data)
		return e
	})
}
