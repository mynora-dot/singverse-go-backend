package assistant

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"github.com/mynora-dot/singverse-go-backend/internal/ai"
)

func Hash(v any) string { sum := sha256.Sum256([]byte(encode(v))); return hex.EncodeToString(sum[:]) }
func toolInfo(d Definition) *schema.ToolInfo {
	parameters := d.Parameters
	if parameters == nil {
		parameters = map[string]*schema.ParameterInfo{}
	}
	return &schema.ToolInfo{Name: d.Name, Desc: d.Title + "。" + d.Description, ParamsOneOf: schema.NewParamsOneOfByParams(parameters)}
}

type businessTool struct {
	s          *Service
	run        Run
	definition Definition
}

func (t *businessTool) Info(context.Context) (*schema.ToolInfo, error) {
	return toolInfo(t.definition), nil
}

// Validate every supplied field on the server. The model's schema is only a hint.
func validateParameters(raw string, parameters map[string]*schema.ParameterInfo) (json.RawMessage, error) {
	if len(raw) > 32000 {
		return nil, fail(400, "工具参数超过限制")
	}
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	var values map[string]any
	if e := dec.Decode(&values); e != nil || values == nil {
		return nil, fail(400, "工具参数必须是 JSON 对象")
	}
	if e := dec.Decode(new(any)); e != io.EOF {
		return nil, fail(400, "工具参数必须是一个 JSON 对象")
	}
	for key, value := range values {
		p, ok := parameters[key]
		if !ok {
			return nil, fail(400, "工具不接受参数："+key)
		}
		if e := validateValue(value, p); e != nil {
			return nil, fail(400, "参数 "+key+"："+e.Error())
		}
	}
	for key, p := range parameters {
		if _, ok := values[key]; !ok && p.Required {
			return nil, fail(400, "缺少参数："+key)
		}
	}
	return json.RawMessage(encode(values)), nil
}
func validateValue(v any, p *schema.ParameterInfo) error {
	bad := errors.New("类型或长度无效")
	switch p.Type {
	case schema.String:
		s, ok := v.(string)
		if !ok || len([]rune(s)) > 8000 {
			return bad
		}
		if len(p.Enum) > 0 {
			for _, allowed := range p.Enum {
				if fmt.Sprint(allowed) == s {
					return nil
				}
			}
			return errors.New("值不在允许范围内")
		}
	case schema.Integer:
		n, ok := v.(json.Number)
		if !ok {
			return bad
		}
		if _, e := n.Int64(); e != nil {
			return bad
		}
	case schema.Number:
		if _, ok := v.(json.Number); !ok {
			return bad
		}
	case schema.Boolean:
		if _, ok := v.(bool); !ok {
			return bad
		}
	case schema.Array:
		items, ok := v.([]any)
		if !ok || len(items) > 500 {
			return bad
		}
		if p.ElemInfo != nil {
			for _, item := range items {
				if e := validateValue(item, p.ElemInfo); e != nil {
					return e
				}
			}
		}
	case schema.Object:
		obj, ok := v.(map[string]any)
		if !ok {
			return bad
		}
		for k, x := range obj {
			child, ok := p.SubParams[k]
			if !ok {
				return errors.New("未知嵌套参数")
			}
			if e := validateValue(x, child); e != nil {
				return e
			}
		}
		for k, child := range p.SubParams {
			if _, ok := obj[k]; child.Required && !ok {
				return errors.New("缺少嵌套参数：" + k)
			}
		}
	default:
		return bad
	}
	return nil
}
func publicError(e error) string {
	if e == nil {
		return ""
	}
	var modelFailure *ai.Failure
	if errors.As(e, &modelFailure) {
		return modelFailure.Detail
	}
	var known interface{ StatusCode() int }
	if errors.As(e, &known) && known.StatusCode() < 500 {
		return e.Error()
	}
	if errors.Is(e, context.Canceled) {
		return "对话已停止"
	}
	if errors.Is(e, context.DeadlineExceeded) {
		return "请求超时，请核对执行记录后重试"
	}
	return "服务调用失败，请检查对应业务页面或模型配置后重试"
}
func (t *businessTool) InvokableRun(ctx context.Context, raw string, _ ...tool.Option) (string, error) {
	args, e := validateParameters(raw, t.definition.Parameters)
	if e != nil {
		return encode(map[string]any{"error": publicError(e), "page": t.definition.Page}), nil
	}
	if !t.definition.Write {
		call := identifier()
		if e = t.s.event(ctx, t.run.ID, "tool_started", map[string]any{"id": call, "name": t.definition.Name, "title": t.definition.Title, "page": t.definition.Page}); e != nil {
			return "", e
		}
		result, err := t.definition.Read(ctx, args)
		data := map[string]any{"id": call, "name": t.definition.Name, "title": t.definition.Title, "page": t.definition.Page, "result": result, "error": publicError(err)}
		if e = t.s.event(ctx, t.run.ID, "tool_result", data); e != nil {
			return "", e
		}
		return boundedResult(data), nil
	}
	var uncertain Operation
	check := t.s.db.GetContext(ctx, &uncertain, t.s.q["uncertainOperation"], t.run.ConversationID, t.definition.Name, string(args))
	if check == nil {
		return boundedResult(map[string]any{"state": uncertain.State, "receiptId": uncertain.ID, "error": "相同操作仍有未核对的执行回执。请先核对业务结果，不会重新提交", "page": t.definition.Page}), nil
	}
	if !errors.Is(check, sql.ErrNoRows) {
		return "", check
	}
	_, hasState, opID := tool.GetInterruptState[string](ctx)
	if !hasState {
		p, e := t.definition.Prepare(ctx, args)
		if e != nil {
			return encode(map[string]any{"error": publicError(e), "page": t.definition.Page}), nil
		}
		if p.Revision == "" {
			p.Revision = Hash([]any{args, p.Targets, p.Changes})
		}
		p.Page = t.definition.Page
		opID = identifier()
		at := now()
		_, e = t.s.db.ExecContext(ctx, t.s.q["createOperation"], opID, t.run.ID, t.definition.Name, "PENDING", string(args), encode(p), p.Revision, "null", "", at, at)
		if e != nil {
			return "", e
		}
		if e = t.s.event(ctx, t.run.ID, "approval_required", map[string]any{"id": opID, "name": t.definition.Name, "preview": p}); e != nil {
			return "", e
		}
		return "", tool.StatefulInterrupt(ctx, "等待管理员确认", opID)
	}
	var op Operation
	if e = t.s.db.GetContext(ctx, &op, t.s.q["operation"], opID, t.run.ID); e != nil {
		return "", e
	}
	switch op.State {
	case "PENDING":
		return "", tool.StatefulInterrupt(ctx, "等待管理员确认", opID)
	case "REJECTED":
		return encode(map[string]any{"rejected": true, "message": "管理员拒绝了这项操作。不要换用其他工具执行同一操作。"}), nil
	case "SUCCEEDED":
		return boundedResult(map[string]any{"receiptId": op.ID, "result": op.Result, "page": t.definition.Page}), nil
	case "FAILED", "STALE", "UNCERTAIN", "EXECUTING":
		return encode(map[string]any{"state": op.State, "error": op.Error, "receiptId": op.ID, "page": t.definition.Page}), nil
	case "APPROVED":
	default:
		return "", fail(409, "操作状态无效")
	}
	// The checkpoint contains only an operation ID. Frozen server-side arguments
	// are authoritative; never trust arguments reconstructed by the model.
	p, e := t.definition.Prepare(ctx, json.RawMessage(op.Arguments))
	if e != nil || p.Revision != op.Revision {
		t.s.setOperation(context.WithoutCancel(ctx), op, "STALE", nil, "对象或内容已变化，请重新发起操作并确认")
		return encode(map[string]any{"state": "STALE", "error": "对象或内容已变化，需要重新确认", "page": t.definition.Page}), nil
	}
	if e = t.s.setOperation(ctx, op, "EXECUTING", nil, ""); e != nil {
		return "", e
	}
	op.State = "EXECUTING"
	// Stopping a conversation must not cancel a business job already accepted.
	executeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 90*time.Second)
	defer cancel()
	var frozen Preview
	if e = json.Unmarshal(op.Preview, &frozen); e != nil {
		return "", e
	}
	result, err := executeBusiness(t.definition, executeCtx, json.RawMessage(op.Arguments), op.ID, frozen)
	state, message := "SUCCEEDED", ""
	if err != nil {
		state = "UNCERTAIN"
		var known interface{ StatusCode() int }
		if errors.As(err, &known) && known.StatusCode() < 500 {
			state = "FAILED"
		}
		message = publicError(err)
	}
	receiptCtx, saveCancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer saveCancel()
	if e = t.s.setOperation(receiptCtx, op, state, result, message); e != nil {
		return "", e
	}
	receipt := map[string]any{"receiptId": op.ID, "state": state, "result": result, "error": message, "page": t.definition.Page}
	if e = t.s.event(receiptCtx, t.run.ID, "operation_result", receipt); e != nil {
		return "", e
	}
	return boundedResult(receipt), nil
}
func executeBusiness(d Definition, ctx context.Context, args json.RawMessage, id string, p Preview) (result any, err error) {
	defer func() {
		if recover() != nil {
			err = errors.New("business operation interrupted")
		}
	}()
	return d.Execute(ctx, args, id, p)
}
func boundedResult(v any) string {
	data := encode(v)
	if len(data) <= 24000 {
		return data
	}
	return encode(map[string]any{"summary": "结果已保存到运行记录，请缩小查询或在业务页面查看完整内容。", "truncated": true})
}
func (s *Service) setOperation(ctx context.Context, op Operation, state string, result any, message string) error {
	r, e := s.db.ExecContext(ctx, s.q["setOperation"], state, encode(result), message, now(), op.ID, op.RunID, op.State)
	return changed(r, e)
}
