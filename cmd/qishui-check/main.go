// qishui-check performs a real metadata-only search without opening a database.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/mynora-dot/singverse-go-backend/internal/platform/qishui"
)

func main() {
	query := flag.String("query", "传奇 郭沁", "song title and artist")
	requireResults := flag.Bool("require-results", false, "exit unsuccessfully if no song candidates are returned")
	flag.Parse()
	client, err := qishui.New(nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	defer client.Close()
	started := time.Now()
	root, err := client.Search(context.Background(), *query, "track")
	if err != nil {
		result := map[string]any{"ok": false, "error": err.Error(), "elapsedMs": time.Since(started).Milliseconds()}
		var failed *qishui.Failure
		if errors.As(err, &failed) {
			result["stage"], result["status"] = failed.Stage, failed.StatusCode()
		}
		json.NewEncoder(os.Stdout).Encode(result)
		os.Exit(1)
	}
	tracks := []map[string]any{}
	seen := make(map[string]bool)
	groups, _ := root["result_groups"].([]any)
	for _, raw := range groups {
		rows, _ := object(raw)["data"].([]any)
		for _, row := range rows {
			entity := object(object(row)["entity"])
			track := object(entity["track"])
			if track == nil {
				track = object(object(entity["track_wrapper"])["track"])
			}
			base := object(track["base_info"])
			related, display := object(track["related_info"]), object(track["display_info"])
			if base == nil || base["id"] == nil || base["name"] == nil {
				continue
			}
			id, title := strings.TrimSpace(fmt.Sprint(base["id"])), strings.TrimSpace(fmt.Sprint(base["name"]))
			if id == "" || title == "" || seen[id] {
				continue
			}
			seen[id] = true
			artists := []any{}
			people, _ := related["artist_links"].([]any)
			for _, person := range people {
				artists = append(artists, object(person)["name"])
			}
			album := object(related["album_link"])
			tracks = append(tracks, map[string]any{"id": id, "title": title, "artists": artists, "album": album["name"], "hasCover": display["cover_url"] != nil})
		}
	}
	json.NewEncoder(os.Stdout).Encode(map[string]any{"ok": true, "count": len(tracks), "tracks": tracks, "elapsedMs": time.Since(started).Milliseconds()})
	if *requireResults && len(tracks) == 0 {
		os.Exit(3)
	}
}

func object(value any) map[string]any { row, _ := value.(map[string]any); return row }
