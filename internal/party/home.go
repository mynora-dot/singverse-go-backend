package party

import (
	"github.com/gin-gonic/gin"
	"github.com/mynora-dot/singverse-go-backend/internal/platform/store"
)

func (s *Service) registerHome(r *gin.Engine) {
	r.GET("/api/v1/tv/home", s.RequireActor(true), func(c *gin.Context) {
		index, err := s.current(c.Request.Context())
		if err != nil {
			s.report(c, err)
			return
		}
		recent := []document{}
		for _, entry := range index.entries[:min(8, len(index.entries))] {
			recent = append(recent, clone(entry.song))
		}
		lists, err := s.playlists(c.Request.Context(), "")
		if err != nil {
			s.report(c, err)
			return
		}
		featured := []document{}
		for _, v := range lists[:min(4, len(lists))] {
			v = clone(v)
			delete(v, "songIds")
			delete(v, "songs")
			featured = append(featured, v)
		}
		s.run(c, func(tx store.DBTX) (any, error) {
			server, err := s.serverInfo(c.Request.Context(), tx)
			if err != nil {
				return nil, err
			}
			return document{"server": server, "totalSongs": len(index.entries), "recentSongs": recent, "featuredPlaylists": featured, "generatedAt": s.now().UTC().Format("2006-01-02T15:04:05.999999999Z")}, nil
		})
	})
	r.GET("/api/v1/tv/releases/current", s.RequireActor(true), func(c *gin.Context) {
		v, err := s.release()
		if err != nil {
			s.report(c, err)
			return
		}
		c.JSON(200, v)
	})
	r.GET("/api/v1/tv/releases/apk", s.RequireActor(true), s.apk)
}
