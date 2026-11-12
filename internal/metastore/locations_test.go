package metastore

import (
	"encoding/json"
	"testing"

	"github.com/mynora-dot/singverse-go-backend/internal/platform/store"
)

func TestLocationScanRetainsOnlyCurrentVerificationReceipt(t *testing.T) {
	for _, tc := range []struct {
		name, path, state string
		size              any
		proof             bool
	}{
		{"same size", "歌曲.mp4", "PRESENT", int64(1000), true},
		{"typed nil size", "歌曲.mp4", "PRESENT", (*int64)(nil), true},
		{"size changed", "歌曲.mp4", "PRESENT", int64(1001), false},
		{"path changed", "其他.mp4", "PRESENT", int64(1000), false},
		{"missing", "歌曲.mp4", "MISSING", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, db := setup(t)
			ctx, media, source := t.Context(), NewID(), NewID()
			err := store.WithinTx(ctx, db, func(tx store.DBTX) error {
				if _, e := s.Create(ctx, tx, "SONG", media, ""); e != nil {
					return e
				}
				proof := map[string]any{"size": int64(1000), "verificationBinding": "root", "verificationSourceBinding": "account", "verificationLevel": "PATH_AND_SIZE", "sizeVerified": true}
				if e := s.Location(ctx, tx, media, source, "歌曲.mp4", "", "PRESENT", proof); e != nil {
					return e
				}
				return s.Location(ctx, tx, media, source, tc.path, "", tc.state, map[string]any{"size": tc.size, "etag": "new-etag"})
			})
			if err != nil {
				t.Fatal(err)
			}
			rows, err := s.Locations(ctx, db, media)
			if err != nil || len(rows) != 1 {
				t.Fatal(rows, err)
			}
			var observed map[string]any
			if err = json.Unmarshal([]byte(rows[0].Observation), &observed); err != nil {
				t.Fatal(err)
			}
			if (observed["verificationSourceBinding"] == "account") != tc.proof {
				t.Fatal("receipt survived different location or disappeared on same scan", observed)
			}
			if tc.proof && observed["size"] != float64(1000) {
				t.Fatal("last verified known size lost", observed)
			}
		})
	}
}
