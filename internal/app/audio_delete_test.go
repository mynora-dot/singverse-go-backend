package app

import (
	"net/http"
	"testing"
)

func TestAccompanimentDeleteRequiresAdministratorAndCSRF(t *testing.T) {
	a := application(t)
	c := newBusinessClient(t, a)
	path := "/api/v1/media-songs/00000000-0000-0000-0000-000000000001/accompaniment/00000000-0000-0000-0000-000000000002"
	c.call(http.MethodDelete, path, nil, "", http.StatusUnauthorized)
	c.call(http.MethodPost, "/api/v1/auth/login", map[string]any{"username": "admin", "password": "admin"}, "", http.StatusOK)
	req, err := http.NewRequest(http.MethodDelete, c.origin+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := c.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("delete bypassed CSRF: %d", response.StatusCode)
	}
	// Valid credentials reach the new route and its song/job ownership check.
	c.call(http.MethodDelete, path, nil, "", http.StatusNotFound)
}
