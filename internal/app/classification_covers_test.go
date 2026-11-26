package app

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"testing"
)

func readArtwork(t *testing.T, client *http.Client, url, bearer, etag string, status int) ([]byte, http.Header) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil || resp.StatusCode != status {
		t.Fatalf("GET %s: status %d, expected %d, error %v", url, resp.StatusCode, status, err)
	}
	return data, resp.Header
}

func TestDefaultClassificationCoversAcrossAdminAndTV(t *testing.T) {
	a := application(t)
	admin := newBusinessClient(t, a)
	admin.call("POST", "/api/v1/auth/login", map[string]any{"username": "admin", "password": "admin"}, "", 200)
	grant := admin.call("POST", "/api/v1/auth/tv-session", map[string]any{"name": "分类封面验收"}, "", 200)
	device := stringField(grant, "token")
	guest := &http.Client{}
	type coverTag struct {
		ID           string `json:"id"`
		CoverPath    string `json:"coverPath"`
		CoverVersion string `json:"coverVersion"`
	}
	readTags := func(client *http.Client, path, bearer string) []coverTag {
		t.Helper()
		data, _ := readArtwork(t, client, admin.origin+path, bearer, "", 200)
		var tags []coverTag
		if err := json.Unmarshal(data, &tags); err != nil {
			t.Fatal(err)
		}
		return tags
	}
	tags := readTags(admin.client, "/api/v1/classification/tags", "")
	tvTags := readTags(guest, "/api/v1/tv/library/classification-tags", device)
	if len(tags) != 34 || len(tvTags) != 34 {
		t.Fatal("fresh installation did not expose the complete vocabulary", len(tags), len(tvTags))
	}
	for i, tag := range tags {
		tvTag := tvTags[i]
		if tag.ID != tvTag.ID || tag.CoverPath == "" || tvTag.CoverPath == "" || tag.CoverVersion == "" || tag.CoverVersion != tvTag.CoverVersion {
			t.Fatal("admin and TV cover metadata differs", tag, tvTag)
		}
		adminImage, adminHeaders := readArtwork(t, admin.client, admin.origin+tag.CoverPath, "", "", 200)
		tvImage, tvHeaders := readArtwork(t, guest, admin.origin+tvTag.CoverPath, device, "", 200)
		sum := sha256.Sum256(adminImage)
		if !bytes.Equal(adminImage, tvImage) || hex.EncodeToString(sum[:]) != tag.CoverVersion || adminHeaders.Get("Content-Type") != "image/jpeg" || tvHeaders.Get("Content-Type") != "image/jpeg" || tvHeaders.Get("X-Content-Type-Options") != "nosniff" {
			t.Fatal("admin and TV did not serve the same valid cover", tag.ID)
		}
	}
	for _, target := range []struct {
		path   string
		client *http.Client
		bearer string
	}{
		{tags[0].CoverPath, admin.client, ""},
		{tvTags[0].CoverPath, guest, device},
	} {
		url := admin.origin + target.path
		etag := strconv.Quote(tags[0].CoverVersion)
		_, headers := readArtwork(t, target.client, url, target.bearer, "", 200)
		if headers.Get("ETag") != etag || headers.Get("Cache-Control") != "private, max-age=300" {
			t.Fatalf("formal cover cache headers on %s: %v", target.path, headers)
		}
		data, headers := readArtwork(t, target.client, url, target.bearer, etag, 304)
		if len(data) != 0 || headers.Get("ETag") != etag || headers.Get("Cache-Control") != "private, max-age=300" {
			t.Fatalf("unexpected cover revalidation on %s: %v", target.path, headers)
		}
		_, headers = readArtwork(t, guest, url, "", etag, 401)
		if headers.Get("ETag") != "" || headers.Get("Cache-Control") != "no-store" {
			t.Fatalf("unauthorized response permitted caching on %s: %v", target.path, headers)
		}
	}
	admin.call("PUT", "/api/v1/classification/tags/"+tags[0].ID, map[string]any{"dimension": "CUSTOM", "name": "家庭常唱", "aliases": []string{}, "active": false, "revision": 1}, "", 200)
	readArtwork(t, guest, admin.origin+tvTags[0].CoverPath, device, strconv.Quote(tvTags[0].CoverVersion), 404)
	if len(readTags(guest, "/api/v1/tv/library/classification-tags", device)) != 33 {
		t.Fatal("disabled default label still appears on TV")
	}
	admin.call("DELETE", tags[1].CoverPath+"?version="+tags[1].CoverVersion, nil, "", 200)
	readArtwork(t, admin.client, admin.origin+tags[1].CoverPath, "", strconv.Quote(tags[1].CoverVersion), 404)
	readArtwork(t, guest, admin.origin+tvTags[1].CoverPath, device, strconv.Quote(tvTags[1].CoverVersion), 404)
}
