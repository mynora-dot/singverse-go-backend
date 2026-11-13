package app

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/mynora-dot/singverse-go-backend/internal/background"
)

func TestWebDAVScanAdmitsAudioScopeBeforeCompleting(t *testing.T) {
	a := application(t)
	c := newBusinessClient(t, a)
	c.call("POST", "/api/v1/auth/login", map[string]any{"username": "admin", "password": "admin"}, "", 200)
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "PROPFIND" {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(207)
		fmt.Fprint(w, `<d:multistatus xmlns:d="DAV:"><d:response><d:href>/dav/</d:href><d:propstat><d:prop><d:resourcetype><d:collection/></d:resourcetype></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response><d:response><d:href>/dav/A-Song.mkv</d:href><d:propstat><d:prop><d:resourcetype/><d:getcontentlength>50</d:getcontentlength></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response></d:multistatus>`)
	}))
	defer remote.Close()
	source := c.call("POST", "/api/v1/media-sources", map[string]any{"name": "WebDAV scope", "type": "WEBDAV", "location": remote.URL + "/dav/"}, "", 201)
	base := "/api/v1/media-sources/" + stringField(source, "id")
	scan := c.call("POST", base+"/scan-tasks", nil, "", 202)
	id := stringField(scan, "id")
	deadline := time.Now().Add(8 * time.Second)
	for {
		scan = c.call("GET", base+"/scan-tasks/"+id, nil, "", 200)
		if state := stringField(scan, "status"); state != "QUEUED" && state != "RUNNING" {
			if state != "SUCCEEDED" {
				t.Fatal(scan)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("scan did not complete", scan)
		}
		time.Sleep(20 * time.Millisecond)
	}
	taskPath := "/api/v1/background-tasks/" + background.ID("WEBDAV_SCAN", id)
	task := c.call("GET", taskPath, nil, "", 200)
	counts := object(task["childrenCounts"])
	if task["scopeSealed"] != true || counts["pendingDiscovery"] != float64(1) || counts["total"] != float64(0) || stringField(task, "stage") != "WAITING_RECOGNITION" {
		t.Fatal("published scan lost its audio admission", task)
	}
	// An unconfigured node is an explicit skip, and must close the batch.
	if err := a.tasks.SkipPendingAudio(t.Context(), "计算节点未配置或已停用"); err != nil {
		t.Fatal(err)
	}
	task = c.call("GET", taskPath, nil, "", 200)
	if stringField(task, "status") != "COMPLETED" || task["finishedAt"] == nil || object(task["childrenCounts"])["skipped"] != float64(1) {
		t.Fatal("scan remained queued after the audio decision", task)
	}
}
