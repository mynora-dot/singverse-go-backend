package clientrelease

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
)

func variantFixture(t *testing.T, platform string) (*Service, map[string][]byte) {
	t.Helper()
	s, base := fixture(t, platform)
	dir := filepath.Join(s.Directory, platform)
	if err := os.Remove(filepath.Join(dir, "singverse-"+platform+".apk")); err != nil {
		t.Fatal(err)
	}
	index := struct {
		Manifest
		Variants []Variant `json:"variants"`
	}{Manifest: base}
	index.SchemaVersion, index.ABIs, index.Size, index.SHA256 = 2, nil, 0, ""
	payloads := map[string][]byte{}
	for _, abi := range platformABIs[platform] {
		data := []byte("APK fixture " + abi)
		digest := sha256.Sum256(data)
		name := "singverse-" + platform + "-" + abi + ".apk"
		index.Variants = append(index.Variants, Variant{abi, name, int64(len(data)), hex.EncodeToString(digest[:])})
		if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
			t.Fatal(err)
		}
		payloads[abi] = data
	}
	raw, _ := json.Marshal(index)
	if err := os.WriteFile(filepath.Join(dir, "release.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	return s, payloads
}

func TestPerABIManifestAndDownloadSelectTheSamePackage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, platform := range []string{"mobile", "tv"} {
		s, payloads := variantFixture(t, platform)
		r := gin.New()
		s.Register(r)
		for abi, data := range payloads {
			for _, mode := range []string{"query", "header"} {
				path := "/api/v1/client-releases/" + platform
				query := ""
				if mode == "query" {
					query = "?abi=" + abi
				}
				req := httptest.NewRequest("GET", path+query, nil)
				if mode == "header" {
					req.Header.Set("X-SingVerse-ABI", abi)
				}
				w := httptest.NewRecorder()
				r.ServeHTTP(w, req)
				var release Release
				if err := json.Unmarshal(w.Body.Bytes(), &release); err != nil || w.Code != 200 {
					t.Fatalf("manifest: %d %s: %v", w.Code, w.Body.String(), err)
				}
				if !release.Available || len(release.ABIs) != 1 || release.ABIs[0] != abi || len(release.Variants) != len(payloads) || release.URL != path+"/apk"+query {
					t.Fatalf("incorrect variant: %+v", release)
				}
				req = httptest.NewRequest("GET", release.URL, nil)
				if mode == "header" {
					req.Header.Set("X-SingVerse-ABI", abi)
				}
				w = httptest.NewRecorder()
				r.ServeHTTP(w, req)
				if w.Code != 200 || w.Body.String() != string(data) || !strings.Contains(w.Header().Get("Content-Disposition"), "-"+abi+".apk") || w.Header().Get("ETag") != `"`+release.SHA256+`"` {
					t.Fatalf("wrong APK served for %s %s: %d %s", platform, abi, w.Code, w.Body.String())
				}
			}
		}
	}
}

func TestInvalidABIAndCorruptVariantFailWithoutServingAnotherArchitecture(t *testing.T) {
	s, _ := variantFixture(t, "mobile")
	r := gin.New()
	s.Register(r)
	for _, query := range []string{"?abi=../outside", "?abi=universal", "?abi=x86", "?abi=arm64-v8a&abi=x86_64", "?abi="} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/client-releases/mobile/apk"+query, nil))
		if w.Code != 400 {
			t.Fatalf("invalid ABI accepted: %s %d", query, w.Code)
		}
	}
	if err := os.WriteFile(filepath.Join(s.Directory, "mobile/singverse-mobile-arm64-v8a.apk"), []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/client-releases/mobile/apk?abi=arm64-v8a", nil))
	if w.Code != 503 {
		t.Fatalf("corrupt variant served: %d", w.Code)
	}
	if release, err := s.CurrentForABI("mobile", "armeabi-v7a"); err != nil || !release.Available {
		t.Fatalf("healthy architecture unexpectedly unavailable: %+v %v", release, err)
	}
}

func TestIncompleteDuplicateOrEscapingVariantIndexIsRejected(t *testing.T) {
	s, _ := variantFixture(t, "tv")
	path := filepath.Join(s.Directory, "tv/release.json")
	original, _ := os.ReadFile(path)
	for _, mode := range []string{"missing", "duplicate", "escape", "universal"} {
		var index struct {
			Manifest
			Variants []Variant `json:"variants"`
		}
		json.Unmarshal(original, &index)
		switch mode {
		case "missing":
			index.Variants = index.Variants[:3]
		case "duplicate":
			index.Variants[0] = index.Variants[1]
		case "escape":
			index.Variants[0].FileName = "../outside.apk"
		case "universal":
			index.Variants[0].ABI = "universal"
		}
		raw, _ := json.Marshal(index)
		os.WriteFile(path, raw, 0600)
		if _, err := s.Current("tv"); err == nil {
			t.Fatalf("invalid index accepted: %s", mode)
		}
	}
}
