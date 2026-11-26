package party

import (
	"github.com/gin-gonic/gin"
	"github.com/mynora-dot/singverse-go-backend/internal/platform/store"
)

func (s *Service) registerDeviceRoutes(r *gin.Engine) {
	r.GET("/api/v1/server-info", s.serverInfoHTTP)
	r.POST("/api/v1/auth/tv-session", s.loginDeviceHTTP)
	r.DELETE("/api/v1/admin/tv-devices/:id", s.revokeDeviceHTTP)
	r.GET("/api/v1/admin/party/televisions", s.onlineTelevisionsHTTP)
	r.POST("/api/v1/admin/party/televisions/:id/join", s.joinTelevisionHTTP)
}

func (s *Service) serverInfoHTTP(c *gin.Context) {
	s.run(c, func(tx store.DBTX) (any, error) { return s.serverInfo(c.Request.Context(), tx) })
}

// The administrator Cookie/CSRF boundary requires a completed account login,
// including MFA when enabled, before issuing a TV-only credential.
func (s *Service) loginDeviceHTTP(c *gin.Context) {
	in, ok := decode(c)
	if !ok {
		return
	}
	s.run(c, func(tx store.DBTX) (any, error) { return s.loginDevice(c.Request.Context(), tx, in) })
}

func (s *Service) revokeDeviceHTTP(c *gin.Context) {
	s.run(c, func(tx store.DBTX) (any, error) { return nil, s.revoke(c.Request.Context(), tx, c.Param("id")) })
}
