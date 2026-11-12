package clientrelease

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func fixture(t *testing.T, platform string) (*Service, Manifest) {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, platform)
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	data := []byte("isolated APK bytes")
	h := sha256.Sum256(data)
	sdk := 24
	if platform == "tv" {
		sdk = 23
	}
	m := Manifest{SchemaVersion: 1, Platform: platform, VersionCode: 1000, VersionName: "0.1.0", PackageName: "com.mynora.singverse." + platform, MinSDK: sdk, ABIs: []string{"arm64-v8a"}, Size: int64(len(data)), SHA256: hex.EncodeToString(h[:]), CertificateSHA256: strings.Repeat("a", 64), Notes: "release"}
	raw, _ := json.Marshal(m)
	if err := os.WriteFile(filepath.Join(dir, "release.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "singverse-"+platform+".apk"), data, 0600); err != nil {
		t.Fatal(err)
	}
	return New(root, ""), m
}
func TestPublicDownloadAndRange(t *testing.T) {
	for _, platform := range []string{"mobile", "tv"} {
		t.Run(platform, func(t *testing.T) {
			s, m := fixture(t, platform)
			gin.SetMode(gin.TestMode)
			r := gin.New()
			s.Register(r)
			for _, tt := range []struct {
				method, path, rangeHeader string
				status, size              int
			}{
				{"GET", "/api/v1/client-releases/" + platform, "", 200, -1},
				{"GET", "/api/v1/client-releases/" + platform + "/apk", "", 200, int(m.Size)},
				{"HEAD", "/api/v1/client-releases/" + platform + "/apk", "", 200, 0},
				{"GET", "/api/v1/client-releases/" + platform + "/apk", "bytes=0-3", 206, 4},
				{"GET", "/api/v1/client-releases/" + platform + "/apk", "bytes=1000-1001", 416, -1},
				{"GET", "/api/v1/client-releases/other", "", 404, -1},
			} {
				req := httptest.NewRequest(tt.method, tt.path, nil)
				if tt.rangeHeader != "" {
					req.Header.Set("Range", tt.rangeHeader)
				}
				w := httptest.NewRecorder()
				r.ServeHTTP(w, req)
				if w.Code != tt.status || (tt.size >= 0 && w.Body.Len() != tt.size) {
					t.Fatalf("%s %s: %d %s", tt.method, tt.path, w.Code, w.Body.String())
				}
				if strings.Contains(w.Body.String(), s.Directory) {
					t.Fatal("filesystem path exposed")
				}
			}
		})
	}
}
func TestRejectInvalidPackages(t *testing.T) {
	for _, mode := range []string{"digest", "metadata", "symlink", "missing", "oversize"} {
		t.Run(mode, func(t *testing.T) {
			s, m := fixture(t, "tv")
			dir := filepath.Join(s.Directory, "tv")
			apk := filepath.Join(dir, "singverse-tv.apk")
			switch mode {
			case "digest":
				os.WriteFile(apk, []byte(strings.Repeat("x", int(m.Size))), 0600)
			case "metadata":
				m.PackageName = "wrong"
				raw, _ := json.Marshal(m)
				os.WriteFile(filepath.Join(dir, "release.json"), raw, 0600)
			case "symlink":
				outside := filepath.Join(t.TempDir(), "outside.apk")
				os.WriteFile(outside, []byte("private"), 0600)
				os.Remove(apk)
				os.Symlink(outside, apk)
			case "missing":
				os.Remove(apk)
			case "oversize":
				os.WriteFile(filepath.Join(dir, "release.json"), []byte(strings.Repeat(" ", 65537)), 0600)
			}
			if _, err := s.Current("tv"); err == nil {
				t.Fatal("invalid package accepted")
			}
			gin.SetMode(gin.TestMode)
			r := gin.New()
			s.Register(r)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/client-releases/tv/apk", nil))
			if w.Code != 503 {
				t.Fatal(w.Code, w.Body.String())
			}
		})
	}
}
func TestUnavailableAndLegacyOverride(t *testing.T) {
	s, m := fixture(t, "tv")
	other, _ := fixture(t, "tv")
	s.TVOverride = filepath.Join(other.Directory, "tv")
	got, err := s.Current("tv")
	if err != nil || !got.Available || got.VersionCode != m.VersionCode {
		t.Fatal(got, err)
	}
	if got, err := New("", "").Current("mobile"); err != nil || got.Available {
		t.Fatal(got, err)
	}
	if _, err := s.Current("../tv"); err == nil {
		t.Fatal("invalid platform accepted")
	}
}

func TestQRCodeOriginValidation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	New("", "").Register(r)
	for _, tt := range []struct {
		origin string
		status int
	}{
		{"http://nas.local:18080", 200}, {"https://nas.example", 200},
		{"http://localhost:18080", 400}, {"http://127.0.0.1", 400}, {"http://[::1]", 400},
		{"https://nas.local/path", 400}, {"https://nas.local?q=x", 400}, {"https://user:pass@nas.local", 400},
	} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/client-releases/mobile/qr?origin="+url.QueryEscape(tt.origin), nil))
		if w.Code != tt.status {
			t.Fatal(tt.origin, w.Code)
		}
		if w.Code == 200 && !strings.HasPrefix(w.Body.String(), "\x89PNG") {
			t.Fatal("not a PNG")
		}
	}
}
