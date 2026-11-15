package party

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jmoiron/sqlx"
	"github.com/mynora-dot/singverse-go-backend/internal/platform/store"
)

// Benchmarks the production handler with an already built, fixed-age index.
// There are no tags or filters. Mobile and TV both return the first 20 songs.
func BenchmarkWarmLibraryPage(b *testing.B) {
	gin.SetMode(gin.ReleaseMode)
	for _, count := range []int{10000, 100000} {
		b.Run(fmt.Sprintf("songs_%d", count), func(b *testing.B) {
			db, err := sqlx.Open("sqlite", ":memory:")
			if err != nil {
				b.Fatal(err)
			}
			defer db.Close()
			db.SetMaxOpenConns(1)
			_, err = db.Exec(`
                CREATE TABLE classification_tag(id TEXT, dimension TEXT, name TEXT, revision INTEGER, active INTEGER);
                CREATE TABLE classification_tag_cover(tag_id TEXT, version TEXT);
                CREATE TABLE classification_assignment(song_id TEXT, tag_id TEXT);
                CREATE TABLE media_song(id TEXT);
            `)
			if err != nil {
				b.Fatal(err)
			}
			queries, err := store.ReadQueries(queryFiles, "sql")
			if err != nil {
				b.Fatal(err)
			}
			fixed := time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC)
			index := &readIndex{built: fixed, entries: make([]readEntry, 0, count)}
			for i := 0; i < count; i++ {
				title := fmt.Sprintf("测试歌曲%06d", i)
				artists := []string{fmt.Sprintf("测试歌手%03d", i%100)}
				raw := document{
					"id": fmt.Sprintf("song-%06d", i), "sourceId": "source", "sourceName": "测试曲库",
					"relativePath": title + ".mp4", "fileSize": json.Number("10000000"), "modifiedAt": "2026-11-01T00:00:00Z",
					"scannedAt": "2026-11-01T00:00:00Z", "mediaVerification": "PRESENT",
					"result":         document{"title": title, "artists": artists, "extension": "mp4", "language": "MANDARIN", "versionType": "ORIGINAL", "songCode": fmt.Sprintf("%06d", i)},
					"metadata":       document{"stale": false, "fields": []string{"title", "artists", "album"}, "track": document{"title": title, "artists": artists, "album": "测试专辑"}},
					"classification": document{"dimensions": document{}},
				}
				index.entries = append(index.entries, readEntry{song: Projection(raw), raw: raw, search: strings.ToLower(title + " " + artists[0]), code: fmt.Sprintf("%06d", i)})
			}
			service := &Service{db: db, q: queries, index: index, now: func() time.Time { return fixed }}
			for _, mobile := range []bool{false, true} {
				name := "tv"
				if mobile {
					name = "mobile"
				}
				router := gin.New()
				router.GET("/library/songs", func(c *gin.Context) { service.searchSongs(c, mobile) })
				request := httptest.NewRequest("GET", "/library/songs?page=0&size=20", nil)
				b.Run(name, func(b *testing.B) {
					b.ReportAllocs()
					b.ResetTimer()
					for i := 0; i < b.N; i++ {
						recorder := httptest.NewRecorder()
						router.ServeHTTP(recorder, request)
						if recorder.Code != 200 {
							b.Fatalf("status %d: %s", recorder.Code, recorder.Body.String())
						}
					}
					b.StopTimer()
				})
			}
		})
	}
}
