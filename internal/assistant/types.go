// Package assistant coordinates administrator conversations. Business operations
// are supplied by the application composition root, never HTTP calls or SQL tools.
package assistant

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/cloudwego/eino/schema"
)

// JSON preserves JSON objects across both SQLite TEXT and MySQL LONGTEXT scans.
type JSON []byte

func (j *JSON) Scan(src any) error {
	switch v := src.(type) {
	case string:
		*j = []byte(v)
	case []byte:
		*j = append((*j)[:0], v...)
	case nil:
		*j = nil
	default:
		return fmt.Errorf("invalid JSON storage type")
	}
	return nil
}
func (j JSON) MarshalJSON() ([]byte, error) {
	if len(j) == 0 {
		return []byte("null"), nil
	}
	return j, nil
}

type Definition struct {
	Name        string                                                               `json:"name"`
	Title       string                                                               `json:"title"`
	Group       string                                                               `json:"group"`
	Description string                                                               `json:"description"`
	Write       bool                                                                 `json:"write"`
	Page        string                                                               `json:"page"`
	Parameters  map[string]*schema.ParameterInfo                                     `json:"-"`
	InputSchema any                                                                  `json:"inputSchema"`
	Read        func(context.Context, json.RawMessage) (any, error)                  `json:"-"`
	Prepare     func(context.Context, json.RawMessage) (Preview, error)              `json:"-"`
	Execute     func(context.Context, json.RawMessage, string, Preview) (any, error) `json:"-"`
	// Reconcile is read-only. It returns evidence, never resubmits an operation.
	Reconcile func(context.Context, json.RawMessage, string) (any, error) `json:"-"`
}

type Preview struct {
	Summary  string `json:"summary"`
	Targets  any    `json:"targets"`
	Changes  any    `json:"changes"`
	Impact   string `json:"impact"`
	Revision string `json:"revision"`
	Page     string `json:"page"`
}
type Conversation struct {
	ID          string  `json:"id" db:"id"`
	Title       string  `json:"title" db:"title"`
	ActiveRunID *string `json:"activeRunId" db:"active_run_id"`
	CreatedAt   string  `json:"createdAt" db:"created_at"`
	UpdatedAt   string  `json:"updatedAt" db:"updated_at"`
}
type Run struct {
	ID             string `json:"id" db:"id"`
	ConversationID string `json:"conversationId" db:"conversation_id"`
	RequestID      string `json:"requestId" db:"request_id"`
	InputHash      string `json:"-" db:"input_hash"`
	Status         string `json:"status" db:"status"`
	ModelServiceID string `json:"modelServiceId" db:"model_service_id"`
	ModelRevision  int64  `json:"-" db:"model_revision"`
	NextSequence   int64  `json:"lastSequence" db:"next_sequence"`
	Error          string `json:"error" db:"error"`
	CreatedAt      string `json:"createdAt" db:"created_at"`
	UpdatedAt      string `json:"updatedAt" db:"updated_at"`
}
type Message struct {
	ID             string `json:"id" db:"id"`
	ConversationID string `json:"conversationId" db:"conversation_id"`
	RunID          string `json:"runId" db:"run_id"`
	Role           string `json:"role" db:"role"`
	Content        string `json:"content" db:"content"`
	CreatedAt      string `json:"createdAt" db:"created_at"`
}
type Event struct {
	RunID     string `json:"runId" db:"run_id"`
	Sequence  int64  `json:"sequence" db:"sequence"`
	Kind      string `json:"kind" db:"kind"`
	Data      JSON   `json:"data" db:"data"`
	CreatedAt string `json:"createdAt" db:"created_at"`
}
type Operation struct {
	ID         string  `json:"id" db:"id"`
	RunID      string  `json:"runId" db:"run_id"`
	ToolName   string  `json:"toolName" db:"tool_name"`
	State      string  `json:"state" db:"state"`
	Arguments  JSON    `json:"arguments" db:"arguments"`
	Preview    JSON    `json:"preview" db:"preview"`
	Revision   string  `json:"revision" db:"revision"`
	Result     JSON    `json:"result" db:"result"`
	Error      string  `json:"error" db:"error"`
	DecisionID *string `json:"decisionId" db:"decision_id"`
	Decision   *string `json:"-" db:"decision"`
	CreatedAt  string  `json:"createdAt" db:"created_at"`
	UpdatedAt  string  `json:"updatedAt" db:"updated_at"`
}
type Detail struct {
	Conversation Conversation `json:"conversation"`
	Messages     []Message    `json:"messages"`
	Runs         []Run        `json:"runs"`
	Operations   []Operation  `json:"operations"`
}
type Status struct {
	Ready        bool   `json:"ready"`
	Message      string `json:"message"`
	Model        string `json:"model"`
	ToolCount    int    `json:"toolCount"`
	SettingsPage string `json:"settingsPage"`
}
type failure struct {
	code    int
	message string
}

func (e *failure) Error() string          { return e.message }
func (e *failure) StatusCode() int        { return e.code }
func fail(code int, message string) error { return &failure{code, message} }
func encode(v any) string {
	b, e := json.Marshal(v)
	if e != nil {
		panic(fmt.Errorf("assistant JSON: %w", e))
	}
	return string(b)
}
