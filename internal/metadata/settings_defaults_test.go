package metadata

import (
	"testing"

	"github.com/mynora-dot/singverse-go-backend/internal/platform/testutil"
)

func TestDefaultMusicPlatforms(t *testing.T) { exerciseDefaultMusicPlatforms(t, setup(t)) }

func TestMySQLDefaultMusicPlatforms(t *testing.T) {
	db, cfg := testutil.MySQLDatabase(t)
	exerciseDefaultMusicPlatforms(t, setupWithDatabase(t, db, "mysql", cfg))
}

func exerciseDefaultMusicPlatforms(t *testing.T, f *fixture) {
	t.Helper()
	settings := request(t, f.router, "GET", "/api/v1/metadata/settings", nil, 200)
	if !boolean(settings["enabled"]) || encode(stringsOf(settings["providers"])) != encode(providers) || boolean(settings["autoApply"]) || settings["threshold"] != .8 || number(settings["minIntervalSeconds"]) != 1 || number(settings["maxIntervalSeconds"]) != 2 {
		t.Fatal("fresh install must enable all platforms without enabling automatic writes", settings)
	}
	input := Settings{Enabled: false, Providers: []string{"QQ"}, AutoApply: true, Threshold: .98, MinIntervalSeconds: ptr(0), MaxIntervalSeconds: ptr(20)}
	request(t, f.router, "PUT", "/api/v1/metadata/settings", input, 200)
	settings = request(t, f.router, "GET", "/api/v1/metadata/settings", nil, 200)
	if boolean(settings["enabled"]) || encode(stringsOf(settings["providers"])) != `["QQ"]` || !boolean(settings["autoApply"]) || number(settings["minIntervalSeconds"]) != 0 || number(settings["maxIntervalSeconds"]) != 20 {
		t.Fatal("reading saved preferences silently restored defaults", settings)
	}
	input.MinIntervalSeconds, input.MaxIntervalSeconds = nil, nil
	settings = request(t, f.router, "PUT", "/api/v1/metadata/settings", input, 200)
	if number(settings["minIntervalSeconds"]) != 1 || number(settings["maxIntervalSeconds"]) != 2 || settings["threshold"] != .98 {
		t.Fatal("omitted delay range must use installation defaults without changing the threshold", settings)
	}
}
