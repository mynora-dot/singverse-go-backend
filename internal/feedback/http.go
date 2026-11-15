package feedback

import (
	"errors"
	"github.com/gin-gonic/gin"
	"github.com/mynora-dot/singverse-go-backend/internal/platform/httpx"
	"strconv"
)

func (s *Service) report(c *gin.Context, err error) {
	var p *failure
	if errors.As(err, &p) {
		httpx.Error(c, p.status, "about:blank", p.detail)
	} else {
		httpx.Error(c, 500, "about:blank", "反馈暂时无法读取")
	}
}
func (s *Service) Register(g *gin.RouterGroup) {
	g.GET("/missing-song-feedback", func(c *gin.Context) {
		page, e1 := strconv.Atoi(c.DefaultQuery("page", "0"))
		size, e2 := strconv.Atoi(c.DefaultQuery("size", "20"))
		if e1 != nil || e2 != nil {
			httpx.Error(c, 400, "about:blank", "分页无效")
			return
		}
		out, err := s.List(c.Request.Context(), c.DefaultQuery("q", ""), c.DefaultQuery("status", "ALL"), page, size)
		if err != nil {
			s.report(c, err)
			return
		}
		c.JSON(200, out)
	})
	g.GET("/missing-song-feedback/:id", func(c *gin.Context) {
		out, err := s.Get(c.Request.Context(), c.Param("id"))
		if err != nil {
			s.report(c, err)
			return
		}
		c.JSON(200, out)
	})
	g.POST("/missing-song-feedback", func(c *gin.Context) {
		var in Create
		if !httpx.Decode(c, &in, 16384) {
			return
		}
		out, err := s.Create(c.Request.Context(), in)
		if err != nil {
			s.report(c, err)
			return
		}
		c.JSON(201, out)
	})
	g.PUT("/missing-song-feedback/:id", func(c *gin.Context) {
		var in Update
		if !httpx.Decode(c, &in, 16384) {
			return
		}
		out, err := s.Update(c.Request.Context(), c.Param("id"), in)
		if err != nil {
			s.report(c, err)
			return
		}
		c.JSON(200, out)
	})
}

func (s *Service) RegisterParty(r *gin.Engine, actor gin.HandlerFunc) {
	r.POST("/api/v1/party/missing-song-feedback", actor, func(c *gin.Context) {
		var input Create
		if !httpx.Decode(c, &input, 16384) {
			return
		}
		raw, _ := c.Get("partyActor")
		identity, _ := raw.(map[string]any)
		actorID, _ := identity["id"].(string)
		name, _ := identity["name"].(string)
		if actorID == "" {
			httpx.Error(c, 401, "about:blank", "请先加入聚会")
			return
		}
		out, err := s.CreateParty(c.Request.Context(), input, actorID, name)
		if err != nil {
			s.report(c, err)
			return
		}
		c.Header("Cache-Control", "no-store")
		c.JSON(201, out)
	})
}
