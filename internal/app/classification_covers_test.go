package app

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"testing"
)

func TestDefaultClassificationCoversAcrossAdminAndTV(t *testing.T) {
	a := application(t)
	admin := newBusinessClient(t, a)
	admin.call("POST", "/api/v1/auth/login", map[string]any{"username": "admin", "password": "admin"}, "", 200)
	grant := admin.call("POST", "/api/v1/auth/tv-session", map[string]any{"name": "分类封面验收"}, "", 200)
	device := stringField(grant, "token")
	guest := &http.Client{}
	get := func(client *http.Client, path, bearer string, status int) ([]byte, http.Header) {
		t.Helper()
		req, err := http.NewRequest("GET", admin.origin+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		data, err := io.ReadAll(resp.Body)
		if err != nil || resp.StatusCode != status {
			t.Fatalf("GET %s: status %d, error %v", path, resp.StatusCode, err)
		}
		return data, resp.Header
	}
	type coverTag struct {
		ID           string `json:"id"`
		CoverPath    string `json:"coverPath"`
		CoverVersion string `json:"coverVersion"`
	}
	readTags := func(client *http.Client, path, bearer string) []coverTag {
		t.Helper()
		data, _ := get(client, path, bearer, 200)
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
		adminImage, adminHeaders := get(admin.client, tag.CoverPath, "", 200)
		tvImage, tvHeaders := get(guest, tvTag.CoverPath, device, 200)
		sum := sha256.Sum256(adminImage)
		if !bytes.Equal(adminImage, tvImage) || hex.EncodeToString(sum[:]) != tag.CoverVersion || adminHeaders.Get("Content-Type") != "image/jpeg" || tvHeaders.Get("Content-Type") != "image/jpeg" || tvHeaders.Get("X-Content-Type-Options") != "nosniff" {
			t.Fatal("admin and TV did not serve the same valid cover", tag.ID)
		}
	}
	get(guest, tags[0].CoverPath, "", 401)
	get(guest, tvTags[0].CoverPath, "", 401)
	admin.call("PUT", "/api/v1/classification/tags/"+tags[0].ID, map[string]any{"dimension": "CUSTOM", "name": "家庭常唱", "aliases": []string{}, "active": false, "revision": 1}, "", 200)
	get(guest, tvTags[0].CoverPath, device, 404)
	if len(readTags(guest, "/api/v1/tv/library/classification-tags", device)) != 33 {
		t.Fatal("disabled default label still appears on TV")
	}
	admin.call("DELETE", tags[1].CoverPath+"?version="+tags[1].CoverVersion, nil, "", 200)
	get(admin.client, tags[1].CoverPath, "", 404)
	get(guest, tvTags[1].CoverPath, device, 404)
}
