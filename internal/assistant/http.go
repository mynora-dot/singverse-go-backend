package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/mynora-dot/singverse-go-backend/internal/platform/httpx"
)

// Register is mounted after the application's administrator Cookie, origin and
// CSRF middleware. No alternate token, public route or cross-origin API exists.
func (s *Service) Register(r *gin.Engine) {
	g := r.Group("/api/v1/ai/assistant")
	g.Use(func(c *gin.Context) { c.Header("Cache-Control", "no-store"); c.Next() })
	g.GET("/status", func(c *gin.Context) { v, e := s.Status(c.Request.Context()); reply(c, v, e) })
	g.GET("/tools", func(c *gin.Context) { c.JSON(200, s.Catalog()) })
	g.GET("/conversations", func(c *gin.Context) { v, e := s.Conversations(c.Request.Context()); reply(c, v, e) })
	g.POST("/conversations", func(c *gin.Context) {
		var v struct {
			Title string `json:"title"`
		}
		if !httpx.Decode(c, &v, 2000) {
			return
		}
		out, e := s.Create(c.Request.Context(), v.Title)
		reply(c, out, e)
	})
	g.GET("/conversations/:id", func(c *gin.Context) { v, e := s.Detail(c.Request.Context(), c.Param("id")); reply(c, v, e) })
	g.DELETE("/conversations/:id", func(c *gin.Context) { e := s.Delete(c.Request.Context(), c.Param("id")); reply(c, nil, e) })
	g.POST("/conversations/:id/messages", func(c *gin.Context) {
		var v struct {
			RequestID string `json:"requestId"`
			Content   string `json:"content"`
		}
		if !httpx.Decode(c, &v, 40000) {
			return
		}
		out, e := s.Submit(c.Request.Context(), c.Param("id"), v.RequestID, v.Content)
		reply(c, out, e)
	})
	g.GET("/runs/:id", func(c *gin.Context) { v, e := s.Run(c.Request.Context(), c.Param("id")); reply(c, v, e) })
	g.POST("/runs/:id/stop", func(c *gin.Context) { e := s.Stop(c.Request.Context(), c.Param("id")); reply(c, nil, e) })
	g.GET("/runs/:id/events", s.eventsHTTP)
	g.POST("/runs/:id/operations/:operationId/decision", func(c *gin.Context) {
		var v struct {
			RequestID string `json:"requestId"`
			Revision  string `json:"revision"`
			Approve   *bool  `json:"approve"`
		}
		if !httpx.Decode(c, &v, 2000) {
			return
		}
		if v.Approve == nil {
			reply(c, nil, fail(400, "请选择确认或拒绝"))
			return
		}
		out, e := s.Decide(c.Request.Context(), c.Param("id"), c.Param("operationId"), v.RequestID, v.Revision, *v.Approve)
		reply(c, out, e)
	})
	g.GET("/runs/:id/operations/:operationId/reconcile", func(c *gin.Context) {
		var op Operation
		e := s.db.GetContext(c.Request.Context(), &op, s.q["operation"], c.Param("operationId"), c.Param("id"))
		if e != nil {
			reply(c, nil, missing(e))
			return
		}
		d, ok := s.byName[op.ToolName]
		if !ok || d.Reconcile == nil {
			reply(c, map[string]any{"state": op.State, "page": d.Page, "message": "请打开业务页面核对；不会自动重放"}, nil)
			return
		}
		v, e := d.Reconcile(c.Request.Context(), json.RawMessage(op.Arguments), op.ID)
		reply(c, map[string]any{"state": op.State, "evidence": v, "page": d.Page}, e)
	})
}
func reply(c *gin.Context, v any, e error) {
	if e != nil {
		code := 500
		var known interface{ StatusCode() int }
		if errors.As(e, &known) {
			code = known.StatusCode()
		}
		httpx.Error(c, code, "about:blank", publicError(e))
		return
	}
	if v == nil {
		c.Status(204)
	} else {
		c.JSON(200, v)
	}
}
func (s *Service) eventsHTTP(c *gin.Context) {
	id := c.Param("id")
	if _, e := s.Run(c.Request.Context(), id); e != nil {
		reply(c, nil, e)
		return
	}
	cursor := c.GetHeader("Last-Event-ID")
	if cursor == "" {
		cursor = c.Query("after")
	}
	if cursor == "" {
		cursor = "0"
	}
	after, e := strconv.ParseInt(cursor, 10, 64)
	if e != nil || after < 0 {
		reply(c, nil, fail(400, "事件游标无效"))
		return
	}
	c.Header("Content-Type", "text/event-stream; charset=utf-8")
	c.Header("X-Accel-Buffering", "no")
	c.Header("Connection", "keep-alive")
	ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Minute)
	defer cancel()
	ticker := time.NewTicker(300 * time.Millisecond)
	defer ticker.Stop()
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		run, err := s.Run(ctx, id)
		if err != nil {
			return
		}
		events, err := s.Events(ctx, id, after)
		if err != nil {
			return
		}
		for _, event := range events {
			if _, err = fmt.Fprintf(c.Writer, "id: %d\nevent: message\ndata: %s\n\n", event.Sequence, encode(event)); err != nil {
				return
			}
			after = event.Sequence
		}
		c.Writer.Flush()
		if run.Status != "RUNNING" && after >= run.NextSequence {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-heartbeat.C:
			if _, e = fmt.Fprint(c.Writer, ": heartbeat\n\n"); e != nil {
				return
			}
			c.Writer.Flush()
		case <-ticker.C:
		}
	}
}
