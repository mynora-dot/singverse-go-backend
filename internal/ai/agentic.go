package ai

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cloudwego/eino-ext/components/model/agenticclaude"
	"github.com/cloudwego/eino-ext/components/model/agenticgemini"
	"github.com/cloudwego/eino-ext/components/model/agenticopenai"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"google.golang.org/genai"
)

// Agentic constructs an Eino model from the same encrypted settings used by
// classification. It performs no model request and never changes the settings.
// The SDK only handles protocol conversion; our transport retains ownership of
// endpoint interpretation, credentials, network restrictions and response limits.
func (s *Service) Agentic(ctx context.Context, c Snapshot) (model.AgenticModel, error) {
	if !c.Enabled {
		return nil, failure(503, "模型服务已停用，请在 AI 设置中启用")
	}
	if c.TimeoutSeconds < 1 || c.TimeoutSeconds > 180 || strings.TrimSpace(c.Model) == "" || hasControl(c.Model) {
		return nil, failure(400, "模型或超时配置无效")
	}
	endpoint, err := ModelEndpoint(c.Protocol, c.Endpoint)
	if err != nil {
		return nil, err
	}
	c.Endpoint = endpoint
	u, err := Endpoint(ctx, endpoint)
	if err != nil {
		return nil, err
	}
	key, err := s.Key(c)
	if err != nil {
		return nil, failure(503, "模型凭据无法解密，请在 AI 设置中重新保存")
	}
	timeout := time.Duration(c.TimeoutSeconds) * time.Second
	httpClient := &http.Client{
		Timeout:       timeout,
		Transport:     &agenticTransport{service: s, config: c, endpoint: u, key: key},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	// A nonempty SDK-only placeholder prevents implicit environment credential
	// discovery for keyless local servers. The transport always replaces auth.
	sdkKey := key
	if sdkKey == "" {
		sdkKey = "singverse-keyless-local-model"
	}
	origin := u.Scheme + "://" + u.Host + "/"
	var raw model.AgenticModel
	switch c.Protocol {
	case "CHAT_COMPLETIONS":
		raw, err = agenticopenai.NewChatModel(ctx, &agenticopenai.ChatConfig{
			APIKey: sdkKey, BaseURL: origin, Model: c.Model, HTTPClient: httpClient,
		})
	case "RESPONSES":
		zero, store := 0, false
		raw, err = agenticopenai.NewResponsesModel(ctx, &agenticopenai.ResponsesConfig{
			APIKey: sdkKey, BaseURL: origin, Model: c.Model, HTTPClient: httpClient,
			MaxRetries: &zero, Store: &store,
		})
	case "ANTHROPIC":
		raw, err = agenticclaude.New(ctx, &agenticclaude.Config{
			APIKey: sdkKey, BaseURL: origin, Model: c.Model, MaxTokens: 4096,
			HTTPClient: httpClient, RequestTimeout: timeout,
		})
	case "GEMINI":
		var gc *genai.Client
		gc, err = genai.NewClient(ctx, &genai.ClientConfig{
			APIKey: sdkKey, Backend: genai.BackendGeminiAPI, HTTPClient: httpClient,
			HTTPOptions: genai.HTTPOptions{BaseURL: origin, Timeout: &timeout},
		})
		if err == nil {
			raw, err = agenticgemini.New(ctx, &agenticgemini.Config{Client: gc, Model: c.Model})
		}
	default:
		return nil, failure(400, "不支持的模型协议")
	}
	if err != nil {
		return nil, failure(400, "模型工具适配器初始化失败，请检查协议与配置")
	}
	return &boundedAgenticModel{raw: raw}, nil
}

type agenticAttemptKey struct{}
type agenticAttempt struct {
	started atomic.Bool
	mu      sync.Mutex
	err     error
}

func (a *agenticAttempt) record(err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.err == nil {
		a.err = err
	}
}
func (a *agenticAttempt) result(ctx context.Context, err error) error {
	if err == nil || errors.Is(err, io.EOF) {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.err != nil {
		return a.err
	}
	if errors.Is(ctx.Err(), context.Canceled) || errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	var timeout net.Error
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) || errors.As(err, &timeout) && timeout.Timeout() {
		return failure(504, "模型请求超时，请稍后重试")
	}
	return failure(502, "模型工具响应无效，请检查协议及工具调用支持")
}

type boundedAgenticModel struct{ raw model.AgenticModel }

func (m *boundedAgenticModel) Generate(ctx context.Context, in []*schema.AgenticMessage, opts ...model.Option) (*schema.AgenticMessage, error) {
	a := &agenticAttempt{}
	ctx = context.WithValue(ctx, agenticAttemptKey{}, a)
	out, err := m.raw.Generate(ctx, in, opts...)
	return out, a.result(ctx, err)
}
func (m *boundedAgenticModel) Stream(ctx context.Context, in []*schema.AgenticMessage, opts ...model.Option) (*schema.StreamReader[*schema.AgenticMessage], error) {
	a := &agenticAttempt{}
	ctx = context.WithValue(ctx, agenticAttemptKey{}, a)
	r, err := m.raw.Stream(ctx, in, opts...)
	if err != nil {
		return nil, a.result(ctx, err)
	}
	return schema.StreamReaderWithConvert(r, func(msg *schema.AgenticMessage) (*schema.AgenticMessage, error) {
		return msg, nil
	}, schema.WithErrWrapper(func(err error) error { return a.result(ctx, err) })), nil
}

type agenticTransport struct {
	service  *Service
	config   Snapshot
	endpoint *url.URL
	key      string
}

func (t *agenticTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	a, ok := request.Context().Value(agenticAttemptKey{}).(*agenticAttempt)
	if !ok {
		return nil, failure(502, "模型请求缺少调用上下文")
	}
	if a.started.Swap(true) {
		return nil, failure(502, "模型请求失败；自动重试已禁用，请手动重试")
	}
	t.service.mu.Lock()
	cooling := time.Now().Before(t.service.cooldowns[t.config.ID])
	t.service.mu.Unlock()
	if cooling {
		err := failure(429, "模型仍在限流冷却，请稍后重试")
		a.record(err)
		return nil, err
	}
	// The existing settings contain full operation URLs except Gemini, whose
	// endpoint is a base path. Never append SDK routes to full configured URLs.
	target := *t.endpoint
	if request.Method != http.MethodPost || request.URL.Scheme != target.Scheme || request.URL.Host != target.Host {
		err := failure(400, "模型适配器请求超出已配置地址")
		a.record(err)
		return nil, err
	}
	if t.config.Protocol == "GEMINI" {
		op := "generateContent"
		if strings.HasSuffix(request.URL.Path, ":streamGenerateContent") {
			op = "streamGenerateContent"
		} else if !strings.HasSuffix(request.URL.Path, ":generateContent") {
			return nil, failure(400, "不支持的模型请求")
		}
		var err error
		targetURL := strings.TrimRight(t.config.Endpoint, "/") + "/models/" + url.PathEscape(t.config.Model) + ":" + op
		u, err := url.Parse(targetURL)
		if err != nil {
			return nil, failure(400, "模型地址无效")
		}
		target = *u
		if op == "streamGenerateContent" {
			target.RawQuery = "alt=sse"
		}
	}
	ctx := request.Context()
	if target.Scheme == "http" {
		ctx = context.WithValue(ctx, privateKey{}, true)
	}
	r := request.Clone(ctx)
	r.URL = &target
	r.Host = target.Host
	for _, h := range []string{"Authorization", "Api-Key", "X-Api-Key", "X-Goog-Api-Key", "OpenAI-Organization", "OpenAI-Project"} {
		r.Header.Del(h)
	}
	if t.key != "" {
		switch t.config.Protocol {
		case "ANTHROPIC":
			r.Header.Set("X-Api-Key", t.key)
		case "GEMINI":
			r.Header.Set("X-Goog-Api-Key", t.key)
		default:
			r.Header.Set("Authorization", "Bearer "+t.key)
		}
	}
	response, err := client.Transport.RoundTrip(r)
	if err != nil {
		var timeout net.Error
		if errors.Is(ctx.Err(), context.Canceled) {
			err = context.Canceled
		} else if errors.Is(err, context.DeadlineExceeded) || errors.As(err, &timeout) && timeout.Timeout() {
			err = failure(504, "模型请求超时，请稍后重试")
		} else {
			err = failure(502, "模型连接失败，请检查网络与服务地址")
		}
		a.record(err)
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		response.Body.Close()
		status := response.StatusCode
		if status == 429 {
			t.service.mu.Lock()
			if t.service.cooldowns == nil {
				t.service.cooldowns = map[string]time.Time{}
			}
			t.service.cooldowns[t.config.ID] = time.Now().Add(time.Minute)
			t.service.mu.Unlock()
			err = failure(429, "模型服务限流，至少冷却 60 秒后重试")
		} else if status == 401 || status == 403 {
			err = failure(status, "模型鉴权失败，请在 AI 设置中检查密钥与权限")
		} else {
			err = failure(502, "模型请求失败（HTTP "+format(status)+"），请检查协议与工具调用支持")
		}
		a.record(err)
		// SDK errors must never carry an upstream response body or echoed key.
		response.Header.Set("x-should-retry", "false")
		response.Header.Set("Content-Type", "application/json")
		response.Body = io.NopCloser(strings.NewReader(`{"error":{"message":"model request failed"}}`))
		return response, nil
	}
	response.Body = &agenticBody{ReadCloser: response.Body, remaining: 128000, attempt: a}
	return response, nil
}

type agenticBody struct {
	io.ReadCloser
	remaining int
	attempt   *agenticAttempt
}

func (b *agenticBody) Read(p []byte) (int, error) {
	if b.remaining < 0 {
		return 0, failure(502, "模型响应超过大小限制")
	}
	if len(p) > b.remaining+1 {
		p = p[:b.remaining+1]
	}
	n, err := b.ReadCloser.Read(p)
	b.remaining -= n
	if b.remaining < 0 {
		err = failure(502, "模型响应超过大小限制")
		b.attempt.record(err)
		return 0, err
	}
	return n, err
}

// CheckToolCalling tests native tool request/result conversion using a local
// echo receipt. It never registers or invokes a business tool.
func (s *Service) CheckToolCalling(ctx context.Context, c Snapshot) error {
	m, err := s.Agentic(ctx, c)
	if err != nil {
		return err
	}
	nonce := identifier()
	info := &schema.ToolInfo{Name: "singverse_connection_probe", Desc: "无业务副作用的连接测试，回显 token",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"token": {Type: schema.String, Required: true, Desc: "连接测试 token"},
		}),
	}
	input := []*schema.AgenticMessage{schema.UserAgenticMessage("请调用 singverse_connection_probe，token 为 " + nonce + "；收到工具回执后只回复工具返回的 token。")}
	first, err := collectAgentic(ctx, m, input, model.WithTools([]*schema.ToolInfo{info}), model.WithAgenticToolChoice(&schema.AgenticToolChoice{Type: schema.ToolChoiceForced}))
	if err != nil {
		return err
	}
	var call *schema.FunctionToolCall
	for _, b := range first.ContentBlocks {
		if b.FunctionToolCall != nil {
			if call != nil {
				return failure(502, "工具调用测试要求恰好一次测试调用")
			}
			call = b.FunctionToolCall
		}
	}
	var args struct {
		Token string `json:"token"`
	}
	if call == nil || call.Name != info.Name || json.Unmarshal([]byte(call.Arguments), &args) != nil || args.Token != nonce {
		return failure(502, "模型未返回有效的原生工具调用，请确认模型支持工具调用")
	}
	input = append(input, first, &schema.AgenticMessage{Role: schema.AgenticRoleTypeUser, ContentBlocks: []*schema.ContentBlock{{
		Type: schema.ContentBlockTypeFunctionToolResult,
		FunctionToolResult: &schema.FunctionToolResult{CallID: call.CallID, Name: call.Name, Content: []*schema.FunctionToolResultContentBlock{{
			Type: schema.FunctionToolResultContentBlockTypeText, Text: &schema.UserInputText{Text: nonce},
		}}},
	}}})
	last, err := collectAgentic(ctx, m, input, model.WithTools([]*schema.ToolInfo{info}), model.WithAgenticToolChoice(&schema.AgenticToolChoice{Type: schema.ToolChoiceForbidden}))
	if err != nil {
		return err
	}
	var text strings.Builder
	for _, b := range last.ContentBlocks {
		if b.FunctionToolCall != nil {
			return failure(502, "模型未遵守工具测试的结束条件")
		}
		if b.AssistantGenText != nil {
			text.WriteString(b.AssistantGenText.Text)
		}
	}
	if strings.TrimSpace(text.String()) != nonce {
		return failure(502, "模型未正确接收工具回执，工具往返测试失败")
	}
	return nil
}

func collectAgentic(ctx context.Context, m model.AgenticModel, input []*schema.AgenticMessage, opts ...model.Option) (*schema.AgenticMessage, error) {
	r, err := m.Stream(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	var chunks []*schema.AgenticMessage
	for {
		v, err := r.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		chunks = append(chunks, v)
	}
	if len(chunks) == 0 {
		return nil, failure(502, "模型返回空响应")
	}
	return schema.ConcatAgenticMessages(chunks)
}
