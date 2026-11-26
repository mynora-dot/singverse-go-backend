package httpx

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestArtworkConditionalResponse(t *testing.T) {
	image := []byte("display image bytes")
	tag := fmt.Sprintf(`"%x"`, sha256.Sum256(image))
	router := gin.New()
	router.GET("/image", func(c *gin.Context) { Artwork(c, "image/png", image) })
	for _, tc := range []struct {
		name string
		tags []string
		want int
	}{
		{"fresh", nil, http.StatusOK},
		{"match", []string{tag}, http.StatusNotModified},
		{"weak across header lines", []string{`"other,tag"`, "W/" + tag}, http.StatusNotModified},
		{"wildcard", []string{"*"}, http.StatusNotModified},
		{"different", []string{`"different"`}, http.StatusOK},
		{"fragment of quoted tag", []string{`"before,` + tag + `,after"`}, http.StatusOK},
		{"malformed", []string{tag + "invalid"}, http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/image", nil)
			for _, value := range tc.tags {
				req.Header.Add("If-None-Match", value)
			}
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			if w.Code != tc.want || w.Header().Get("ETag") != tag || w.Header().Get("Cache-Control") != "private, max-age=300" || w.Header().Get("Vary") != "Authorization, Cookie" {
				t.Fatalf("unexpected artwork response: %d %v", w.Code, w.Header())
			}
			if tc.want == http.StatusNotModified && w.Body.Len() != 0 {
				t.Fatal("304 response has a body")
			}
			if tc.want == http.StatusOK && (w.Body.String() != string(image) || w.Header().Get("Content-Type") != "image/png") {
				t.Fatal("200 response lost the image representation")
			}
		})
	}
}
