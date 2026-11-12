package execution

import "testing"

func TestAndroid5TargetHonorsClientLimitsAtEveryQuality(t *testing.T) {
	client := map[string]any{
		"family": "android-tv", "video": []any{"h264"}, "audio": []any{"aac", "mp3"},
		"hls": true, "fmp4": false, "max_width": 1280, "max_height": 720,
		"max_fps": 30, "max_bitrate": 4000000, "max_channels": 2, "max_bit_depth": 8,
		"profiles": map[string]any{"h264": []any{"Baseline"}}, "levels": map[string]any{"h264": 31},
	}
	for _, quality := range []string{"auto", "1080p", "720p", "480p"} {
		t.Run(quality, func(t *testing.T) {
			v, err := target(map[string]any{"client": client, "quality": quality, "start": 15.5}, "REALTIME")
			if err != nil {
				t.Fatal(err)
			}
			if integer(v["width"]) > 1280 || integer(v["height"]) > 720 || number(v["fps"]) > 30 || number(v["bitrate"]) > 4000000 {
				t.Fatal("client limit exceeded", v)
			}
			if str(v, "codec") != "h264" || str(v, "profile") != "baseline" || integer(v["level"]) != 31 || boolv(v["fmp4"]) {
				t.Fatal("incompatible codec or transport", v)
			}
			if number(v["start"]) != 15.5 {
				t.Fatal("original timeline offset lost", v)
			}
		})
	}
}
