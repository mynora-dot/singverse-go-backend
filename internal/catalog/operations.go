package catalog

import (
	"context"

	"github.com/mynora-dot/singverse-go-backend/internal/platform/store"
)

// These application methods share the same validation, transactions, CAS and
// publication hooks as the HTTP routes. They do not depend on Gin.
func (s *Service) Playlists(ctx context.Context, q string, page, size int) (any, error) {
	return s.playlistList(ctx, q, "UPDATED_DESC", page, size)
}
func (s *Service) Playlist(ctx context.Context, id string) (any, error) {
	return s.playlistGet(ctx, id)
}
func (s *Service) SavePlaylist(ctx context.Context, id string, input PlaylistSave) (any, error) {
	return s.playlistSave(ctx, id, input)
}
func (s *Service) DeletePlaylist(ctx context.Context, id string, revision int64) error {
	return s.playlistRemove(ctx, id, revision)
}
func (s *Service) SongSummary(raw map[string]any) map[string]any { return adminProjection(raw) }
func (s *Service) Tags(ctx context.Context) ([]Tag, error)       { return s.tags(ctx, s.db) }
func (s *Service) SaveTag(ctx context.Context, id string, input SaveTag) (any, error) {
	return s.saveTag(ctx, id, input)
}
func (s *Service) Favorites(ctx context.Context, q string, page, size int) (any, error) {
	return s.listFavorites(ctx, q, "ALL", "RECENT", page, size, false)
}
func (s *Service) FavoritesState(ctx context.Context) (map[string]any, error) {
	return s.favoriteIDs(ctx, s.db)
}
func (s *Service) ChangeFavorites(ctx context.Context, input FavoriteChange) (any, error) {
	return s.changeFavorites(ctx, input)
}
func (s *Service) Classification(ctx context.Context, song string) (State, error) {
	return s.state(ctx, s.db, song)
}
func (s *Service) ClassificationTasks(ctx context.Context, page, size int) (any, error) {
	return s.tasks(ctx, page, size)
}
func (s *Service) ClassificationTask(ctx context.Context, id string) (any, error) {
	return s.task(ctx, id, 0, 50, "")
}
func (s *Service) ControlClassificationTask(ctx context.Context, id, action string) (any, error) {
	return s.control(ctx, id, action)
}

// Artist queries must not call ensureArtists: the legacy list lazily creates
// profiles. Agent reads only persisted profiles plus the live song projection.
func (s *Service) ArtistProfiles(ctx context.Context) ([]map[string]any, error) {
	rows, e := query(ctx, s.db, s.q["a_list"])
	if e != nil {
		return nil, e
	}
	out := []map[string]any{}
	for _, r := range rows {
		v, e := artistProfile(r)
		if e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, nil
}
func (s *Service) ArtistCategory(ctx context.Context, id, kind string, revision int64) (any, error) {
	return s.category(ctx, id, kind, revision)
}
func (s *Service) ArtistProfile(ctx context.Context, id string) (map[string]any, error) {
	r, e := s.artist(ctx, s.db, id)
	if e != nil {
		return nil, e
	}
	return artistProfile(r)
}

// ValidatePlaylist checks the complete future selection without creating a
// playlist or taking a worker slot, and returns display-safe song snapshots.
func (s *Service) ValidatePlaylist(ctx context.Context, ids []string) ([]map[string]any, error) {
	if len(ids) > 500 {
		return nil, fail(400, "最多选择 500 首歌曲")
	}
	out := []map[string]any{}
	seen := map[string]bool{}
	e := store.WithinTx(ctx, s.db, func(tx store.DBTX) error {
		for _, id := range ids {
			if seen[id] {
				return fail(400, "歌曲重复")
			}
			seen[id] = true
			song, e := s.library.SongWith(ctx, tx, id)
			if e != nil {
				return e
			}
			out = append(out, adminProjection(song))
		}
		return nil
	})
	return out, e
}
