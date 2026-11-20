package downloads

import (
	"fmt"
	"maps"
)

// LX mobile sources use flat types/_types; newer sources use meta.qualitys and
// meta._qualitys. Build both from the same catalogue data, also for persisted
// selections created before the flat fields were supported.
func sourceMusicInfo(track Track) map[string]any {
	music := maps.Clone(track.MusicInfo)
	if music == nil {
		music = map[string]any{}
	}
	meta := maps.Clone(asObject(music["meta"]))
	if meta == nil {
		meta = map[string]any{}
	}
	meta["picUrl"] = track.CoverURL
	music["meta"] = meta
	music["albumName"] = track.Album
	music["albumId"] = meta["albumId"]
	music["img"] = track.CoverURL
	music["types"] = meta["qualitys"]
	music["_types"] = meta["_qualitys"]
	if track.DurationMS != nil {
		music["interval"] = fmt.Sprintf("%d:%02d", *track.DurationMS/60000, *track.DurationMS/1000%60)
	}
	return music
}
