package playback

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/mynora-dot/singverse-go-backend/internal/platform/store"
)

func TestPlaybackHistoryHTTP(t *testing.T) {
	s, now := fixture(t)
	day := int64(86400000)
	for _, record := range []struct {
		id, song, title, artists, client, status string
		started, played                          int64
		duration, ended                          any
	}{
		{"other", "other-song", "其他歌曲", `["其他歌手"]`, "TV", "COMPLETED", *now - 60000, 45000, 45000, *now - 15000},
		{"recent-a", "song", "中国", `["歌手"]`, "MOBILE", "PLAYING", *now - 120000, 30000, nil, nil},
		{"recent-b", "song", "中国", `["歌手"]`, "ADMIN", "STOPPED", *now - 120000, 45000, 60000, *now - 75000},
		{"ten-days", "song", "中国", `["歌手"]`, "TV", "COMPLETED", *now - 10*day, 10000, 10000, *now - 10*day + 10000},
		{"forty-days", "song", "中国", `["歌手"]`, "TV", "SKIPPED", *now - 40*day, 5000, 60000, *now - 40*day + 5000},
		{"future", "song", "中国", `["歌手"]`, "TV", "PLAYING", *now + 60000, 0, nil, nil},
	} {
		_, err := s.db.Exec(`INSERT INTO playback_history
			(id,song_id,title,artists_json,client_type,status,started_at,played_ms,duration_ms,ended_at,device_name,requested_by)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, record.id, record.song, record.title, record.artists, record.client, record.status,
			record.started, record.played, record.duration, record.ended, "测试设备", "测试用户")
		if err != nil {
			t.Fatal(err)
		}
	}

	gin.SetMode(gin.TestMode)
	router := gin.New()
	s.Register(router.Group("/api/v1"))
	for _, test := range []struct {
		name, query string
		ids         []string
		total       int
		page, size  int
	}{
		{"default period", "", []string{"other", "recent-b", "recent-a", "ten-days"}, 4, 0, 20},
		{"all records", "?period=all", []string{"other", "recent-b", "recent-a", "ten-days", "forty-days"}, 5, 0, 20},
		{"single song all time", "?songId=song&period=all", []string{"recent-b", "recent-a", "ten-days", "forty-days"}, 4, 0, 20},
		{"single song seven days", "?songId=song&period=7d", []string{"recent-b", "recent-a"}, 2, 0, 20},
		{"single song thirty days", "?songId=song&period=30d", []string{"recent-b", "recent-a", "ten-days"}, 3, 0, 20},
		{"title search", "?period=all&q=中国", []string{"recent-b", "recent-a", "ten-days", "forty-days"}, 4, 0, 20},
		{"artist search", "?period=all&q=其他歌手", []string{"other"}, 1, 0, 20},
		{"first page", "?songId=song&period=all&page=0&size=1", []string{"recent-b"}, 4, 0, 1},
		{"second page", "?songId=song&period=all&page=1&size=1", []string{"recent-a"}, 4, 1, 1},
		{"old record page", "?songId=song&period=all&page=3&size=1", []string{"forty-days"}, 4, 3, 1},
		{"page after last", "?songId=song&period=all&page=4&size=1", []string{}, 4, 4, 1},
		{"song without history", "?songId=missing&period=all", []string{}, 0, 0, 20},
		{"search and song intersect", "?songId=song&period=all&q=其他歌手", []string{}, 0, 0, 20},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/playback-history"+test.query, nil))
			if response.Code != http.StatusOK {
				t.Fatalf("history returned %d: %s", response.Code, response.Body.String())
			}
			var result struct {
				Items []struct {
					ID, SongID, Title, ClientType, DeviceName, RequestedBy, StartedAt, Status string
					Artists                                                                   []string
					PlayedMs                                                                  int64
					DurationMs                                                                *int64
					EndedAt                                                                   *string
					Effective                                                                 bool
				}
				TotalElements, Page, Size int
			}
			if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			ids := make([]string, 0, len(result.Items))
			for _, item := range result.Items {
				ids = append(ids, item.ID)
				if item.DeviceName != "测试设备" || item.RequestedBy != "测试用户" || len(item.Artists) != 1 {
					t.Fatalf("record identity was lost: %#v", item)
				}
				if item.ID == "recent-a" && (item.SongID != "song" || item.Title != "中国" || item.Artists[0] != "歌手" || !item.Effective || item.PlayedMs != 30000 || item.DurationMs != nil || item.EndedAt != nil || item.Status != "PLAYING" || item.ClientType != "MOBILE" || item.StartedAt != time.UnixMilli(*now-120000).UTC().Format(time.RFC3339Nano)) {
					t.Fatalf("playing record projection is wrong: %#v", item)
				}
				if item.ID == "recent-b" && item.Effective {
					t.Fatal("admin preview was counted as effective")
				}
				if item.ID == "ten-days" && (!item.Effective || item.DurationMs == nil || *item.DurationMs != 10000 || item.EndedAt == nil) {
					t.Fatalf("completed short song projection is wrong: %#v", item)
				}
			}
			if result.Items == nil || !reflect.DeepEqual(ids, test.ids) || result.TotalElements != test.total || result.Page != test.page || result.Size != test.size {
				t.Fatalf("got ids=%v total=%d page=%d size=%d; want ids=%v total=%d page=%d size=%d", ids, result.TotalElements, result.Page, result.Size, test.ids, test.total, test.page, test.size)
			}
		})
	}
}

func TestPlaybackHistoryQueryBindsForBothDialects(t *testing.T) {
	queries, err := store.ReadQueries(files, "sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, dialect := range []string{"sqlite", "mysql"} {
		t.Run(dialect, func(t *testing.T) {
			s := &Service{queries: queries, dialect: dialect}
			// A list request provides filters and pagination, never a history row ID.
			_, _, err := s.statement("history", map[string]any{"songId": "song", "from": int64(0), "until": int64(2000000000000), "pattern": "", "size": 20, "offset": 0})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}
