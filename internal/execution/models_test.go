package execution

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRKNNModelDownloadForwardsExplicitSourceToAuthenticatedNode(t *testing.T) {
	source := `{"url":"https://models.example.test/rk.zip","sha256":"` + strings.Repeat("a", 64) + `"}`
	var received bool
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/v1/audio-roles/models/download" || r.URL.Query().Get("model") != "uvr-mdxnet-9482-rknn" || r.Header.Get("Authorization") != "Bearer "+testToken {
			t.Errorf("unexpected node request: %s", r.URL)
			w.WriteHeader(400)
			return
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["url"] != "https://models.example.test/rk.zip" || body["sha256"] != strings.Repeat("a", 64) {
			t.Errorf("download source was lost: %v, %v", body, err)
		}
		received = true
		w.WriteHeader(202)
		io.WriteString(w, `{"state":"DOWNLOADING","bytes":0}`)
	}))
	defer node.Close()
	f := executionFixture(t, node.URL)
	request := httptest.NewRequest("POST", "/api/v1/audio-processing/model/download?model=uvr-mdxnet-9482-rknn", strings.NewReader(source))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	f.router.ServeHTTP(response, request)
	if response.Code != 200 || !received || !strings.Contains(response.Body.String(), "DOWNLOADING") {
		t.Fatal(response.Code, response.Body.String(), received)
	}
}

func TestRKNNModelImportStreamsBundleAndPreservesValidationDetail(t *testing.T) {
	payload := []byte("controlled ZIP payload")
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.URL.Path != "/internal/v1/audio-roles/models/import" || r.URL.Query().Get("model") != "uvr-mdxnet-9482-rknn" || !bytes.Equal(body, payload) {
			t.Errorf("bundle was not forwarded: %s, %q", r.URL, body)
		}
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(422)
		io.WriteString(w, `{"title":"RKNN 模型芯片不匹配，当前节点为 rk3566"}`)
	}))
	defer node.Close()
	f := executionFixture(t, node.URL)
	request := httptest.NewRequest("POST", "/api/v1/audio-processing/model/import?model=uvr-mdxnet-9482-rknn", bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/octet-stream")
	response := httptest.NewRecorder()
	f.router.ServeHTTP(response, request)
	if response.Code != 422 || !strings.Contains(response.Body.String(), "rk3566") {
		t.Fatal(response.Code, response.Body.String())
	}
	request = httptest.NewRequest("POST", "/api/v1/audio-processing/model/import?model=uvr-mdxnet-9482-rknn", bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/octet-stream")
	request.ContentLength = (512 << 20) + 1
	response = httptest.NewRecorder()
	f.router.ServeHTTP(response, request)
	if response.Code != 413 {
		t.Fatal("oversized model accepted", response.Code, response.Body.String())
	}
}
