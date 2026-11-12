package metastore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/jmoiron/sqlx"
	"github.com/mynora-dot/singverse-go-backend/internal/metastandard"
	"github.com/mynora-dot/singverse-go-backend/internal/platform/store"
)

type Location struct {
	ID           string  `db:"id" json:"id"`
	MediaID      string  `db:"media_id" json:"mediaId"`
	SourceID     *string `db:"source_id" json:"sourceId"`
	RelativePath string  `db:"relative_path" json:"relativePath"`
	PathHash     string  `db:"path_hash" json:"-"`
	LocalKey     *string `db:"local_key" json:"-"`
	State        string  `db:"state" json:"state"`
	Observation  string  `db:"observation_json" json:"-"`
	UpdatedAt    string  `db:"updated_at" json:"updatedAt"`
}

func (s *Store) Locations(ctx context.Context, db store.DBTX, media string) ([]Location, error) {
	out := []Location{}
	e := sqlx.SelectContext(ctx, db, &out, s.q["location-read"], media)
	return out, e
}
func (s *Store) Location(ctx context.Context, db store.DBTX, media, source, path, localKey, state string, observation map[string]any) error {
	if !metastandard.ValidPath(path) {
		return &Problem{400, "媒体相对路径无效"}
	}
	rows, e := s.Locations(ctx, db, media)
	if e != nil {
		return e
	}
	// A scan updates ordinary attributes without discarding this receiver's
	// directory-verification receipt. Never carry it across a different location,
	// a failed verification, or a conflicting known size.
	if len(rows) == 1 && state == "PRESENT" && rows[0].State == state && rows[0].SourceID != nil && *rows[0].SourceID == source && rows[0].RelativePath == path && observation["verificationBinding"] == nil {
		var old map[string]any
		_ = json.Unmarshal([]byte(rows[0].Observation), &old)
		if encode(old["size"]) == "null" || encode(observation["size"]) == "null" || encode(old["size"]) == encode(observation["size"]) {
			next := map[string]any{}
			for key, value := range observation {
				next[key] = value
			}
			if encode(next["size"]) == "null" && old["size"] != nil {
				next["size"] = old["size"]
			}
			for _, key := range []string{"verificationBinding", "verificationSourceBinding", "verificationLevel", "sizeVerified"} {
				if value, ok := old[key]; ok {
					next[key] = value
				}
			}
			// A size-less scan does not strengthen an earlier PATH_ONLY observation.
			observation = next
		}
	}
	hash := metastandard.Digest([]byte(path))
	if len(rows) == 0 {
		_, e = db.ExecContext(ctx, s.q["location-insert"], NewID(), media, nullable(source), path, hash, nullable(localKey), state, encode(observation), timestamp())
	} else {
		_, e = db.ExecContext(ctx, s.q["location-update"], nullable(source), path, hash, nullable(localKey), state, encode(observation), timestamp(), media)
	}
	return e
}
func (s *Store) DetachSource(ctx context.Context, db store.DBTX, source string) error {
	_, e := db.ExecContext(ctx, s.q["location-detach"], timestamp(), source)
	return e
}
func (s *Store) PathMedia(ctx context.Context, db store.DBTX, source, path string) (string, error) {
	var id string
	e := db.QueryRowContext(ctx, s.q["location-path"], source, metastandard.Digest([]byte(path))).Scan(&id)
	return id, e
}
func (s *Store) LocalMedia(ctx context.Context, db store.DBTX, source, key string) ([]Location, error) {
	rows, e := store.Maps(ctx, db, s.q["location-local"], source, key)
	if e != nil {
		return nil, e
	}
	out := []Location{}
	for _, row := range rows {
		id, ok := row["media_id"].(string)
		if !ok {
			return nil, errors.New("invalid location identity")
		}
		obs, _ := row["observation_json"].(string)
		out = append(out, Location{MediaID: id, Observation: obs})
	}
	return out, nil
}
func (s *Store) Stats(ctx context.Context, db store.DBTX, source string) (map[string]int64, error) {
	rows, e := store.Maps(ctx, db, s.q["location-stats"], source)
	out := map[string]int64{}
	for _, row := range rows {
		state, _ := row["state"].(string)
		switch n := row["total"].(type) {
		case int64:
			out[state] = n
		case int:
			out[state] = int64(n)
		}
	}
	return out, e
}
func IsMissing(e error) bool { return errors.Is(e, sql.ErrNoRows) }
