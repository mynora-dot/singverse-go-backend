package catalog

import (
	"context"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/mynora-dot/singverse-go-backend/internal/platform/httpx"
	"github.com/mynora-dot/singverse-go-backend/internal/platform/search"
	"github.com/mynora-dot/singverse-go-backend/internal/platform/store"
)

// Favorites belong to this NAS, independent of the administrator or TV device.
// Snapshots only identify a removed song; the live library remains authoritative
// for current metadata and playback. No media is written or removed here.
type FavoriteChange struct {
	SongIDs  []string `json:"songIds"`
	Selected *bool    `json:"selected"`
}

func (s *Service) favoriteIDs(ctx context.Context, tx store.DBTX) (map[string]any, error) {
	rows, err := query(ctx, tx, s.q["f_ids"])
	if err != nil {
		return nil, err
	}
	state, err := one(ctx, tx, s.q["f_revision"])
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, str(row["song_id"]))
	}
	return map[string]any{"songIds": ids, "revision": num(state["revision"])}, nil
}

func (s *Service) changeFavorites(ctx context.Context, in FavoriteChange) (any, error) {
	if in.Selected == nil || len(in.SongIDs) < 1 || len(in.SongIDs) > 500 {
		return nil, fail(400, "请提供目标收藏状态及 1～500 首歌曲")
	}
	seen := map[string]bool{}
	for _, id := range in.SongIDs {
		if !textValid(id, 160, false) || strings.TrimSpace(id) != id || seen[id] {
			return nil, fail(400, "歌曲 ID 无效或重复")
		}
		seen[id] = true
	}
	var out any
	err := store.WithinTx(ctx, s.db, func(tx store.DBTX) error {
		if err := s.exec(ctx, tx, "f_lock"); err != nil {
			return err
		}
		current, err := s.favoriteIDs(ctx, tx)
		if err != nil {
			return err
		}
		existing := map[string]bool{}
		for _, id := range current["songIds"].([]string) {
			existing[id] = true
		}
		library := map[string]map[string]any{}
		if *in.Selected {
			songs, err := s.library.LibrarySongs(ctx, tx)
			if err != nil {
				return err
			}
			for _, song := range songs {
				library[str(song["id"])] = song
			}
			// Validate the entire batch before writing anything. Retrying an
			// already selected, subsequently removed song remains a no-op.
			for _, id := range in.SongIDs {
				if !existing[id] && library[id] == nil {
					return fail(409, "部分歌曲已不在曲库，请刷新后重试")
				}
			}
		}
		changed := false
		for _, id := range in.SongIDs {
			if existing[id] == *in.Selected {
				continue
			}
			if *in.Selected {
				err = s.exec(ctx, tx, "f_insert", id, now(), encode(adminProjection(library[id])))
			} else {
				err = s.exec(ctx, tx, "f_remove", id)
			}
			if err != nil {
				return err
			}
			changed = true
		}
		if changed {
			if err := s.exec(ctx, tx, "f_increment"); err != nil {
				return err
			}
		}
		out, err = s.favoriteIDs(ctx, tx)
		return err
	})
	return out, err
}

func (s *Service) listFavorites(ctx context.Context, q, availability, order string, p, size int, tv bool) (any, error) {
	if err := pages(p, size); err != nil {
		return nil, err
	}
	if !textValid(q, 200, true) {
		return nil, fail(400, "搜索词过长")
	}
	if !has(availability, "ALL", "AVAILABLE", "UNAVAILABLE") || !has(order, "RECENT", "TITLE", "ARTIST") {
		return nil, fail(400, "收藏筛选或排序无效")
	}
	var out any
	err := store.WithinTx(ctx, s.db, func(tx store.DBTX) error {
		rows, err := query(ctx, tx, s.q["f_list"])
		if err != nil {
			return err
		}
		state, err := one(ctx, tx, s.q["f_revision"])
		if err != nil {
			return err
		}
		songs, err := s.library.LibrarySongs(ctx, tx)
		if err != nil {
			return err
		}
		live := map[string]map[string]any{}
		for _, song := range songs {
			live[str(song["id"])] = song
		}
		items := []map[string]any{}
		needle := search.Normalize(q)
		for _, row := range rows {
			id := str(row["song_id"])
			available := live[id] != nil
			if availability == "AVAILABLE" && !available || availability == "UNAVAILABLE" && available {
				continue
			}
			v, err := decode[map[string]any](row["song_json"])
			if err != nil {
				return err
			}
			if available {
				v = adminProjection(live[id])
			}
			if v == nil {
				v = map[string]any{"title": "已移除歌曲", "artists": []string{}}
			}
			v["id"], v["addedAt"], v["available"] = id, row["added_at"], available
			if !available {
				delete(v, "coverPath")
			}
			if tv && str(v["coverPath"]) != "" {
				v["coverPath"] = "/api/v1/tv/library/songs/" + id + "/cover"
			}
			if needle != "" && !strings.Contains(search.Normalize(str(v["title"])+" "+strings.Join(stringsArray(v["artists"]), " ")), needle) {
				continue
			}
			items = append(items, v)
		}
		sort.Slice(items, func(i, j int) bool {
			a, b := items[i], items[j]
			if order == "RECENT" && str(a["addedAt"]) != str(b["addedAt"]) {
				return str(a["addedAt"]) > str(b["addedAt"])
			}
			if order == "ARTIST" {
				x, y := search.Normalize(strings.Join(stringsArray(a["artists"]), " ")), search.Normalize(strings.Join(stringsArray(b["artists"]), " "))
				if x != y {
					return x < y
				}
			}
			if order != "RECENT" {
				x, y := search.Normalize(str(a["title"])), search.Normalize(str(b["title"]))
				if x != y {
					return x < y
				}
			}
			return str(a["id"]) < str(b["id"])
		})
		result := page(items, p, size)
		result["totalFavorites"], result["revision"] = len(rows), num(state["revision"])
		out = result
		return nil
	})
	return out, err
}

func (s *Service) registerFavorites(r *gin.Engine, base string, tv bool, middleware ...gin.HandlerFunc) {
	group := r.Group(base, middleware...)
	group.GET("", func(c *gin.Context) {
		p, size, err := pageParams(c)
		if err != nil {
			s.report(c, err)
			return
		}
		v, err := s.listFavorites(c.Request.Context(), c.Query("q"), c.DefaultQuery("availability", "ALL"), c.DefaultQuery("sort", "RECENT"), p, size, tv)
		s.reply(c, 200, v, err)
	})
	group.GET("/ids", func(c *gin.Context) {
		var v any
		err := store.WithinTx(c.Request.Context(), s.db, func(tx store.DBTX) error {
			var err error
			v, err = s.favoriteIDs(c.Request.Context(), tx)
			return err
		})
		s.reply(c, 200, v, err)
	})
	group.PUT("", func(c *gin.Context) {
		var in FavoriteChange
		if !httpx.Decode(c, &in, 100000) {
			return
		}
		v, err := s.changeFavorites(c.Request.Context(), in)
		s.reply(c, 200, v, err)
	})
}

func (s *Service) RegisterDeviceFavorites(r *gin.Engine, deviceOnly gin.HandlerFunc) {
	s.registerFavorites(r, "/api/v1/tv/favorites", true, deviceOnly)
}
