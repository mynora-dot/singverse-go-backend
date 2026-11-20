package downloads

import (
	"testing"

	"github.com/mynora-dot/singverse-go-backend/internal/downloads/lxruntime"
)

func TestCatalogTrackResolvesWithFlatAndNestedLXSources(t *testing.T) {
	// The LX mobile catalogue exposes types/_types at the top level. Some
	// sources use those hashes directly while others use the nested meta schema.
	for _, schema := range []string{"mobile", "nested"} {
		t.Run(schema, func(t *testing.T) {
			track, err := mapCatalogTrack("kg", catalogRow("kg"))
			if err != nil {
				t.Fatal(err)
			}
			// Simulate an older persisted selection, then pass through the same
			// adapter used when a queued or retried download reaches its source.
			for _, field := range []string{"albumName", "albumId", "img", "types", "_types", "interval"} {
				delete(track.MusicInfo, field)
			}
			script := `
lx.on(lx.EVENT_NAMES.request, ({info}) => {
  const m = info.musicInfo;
  const qualities = ` + map[string]string{"mobile": `m._types`, "nested": `m.meta._qualitys`}[schema] + `;
  if (!qualities || !qualities[info.type] || m.albumName !== '测试专辑' || m.interval !== '3:00') throw new Error('invalid track');
  return Promise.resolve('https://audio.example/' + qualities[info.type].hash + '.flac');
});
lx.send(lx.EVENT_NAMES.inited, {sources: {kg: {type: 'music', actions: ['musicUrl'], qualitys: ['flac']}}});`
			runtime := lxruntime.New("")
			defer runtime.Close()
			address, err := runtime.Resolve(t.Context(), script, "mobile", "kg", "flac", sourceMusicInfo(track))
			if err != nil || address != "https://audio.example/2123456789ABCDEF0123456789ABCDEF.flac" {
				t.Fatal("source received wrong quality hash", address, err)
			}
			if _, exists := track.MusicInfo["_types"]; exists {
				t.Fatal("stored selection was mutated")
			}
		})
	}
}
