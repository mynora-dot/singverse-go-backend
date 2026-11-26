package httpx

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// Artwork serves a current, authorized image representation. Callers must
// finish resolving the image and its permissions before calling this function.
func Artwork(c *gin.Context, mime string, data []byte) {
	tag := fmt.Sprintf(`"%x"`, sha256.Sum256(data))
	c.Header("Cache-Control", "private, max-age=300")
	c.Header("ETag", tag)
	c.Header("X-Content-Type-Options", "nosniff")
	c.Writer.Header().Add("Vary", "Authorization, Cookie")
	if matchesArtworkTag(strings.Join(c.Request.Header.Values("If-None-Match"), ","), tag) {
		c.Status(http.StatusNotModified)
		return
	}
	c.Data(http.StatusOK, mime, data)
}

func matchesArtworkTag(requested, tag string) bool {
	if strings.Trim(requested, " \t") == "*" {
		return true
	}
	for {
		requested = strings.TrimLeft(requested, " \t,")
		candidate := strings.TrimPrefix(requested, "W/")
		if !strings.HasPrefix(candidate, `"`) {
			return false
		}
		end := strings.IndexByte(candidate[1:], '"')
		if end < 0 {
			return false
		}
		candidate, requested = candidate[:end+2], strings.TrimLeft(candidate[end+2:], " \t")
		if requested != "" && requested[0] != ',' {
			return false
		}
		if candidate == tag {
			return true
		}
	}
}
