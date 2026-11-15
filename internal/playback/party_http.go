package playback

import (
	"database/sql"
	"errors"

	"github.com/gin-gonic/gin"
	"github.com/mynora-dot/singverse-go-backend/internal/platform/store"
)

// Read-only projection over the same persisted activity used by the admin UI.
// Neither a party join nor a library preview can manufacture a play record.
func (s *Service) RegisterParty(r *gin.Engine, actor gin.HandlerFunc) {
	r.GET("/api/v1/party/library/recent", actor, func(c *gin.Context) { s.partyListHTTP(c, false) })
	r.GET("/api/v1/party/library/popular", actor, func(c *gin.Context) { s.partyListHTTP(c, true) })
}

func (s *Service) partyListHTTP(c *gin.Context, popular bool) {
	defaultPeriod := "all"
	if popular {
		defaultPeriod = "30d"
	}
	f, err := s.filterWithDefault(c, defaultPeriod)
	if err != nil {
		s.error(c, err)
		return
	}
	p, size, err := page(c)
	if err != nil {
		s.error(c, err)
		return
	}
	f["size"], f["offset"] = size, p*size
	name, count := "partyRecent", "countPartyRecent"
	if popular {
		name, count = "partyPopular", "countPartyPopular"
	}
	items := []map[string]any{}
	var total int64
	err = store.WithinTx(c.Request.Context(), s.db, func(tx store.DBTX) error {
		query, args, e := s.statement(count, f)
		if e != nil {
			return e
		}
		if e = tx.QueryRowContext(c.Request.Context(), query, args...).Scan(&total); e != nil {
			return e
		}
		rows, e := s.rows(c.Request.Context(), tx, name, f)
		if e != nil {
			return e
		}
		for _, row := range rows {
			history, e := historyView(row, popular)
			if e != nil {
				return e
			}
			id := text(row, "song_id")
			projection, e := s.song(c.Request.Context(), tx, id)
			if e != nil {
				var problem interface{ StatusCode() int }
				if !errors.Is(e, sql.ErrNoRows) && !(errors.As(e, &problem) && problem.StatusCode() == 404) {
					return e
				}
				projection = map[string]any{"id": id, "title": history["title"], "artists": history["artists"], "available": false}
			} else if _, ok := projection["available"]; !ok {
				projection["available"] = true
			}
			projection["lastPlayedAt"] = history["lastPlayedAt"]
			if !popular {
				projection["lastPlayedAt"] = history["startedAt"]
			}
			if popular {
				projection["playCount"], projection["effectivePlayCount"] = history["playCount"], history["effectivePlayCount"]
			}
			items = append(items, projection)
		}
		return nil
	})
	if err != nil {
		s.error(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(200, gin.H{"items": items, "totalElements": total, "page": p, "size": size})
}
