package ai

import (
	"net/url"
	"regexp"
	"strings"
)

var modelVersionPath = regexp.MustCompile(`(?i)/v[0-9]+(?:(?:alpha|beta)[0-9]*)?$`)

// ModelEndpoint accepts a versioned API base or an existing operation URL.
// Unrecognized custom operation paths remain exact for existing integrations.
func ModelEndpoint(protocol, value string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(value))
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || hasControl(value) || len(value) > 2048 || !contains(u.Scheme, "http", "https") {
		return "", failure(400, "模型地址无效，请填写 API 基础地址或完整接口地址")
	}
	p := strings.TrimRight(u.Path, "/")
	op := map[string]string{"CHAT_COMPLETIONS": "/chat/completions", "RESPONSES": "/responses", "ANTHROPIC": "/messages"}[protocol]
	if protocol != "GEMINI" && op == "" {
		return "", failure(400, "不支持的模型协议")
	}
	for _, suffix := range []string{"/chat/completions", "/responses", "/messages"} {
		if strings.HasSuffix(p, suffix) && suffix != op {
			return "", failure(400, "接口地址与所选协议不匹配，请调整协议或填写 API 基础地址")
		}
	}
	if p == "" {
		p = "/v1"
		if protocol == "GEMINI" {
			p = "/v1beta"
		}
	}
	if protocol != "GEMINI" && modelVersionPath.MatchString(p) {
		p += op
	}
	if p != u.Path {
		u.Path, u.RawPath = p, ""
	}
	return u.String(), nil
}

func modelListEndpoint(protocol, endpoint string) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", failure(400, "模型地址无效")
	}
	p := strings.TrimRight(u.Path, "/")
	if protocol != "GEMINI" {
		suffix := map[string]string{"CHAT_COMPLETIONS": "/chat/completions", "RESPONSES": "/responses", "ANTHROPIC": "/messages"}[protocol]
		if suffix == "" || !strings.HasSuffix(p, suffix) {
			return "", failure(400, "无法从此自定义接口推导模型列表，请填写带版本的基础地址或手动输入模型 ID")
		}
		p = strings.TrimSuffix(p, suffix)
	}
	u.Path, u.RawPath = p+"/models", ""
	return u.String(), nil
}
