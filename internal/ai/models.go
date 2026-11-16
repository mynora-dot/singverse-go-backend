package ai

import (
	"context"
	"errors"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/mynora-dot/singverse-go-backend/internal/platform/search"
)

type ModelDiscoveryInput struct {
	ServiceID      string  `json:"serviceId"`
	Revision       int64   `json:"revision"`
	Provider       string  `json:"provider"`
	Protocol       string  `json:"protocol"`
	Endpoint       string  `json:"endpoint"`
	APIKey         *string `json:"apiKey"`
	ClearKey       bool    `json:"clearKey"`
	TimeoutSeconds int     `json:"timeoutSeconds"`
}

type ModelOption struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type ModelDiscoveryResult struct {
	Items     []ModelOption `json:"items"`
	Truncated bool          `json:"truncated"`
}

// DiscoverModels uses the unsaved form. It never saves credentials, changes a
// service/binding or performs generation. Stored keys are confined to the same
// provider, protocol and canonical endpoint, just as they are during Save.
func (s *Service) DiscoverModels(ctx context.Context, v ModelDiscoveryInput) (ModelDiscoveryResult, error) {
	out := ModelDiscoveryResult{Items: []ModelOption{}}
	known := false
	for _, p := range Providers {
		known = known || p.ID == v.Provider && p.Kind == "MODEL"
	}
	if !known || v.TimeoutSeconds < 5 || v.TimeoutSeconds > 180 || v.APIKey != nil && (search.UTF16Length(*v.APIKey) > 4096 || hasControl(*v.APIKey)) {
		return out, failure(400, "厂商、密钥或超时配置无效")
	}
	endpoint, err := ModelEndpoint(v.Protocol, v.Endpoint)
	if err != nil {
		return out, err
	}
	listEndpoint, err := modelListEndpoint(v.Protocol, endpoint)
	if err != nil {
		return out, err
	}
	key := ""
	fresh := v.APIKey != nil && strings.TrimSpace(*v.APIKey) != ""
	if fresh && v.ClearKey {
		return out, failure(400, "请填写密钥或选择清除，不能同时设置")
	}
	if v.ServiceID != "" {
		old, err := s.required(ctx, s.db, v.ServiceID)
		if err != nil {
			return out, err
		}
		if old.Kind != "MODEL" || old.Revision != v.Revision {
			return out, failure(409, "配置已变化，请重新加载后获取模型列表")
		}
		oldEndpoint, _ := ModelEndpoint(old.Protocol, old.Endpoint)
		if old.Secret != nil && !fresh && !v.ClearKey {
			if old.Provider != v.Provider || old.Protocol != v.Protocol || oldEndpoint != endpoint {
				return out, failure(400, "更换厂商、协议或地址时请重填密钥或明确清除")
			}
			key, err = s.Key(Snapshot{EncryptedKey: old.Secret, CredentialPurpose: old.Purpose})
			if err != nil {
				return out, failure(503, "模型凭据无法解密，请重新填写密钥")
			}
		}
	}
	if fresh {
		key = *v.APIKey
	}
	headers := map[string]string{}
	switch v.Protocol {
	case "ANTHROPIC":
		headers["anthropic-version"] = "2023-06-01"
		if key != "" {
			headers["x-api-key"] = key
		}
	case "GEMINI":
		if key != "" {
			headers["x-goog-api-key"] = key
		}
	default:
		bearer(headers, key)
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(v.TimeoutSeconds)*time.Second)
	defer cancel()
	seen, cursors := map[string]bool{}, map[string]bool{}
	query := url.Values{}
	if v.Protocol == "GEMINI" {
		query.Set("pageSize", "1000")
	} else if v.Protocol == "ANTHROPIC" {
		query.Set("limit", "1000")
	}
	for page := 0; page < 10; page++ {
		root, err := sendLimited(ctx, listEndpoint, query, headers, nil, v.TimeoutSeconds, 4*1024*1024)
		if err != nil {
			var f *Failure
			if errors.As(err, &f) && (f.Status == 404 || f.Status == 405) {
				return out, failure(f.Status, "服务未提供模型列表，请检查地址或手动填写模型 ID")
			}
			return out, err
		}
		field := "data"
		if v.Protocol == "GEMINI" {
			field = "models"
		}
		items, ok := root[field].([]any)
		if !ok {
			return out, failure(502, "服务返回的模型列表格式无效，请手动填写模型 ID")
		}
		for _, item := range items {
			m := object(item)
			id, name := text(m["id"]), text(m["name"])
			if v.Protocol == "GEMINI" {
				methods, present := m["supportedGenerationMethods"]
				if present && !arrayContains(methods, "generateContent") {
					continue
				}
				id, name = strings.TrimPrefix(name, "models/"), text(m["displayName"])
			} else if v.Protocol == "ANTHROPIC" {
				name = text(m["display_name"])
			}
			id = strings.TrimSpace(id)
			if id == "" || search.UTF16Length(id) > 200 || hasControl(id) || seen[id] {
				continue
			}
			if len(out.Items) >= 2000 {
				out.Truncated = true
				break
			}
			if name == "" || search.UTF16Length(name) > 200 || hasControl(name) {
				name = id
			}
			seen[id] = true
			out.Items = append(out.Items, ModelOption{ID: id, Name: name})
		}
		next, param := "", ""
		if v.Protocol == "GEMINI" {
			next, param = text(root["nextPageToken"]), "pageToken"
		} else if v.Protocol == "ANTHROPIC" && root["has_more"] == true {
			next, param = text(root["last_id"]), "after_id"
		}
		if next == "" || out.Truncated {
			out.Truncated = out.Truncated || root["has_more"] == true
			break
		}
		if page == 9 || cursors[next] || len(next) > 2048 || hasControl(next) {
			out.Truncated = true
			break
		}
		cursors[next] = true
		query.Set(param, next)
	}
	sort.Slice(out.Items, func(i, j int) bool { return out.Items[i].ID < out.Items[j].ID })
	return out, nil
}

func arrayContains(value any, want string) bool {
	for _, item := range array(value) {
		if text(item) == want {
			return true
		}
	}
	return false
}
