package playback

import (
	"context"
	"database/sql"
	"encoding/json"
	"github.com/gin-gonic/gin"
	"github.com/mynora-dot/singverse-go-backend/internal/platform/store"
	"net/http/httptest"
	"testing"
)

func TestPartyHistoryUsesActualDeduplicatedRecordsAndLiveAvailability(t *testing.T) {
	s, now := fixture(t)
	s.song = func(_ context.Context, _ store.DBTX, id string) (map[string]any, error) {
		if id == "removed" {
			return nil, sql.ErrNoRows
		}
		return map[string]any{"id": id, "title": "实时歌名", "artists": []string{"实时歌手"}, "available": true}, nil
	}
	for i, r := range []struct {
		song, client string
		played, age  int64
	}{
		{"song", "TV", 45000, 100000}, {"song", "MOBILE", 45000, 60000}, {"song", "ADMIN", 45000, 1000},
		{"unplayed", "TV", 0, 500}, {"removed", "TV", 45000, 80000}, {"old", "TV", 45000, 40 * 86400000},
	} {
		_, e := s.db.Exec(`INSERT INTO playback_history(id,song_id,title,artists_json,client_type,status,started_at,played_ms,duration_ms,ended_at,device_name,requested_by) VALUES(?,?,'历史歌名','["历史歌手"]',?,'COMPLETED',?,?,45000,?,'电视','亲友')`, string(rune('a'+i)), r.song, r.client, *now-r.age, r.played, *now-r.age+45000)
		if e != nil {
			t.Fatal(e)
		}
	}
	gin.SetMode(gin.TestMode)
	router := gin.New()
	s.RegisterParty(router, func(c *gin.Context) { c.Next() })
	for _, test := range []struct {
		path  string
		total int
		ids   []string
	}{
		{"recent", 3, []string{"song", "removed", "old"}},
		{"popular", 2, []string{"song", "removed"}},
		{"popular?period=all", 3, []string{"song", "removed", "old"}},
		{"popular?period=7d", 2, []string{"song", "removed"}},
	} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/party/library/"+test.path, nil))
		var out struct {
			Items         []map[string]any
			TotalElements int
		}
		if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &out) != nil {
			t.Fatal(rec.Code, rec.Body.String())
		}
		if out.TotalElements != test.total {
			t.Fatal(test.path, out)
		}
		for i, item := range out.Items {
			if item["id"] != test.ids[i] {
				t.Fatal(test.path, out)
			}
			if item["id"] == "removed" && (item["available"] != false || item["title"] != "历史歌名") {
				t.Fatal(item)
			}
		}
	}
}
