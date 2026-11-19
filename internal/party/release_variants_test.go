package party

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/mynora-dot/singverse-go-backend/internal/platform/config"
)

func TestLegacyTVRoutesReadVariantIndex(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, override := range []bool{false, true} {
		t.Run(map[bool]string{false: "bundled", true: "override"}[override], func(t *testing.T) {
			root := t.TempDir()
			directory := filepath.Join(root, "tv")
			if err := os.Mkdir(directory, 0700); err != nil {
				t.Fatal(err)
			}
			cfg := config.Config{ClientReleaseDirectory: root}
			if override {
				cfg.TVReleaseDirectory = directory
			}
			s := &Service{cfg: cfg}
			var variants []map[string]any
			var expected []byte
			for _, abi := range []string{"armeabi-v7a", "arm64-v8a", "x86", "x86_64"} {
				data := []byte("TV fixture " + abi)
				digest := sha256.Sum256(data)
				name := "singverse-tv-" + abi + ".apk"
				if err := os.WriteFile(filepath.Join(directory, name), data, 0600); err != nil {
					t.Fatal(err)
				}
				variants = append(variants, map[string]any{"abi": abi, "fileName": name, "size": len(data), "sha256": hex.EncodeToString(digest[:])})
				if abi == "armeabi-v7a" {
					expected = data
				}
			}
			index := map[string]any{"schemaVersion": 2, "platform": "tv", "versionCode": 1, "versionName": "0.0.1", "packageName": "com.mynora.singverse.tv", "minSdk": 21, "certificateSha256": strings.Repeat("a", 64), "notes": "", "variants": variants}
			raw, err := json.Marshal(index)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(directory, "release.json"), raw, 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(directory, "singverse-tv.apk"), []byte("stale universal"), 0600); err != nil {
				t.Fatal(err)
			}
			release, err := s.release()
			digest := sha256.Sum256(expected)
			if err != nil || release["available"] != true || release["sha256"] != hex.EncodeToString(digest[:]) {
				t.Fatalf("wrong legacy metadata: %+v %v", release, err)
			}
			download := func() *httptest.ResponseRecorder {
				w := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(w)
				c.Request = httptest.NewRequest("GET", "/api/v1/tv/releases/apk", nil)
				s.apk(c)
				return w
			}
			if w := download(); w.Code != 200 || w.Body.String() != string(expected) {
				t.Fatalf("legacy download did not match index: %d %q", w.Code, w.Body.String())
			}
			if err := os.WriteFile(filepath.Join(directory, "singverse-tv-armeabi-v7a.apk"), []byte("corrupt"), 0600); err != nil {
				t.Fatal(err)
			}
			if w := download(); w.Code != 503 {
				t.Fatalf("invalid variant fell back to stale package: %d %q", w.Code, w.Body.String())
			}
		})
	}
}
