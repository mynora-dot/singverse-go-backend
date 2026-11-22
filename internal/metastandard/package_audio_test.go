package metastandard

import "testing"

func TestPackageAudioRoles(t *testing.T) {
	audio := func(id int, title string) map[string]any {
		return map[string]any{"index": id, "codec_type": "audio", "tags": map[string]any{"TITLE": title}}
	}
	first, second := audio(2, "伴奏"), audio(7, "原唱（Original Vocals）")
	first["id"] = "0x102"
	probe := func(streams ...any) map[string]any { return map[string]any{"streams": streams} }
	mixed := audio(2, "原唱")
	mixed["title"] = "off vocal"
	badStream := map[string]any{"kind": "audio", "id": "stream:4", "index": 1, "title": "original"}
	video := map[string]any{"kind": "video", "index": 0}
	stereo := audio(2, "原唱/伴奏")
	stereo["channels"] = 2
	mono := audio(2, "原唱/伴奏")
	mono["channels"] = 1
	channels := AudioRoles("stream:2:RIGHT", "stream:2:LEFT")
	partial := append(AudioRoles("stream:2", ""), map[string]any{"streamId": "stream:7", "role": "UNKNOWN"})
	for _, tc := range []struct {
		name                             string
		roles, observation               any
		wantOriginal, wantBacking, basis string
	}{
		{"ffprobe and bilingual labels", nil, probe(video, first, second), "stream:7", "stream:2", "STREAM_TITLES"},
		{"paired English", nil, probe(audio(2, "lead vocals"), audio(7, "Off_Vocal")), "stream:2", "stream:7", "STREAM_TITLES"},
		{"complete explicit roles win", AudioRoles("stream:2", "stream:7"), probe(first, second), "stream:2", "stream:7", "DECLARED"},
		{"consistent partial roles", AudioRoles("stream:7", ""), probe(first, second), "stream:7", "stream:2", "STREAM_TITLES"},
		{"unobserved declared single", AudioRoles("stream:7", ""), nil, "stream:7", "", "DECLARED"},
		{"dual track incomplete roles without titles", partial, nil, "", "", ""},
		{"dual track missing other label", partial, probe(audio(2, "original"), audio(7, "")), "", "", ""},
		{"explicit stereo channels", channels, probe(stereo), "stream:2:RIGHT", "stream:2:LEFT", "DECLARED"},
		{"channel mapping with other streams", channels, probe(stereo, second), "stream:2:RIGHT", "stream:2:LEFT", "DECLARED"},
		{"no channel inference", nil, probe(stereo), "", "", ""},
		{"mono cannot carry channel mapping", channels, probe(mono), "", "", ""},
		{"missing channel count", channels, probe(first), "", "", ""},
		{"unobserved channel declaration", channels, nil, "", "", ""},
		{"unobserved channel base", channels, probe(second), "", "", ""},
		{"overlapping channel and full stream", AudioRoles("stream:2", "stream:2:LEFT"), probe(stereo), "", "", ""},
		{"contradictory partial", AudioRoles("stream:2", ""), probe(first, second), "", "", ""},
		{"absent declared ID", AudioRoles("stream:5", "stream:7"), probe(first, second), "", "", ""},
		{"duplicate role", nil, probe(first, audio(7, "instrumental")), "", "", ""},
		{"duplicated index", nil, probe(first, audio(2, "original")), "", "", ""},
		{"video shares index", nil, probe(map[string]any{"kind": "video", "index": 2}, first, second), "", "", ""},
		{"inconsistent stream id", nil, probe(badStream, first), "", "", ""},
		{"absent index", nil, probe(first, map[string]any{"kind": "audio", "title": "original"}), "", "", ""},
		{"missing title", nil, probe(first, audio(7, "")), "", "", ""},
		{"unclear free text", nil, probe(first, audio(7, "probably vocals?")), "", "", ""},
		{"contradictory title tags", nil, probe(mixed, second), "", "", ""},
		{"compound ambiguity", nil, probe(first, audio(7, "原唱/伴奏")), "", "", ""},
		{"negated label", nil, probe(first, audio(7, "not original")), "", "", ""},
		{"three audio streams", nil, probe(first, second, audio(4, "")), "", "", ""},
		{"single unlabeled role", nil, probe(second), "", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o, b, basis := PackageAudioRoles(tc.roles, tc.observation)
			if o != tc.wantOriginal || b != tc.wantBacking || basis != tc.basis {
				t.Fatalf("got %q, %q, %q", o, b, basis)
			}
		})
	}
	for _, tc := range []struct {
		state, original, backing, basis string
	}{
		{"CONFIRMED", "stream:2", "stream:7", "DECLARED"},
		{"UNKNOWN", "", "", "STREAM_TITLES"},
		{"UNKNOWN", "stream:2", "stream:7", ""},
		{"CONFIRMED", "stream:2", "generated", ""},
	} {
		role := map[string]any{"state": tc.state, "originalId": tc.original, "backingId": tc.backing, "streams": []any{first, second}}
		if _, _, basis := LegacyPackageAudioRoles(role); basis != tc.basis {
			t.Fatalf("legacy %v: %q", tc, basis)
		}
	}
}
