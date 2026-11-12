package execution

import (
	"errors"
	"testing"

	"github.com/mynora-dot/singverse-go-backend/internal/platform/testutil"
)

func TestCacheStartupBudget(t *testing.T) {
	exerciseCacheStartupBudget(t, executionFixture(t, ""))
}

func TestMySQLCacheStartupBudget(t *testing.T) {
	db, _ := testutil.MySQLDatabase(t)
	exerciseCacheStartupBudget(t, executionFixtureDB(t, "", db, "mysql"))
}

func exerciseCacheStartupBudget(t *testing.T, f *execFixture) {
	t.Helper()
	f.s.cfg.SongCacheBytes = 50 << 30
	for _, tc := range []struct {
		name       string
		free, want int64
	}{
		{"ample space", 80 << 30, 50 << 30},
		{"exactly 50 GiB", 50 << 30, 50 << 30},
		{"insufficient space", 20 << 30, 16 << 30},
		{"byte rounding", 49<<30 - 7, (49<<30 - 7) * 4 / 5},
		{"very small disk", 1 << 20, (1 << 20) * 4 / 5},
		{"full disk", 0, 0},
		{"negative free space", -1, 0},
		{"space recovered on restart", 60 << 30, 50 << 30},
	} {
		t.Run(tc.name, func(t *testing.T) {
			probed := false
			err := f.s.initializeCacheBudget(t.Context(), func(path string) (int64, error) {
				probed = true
				if path != f.s.cfg.SongCachePath {
					t.Fatalf("probed %q instead of the song cache filesystem", path)
				}
				return tc.free, nil
			})
			if err != nil || !probed {
				t.Fatalf("startup sizing failed: probed=%v, err=%v", probed, err)
			}
			settings, err := f.s.cacheSettings(t.Context())
			if err != nil || integer(settings["maxBytes"]) != tc.want || integer(settings["revision"]) != 0 {
				t.Fatalf("startup budget = %v, want %d bytes at revision 0: %v", settings, tc.want, err)
			}
		})
	}
	probeError := errors.New("disk space unavailable")
	if err := f.s.initializeCacheBudget(t.Context(), func(string) (int64, error) { return 0, probeError }); !errors.Is(err, probeError) {
		t.Fatalf("disk probe error was ignored: %v", err)
	}
	saved, err := f.s.saveCacheSettings(t.Context(), map[string]any{
		"maxBytes": int64(128 << 20), "policy": "MANUAL", "ttlHours": int64(48), "revision": int64(0),
		"highWatermarkPercent": int64(80), "lowWatermarkPercent": int64(50),
	})
	if err != nil {
		t.Fatal(err)
	}
	err = f.s.initializeCacheBudget(t.Context(), func(string) (int64, error) {
		t.Error("startup must not resize explicitly saved cache settings")
		return 0, probeError
	})
	if err != nil {
		t.Fatal(err)
	}
	settings, err := f.s.cacheSettings(t.Context())
	if err != nil || encoded(settings) != encoded(saved) {
		t.Fatalf("startup overwrote saved settings: %v, want %v: %v", settings, saved, err)
	}
}
