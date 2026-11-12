// Package clientrelease serves immutable, locally bundled Android releases.
package clientrelease

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/mynora-dot/singverse-go-backend/internal/platform/httpx"
	qrcode "github.com/skip2/go-qrcode"
)

const MaxAPKSize int64 = 256 << 20

var digestPattern = regexp.MustCompile("^[a-f0-9]{64}$")

type Manifest struct {
	SchemaVersion     int      `json:"schemaVersion"`
	Platform          string   `json:"platform"`
	VersionCode       int64    `json:"versionCode"`
	VersionName       string   `json:"versionName"`
	PackageName       string   `json:"packageName"`
	MinSDK            int      `json:"minSdk"`
	ABIs              []string `json:"abis"`
	Size              int64    `json:"size"`
	SHA256            string   `json:"sha256"`
	CertificateSHA256 string   `json:"certificateSha256"`
	Notes             string   `json:"notes"`
}
type Release struct {
	Manifest
	Available bool   `json:"available"`
	URL       string `json:"url,omitempty"`
}
type Service struct{ Directory, TVOverride string }

func New(directory, tvOverride string) *Service { return &Service{directory, tvOverride} }
func validPlatform(platform string) bool        { return platform == "mobile" || platform == "tv" }
func (s *Service) location(platform string) (string, string) {
	if platform == "tv" && s.TVOverride != "" {
		return s.TVOverride, ""
	}
	return s.Directory, platform
}

// Open only regular files whose resolved path remains inside the configured root.
func openRegular(root, relative string) (*os.File, error) {
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	name := filepath.Join(root, relative)
	stat, err := os.Lstat(name)
	if err != nil || !stat.Mode().IsRegular() {
		return nil, errors.New("release file unavailable")
	}
	resolved, err := filepath.EvalSymlinks(name)
	if err != nil {
		return nil, err
	}
	rel, err := filepath.Rel(root, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return nil, errors.New("release file outside root")
	}
	return os.Open(name)
}
func (s *Service) Current(platform string) (Release, error) {
	if !validPlatform(platform) {
		return Release{}, os.ErrNotExist
	}
	root, sub := s.location(platform)
	if root == "" {
		return Release{}, nil
	}
	// A development checkout may have no staged package for this platform.
	if _, err := os.Stat(filepath.Join(root, sub)); os.IsNotExist(err) {
		return Release{}, nil
	}
	f, err := openRegular(root, filepath.Join(sub, "release.json"))
	if err != nil {
		return Release{}, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, 65537))
	if err != nil || len(raw) > 65536 {
		return Release{}, errors.New("invalid release manifest")
	}
	var m Manifest
	if err = json.Unmarshal(raw, &m); err != nil {
		return Release{}, err
	}
	expectedSDK := 24
	if platform == "tv" {
		expectedSDK = 23
	}
	expectedPackage := "com.mynora.singverse." + platform
	if m.SchemaVersion != 1 || m.Platform != platform || m.VersionCode < 1 || m.VersionCode > 2100000000 ||
		strings.TrimSpace(m.VersionName) == "" || len(m.VersionName) > 80 || m.PackageName != expectedPackage ||
		m.MinSDK != expectedSDK || len(m.ABIs) == 0 || m.Size < 1 || m.Size > MaxAPKSize ||
		!digestPattern.MatchString(m.SHA256) || !digestPattern.MatchString(m.CertificateSHA256) || len(m.Notes) > 16000 {
		return Release{}, errors.New("invalid release metadata")
	}
	apk, err := openRegular(root, filepath.Join(sub, "singverse-"+platform+".apk"))
	if err != nil {
		return Release{}, err
	}
	defer apk.Close()
	info, err := apk.Stat()
	if err != nil || info.Size() != m.Size {
		return Release{}, errors.New("release size mismatch")
	}
	h := sha256.New()
	if _, err = io.Copy(h, io.LimitReader(apk, MaxAPKSize+1)); err != nil {
		return Release{}, err
	}
	if hex.EncodeToString(h.Sum(nil)) != m.SHA256 {
		return Release{}, errors.New("release digest mismatch")
	}
	return Release{Manifest: m, Available: true, URL: "/api/v1/client-releases/" + platform + "/apk"}, nil
}
func (s *Service) Register(r *gin.Engine) {
	r.GET("/api/v1/client-releases/:platform", s.manifest)
	r.GET("/api/v1/client-releases/:platform/apk", s.apk)
	r.HEAD("/api/v1/client-releases/:platform/apk", s.apk)
	r.GET("/api/v1/client-releases/:platform/qr", s.qr)
}
func (s *Service) read(c *gin.Context) (Release, bool) {
	c.Header("Cache-Control", "no-store")
	platform := c.Param("platform")
	if !validPlatform(platform) {
		httpx.Error(c, 404, "about:blank", "安装包类型不存在")
		return Release{}, false
	}
	release, err := s.Current(platform)
	if err != nil {
		httpx.Error(c, 503, "about:blank", "安装包暂不可用，请联系管理员")
		return Release{}, false
	}
	return release, true
}
func (s *Service) manifest(c *gin.Context) {
	release, ok := s.read(c)
	if !ok {
		return
	}
	if !release.Available {
		c.JSON(200, gin.H{"available": false})
		return
	}
	c.JSON(200, release)
}
func (s *Service) apk(c *gin.Context) {
	release, ok := s.read(c)
	if !ok {
		return
	}
	if !release.Available {
		httpx.Error(c, 404, "about:blank", "尚未发布安装包")
		return
	}
	root, sub := s.location(c.Param("platform"))
	filename := "singverse-" + release.Platform + ".apk"
	f, err := openRegular(root, filepath.Join(sub, filename))
	if err != nil {
		httpx.Error(c, 503, "about:blank", "安装包暂不可用")
		return
	}
	defer f.Close()
	// Recheck the exact descriptor served in case a configured directory changed.
	info, err := f.Stat()
	if err != nil || info.Size() != release.Size {
		httpx.Error(c, 503, "about:blank", "安装包已变化，请重试")
		return
	}
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil || hex.EncodeToString(h.Sum(nil)) != release.SHA256 {
		httpx.Error(c, 503, "about:blank", "安装包已变化，请重试")
		return
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		httpx.Error(c, 503, "about:blank", "安装包暂不可用")
		return
	}
	c.Header("Content-Type", "application/vnd.android.package-archive")
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("ETag", `"`+release.SHA256+`"`)
	http.ServeContent(c.Writer, c.Request, filename, info.ModTime(), f)
}

func (s *Service) qr(c *gin.Context) {
	if !validPlatform(c.Param("platform")) {
		httpx.Error(c, 404, "about:blank", "Unknown platform")
		return
	}
	raw := c.Query("origin")
	u, err := url.Parse(raw)
	if err != nil || len(raw) > 2048 || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.Path != "" || u.RawPath != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" {
		httpx.Error(c, 400, "about:blank", "Use the NAS HTTP(S) origin")
		return
	}
	host := strings.ToLower(u.Hostname())
	ip := net.ParseIP(host)
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || host == "" || (ip != nil && (ip.IsLoopback() || ip.IsUnspecified())) {
		httpx.Error(c, 400, "about:blank", "Use a NAS address reachable by the phone")
		return
	}
	png, err := qrcode.Encode(u.String()+"/downloads/#"+c.Param("platform"), qrcode.Medium, 256)
	if err != nil {
		httpx.Error(c, 400, "about:blank", "Invalid NAS origin")
		return
	}
	c.Header("Cache-Control", "no-store")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Data(200, "image/png", png)
}
