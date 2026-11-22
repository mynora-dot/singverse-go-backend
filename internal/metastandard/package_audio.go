package metastandard

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/text/unicode/norm"
)

const MaxLegacyAudioClaimBytes = 16 * 1024

// PackageAudioRoles interprets the exporting library's evidence. The result
// describes its streams and says nothing about access to the receiver's media.
func PackageAudioRoles(roles, observation any) (original, backing, basis string) {
	assigned := map[string]string{}
	if roles != nil {
		if validateAudioRoles(roles) != nil {
			return "", "", ""
		}
		for _, raw := range roles.([]any) {
			item := raw.(map[string]any)
			id, role := item["streamId"].(string), item["role"].(string)
			assigned[id] = role
			switch role {
			case "ORIGINAL":
				original = id
			case "BACKING":
				backing = id
			}
		}
	}

	obs, _ := observation.(map[string]any)
	streams, ok := obs["streams"].([]any)
	if !ok && obs["streams"] != nil {
		return "", "", ""
	}
	if len(streams) == 0 {
		for id := range assigned {
			if strings.Count(id, ":") > 1 {
				return "", "", ""
			}
		}
		if original != "" && (backing != "" || len(assigned) == 1) {
			return original, backing, "DECLARED"
		}
		return "", "", ""
	}
	if len(streams) > 128 {
		return "", "", ""
	}

	audio := map[string]map[string]any{}
	seen := map[string]bool{}
	for _, raw := range streams {
		stream, ok := raw.(map[string]any)
		if !ok {
			return "", "", ""
		}
		kind, _ := stream["kind"].(string)
		rawKind, _ := stream["codec_type"].(string)
		if kind == "" {
			kind = rawKind
		}
		if kind == "" || rawKind != "" && kind != rawKind {
			return "", "", ""
		}
		id := PackageStreamID(stream)
		if seen[id] || id == "" && kind == "audio" {
			return "", "", ""
		}
		if id != "" {
			seen[id] = true
		}
		if kind == "audio" {
			audio[id] = stream
		}
	}
	channelRole := false
	for id := range assigned {
		base := id
		if strings.HasSuffix(id, ":LEFT") || strings.HasSuffix(id, ":RIGHT") {
			base = id[:strings.LastIndexByte(id, ':')]
			if assigned[base] != "" || streamInteger(audio[base]["channels"]) < 2 {
				return "", "", ""
			}
			channelRole = true
		}
		if audio[base] == nil {
			return "", "", ""
		}
	}
	if original != "" && (backing != "" || len(audio) == 1 && !channelRole) {
		return original, backing, "DECLARED"
	}
	if len(audio) != 2 || channelRole {
		return "", "", ""
	}

	for id, stream := range audio {
		role := packageStreamRole(stream)
		if role == "" {
			return "", "", ""
		}
		if declared := assigned[id]; declared != "" && declared != "UNKNOWN" && declared != role {
			return "", "", ""
		}
		switch role {
		case "ORIGINAL":
			original = id
		case "BACKING":
			backing = id
		}
	}
	if original == "" || backing == "" {
		return "", "", ""
	}
	return original, backing, "STREAM_TITLES"
}

// PackageStreamID resolves a source stream from a portable probe or FFprobe record.
func PackageStreamID(stream map[string]any) string {
	id, _ := stream["id"].(string)
	if raw := stream["index"]; raw != nil {
		index := streamInteger(raw)
		if index < 0 || index > 999 {
			return ""
		}
		observed := "stream:" + strconv.Itoa(index)
		// FFprobe's native id can be a container PID. Its index identifies
		// the stream. A SingVerse stream ID must agree with that index.
		if id != "" && id != observed && (stream["kind"] != nil || strings.HasPrefix(id, "stream:")) {
			return ""
		}
		return observed
	}
	if !streamID.MatchString(id) || strings.Count(id, ":") != 1 {
		return ""
	}
	index := strings.TrimPrefix(id, "stream:")
	if len(index) > 1 && index[0] == '0' {
		return ""
	}
	return id
}

func streamInteger(value any) int {
	switch value.(type) {
	case int, int32, int64, float64, json.Number:
		if n, err := strconv.Atoi(fmt.Sprint(value)); err == nil {
			return n
		}
	}
	return -1
}

func packageStreamRole(stream map[string]any) string {
	title, _ := stream["title"].(string)
	role := packageTitleRole(title)
	if title != "" && role == "" {
		return ""
	}
	tags, _ := stream["tags"].(map[string]any)
	for name, raw := range tags {
		tag, ok := raw.(string)
		if !strings.EqualFold(name, "title") || !ok || tag == "" {
			continue
		}
		fromTag := packageTitleRole(tag)
		if fromTag == "" || role != "" && role != fromTag {
			return ""
		}
		role = fromTag
	}
	return role
}

var labelSeparators = strings.NewReplacer("_", " ", "-", " ")

// Match complete track labels; free text, mixed labels, negations and
// unrelated song titles must not become assignments through substring matches.
func packageTitleRole(title string) string {
	if len(title) > 512 {
		return ""
	}
	normalized := strings.ToLower(norm.NFKC.String(title))
	normalized = labelSeparators.Replace(normalized)
	parts := strings.FieldsFunc(normalized, func(r rune) bool { return strings.ContainsRune("()[]【】/|,;", r) })
	result := ""
	for _, part := range parts {
		role := ""
		switch strings.Join(strings.Fields(part), " ") {
		case "原唱", "原声", "原聲", "原唱音轨", "原唱音軌", "原唱版", "人声", "人聲", "主唱",
			"original", "original audio", "original vocal", "original vocals", "vocals", "vocal",
			"lead vocal", "lead vocals", "with vocal", "with vocals", "on vocal":
			role = "ORIGINAL"
		case "伴奏", "伴奏版", "伴奏音轨", "伴奏音軌", "纯伴奏", "純伴奏", "无人声", "無人聲", "纯音乐", "純音樂",
			"backing", "backing track", "instrumental", "instrumental track", "karaoke", "accompaniment",
			"no vocal", "no vocals", "without vocal", "without vocals", "off vocal":
			role = "BACKING"
		default:
			return ""
		}
		if result != "" && result != role {
			return ""
		}
		result = role
	}
	return result
}

// LegacyPackageAudioRoles reads the portable audio status stored by v2/v3.
// A receiver's generated accompaniment is never a source stream.
func LegacyPackageAudioRoles(claim map[string]any) (original, backing, basis string) {
	original, _ = claim["originalId"].(string)
	backing, _ = claim["backingId"].(string)
	var roles any
	if original != "" || backing != "" {
		if claim["state"] != "CONFIRMED" || original != "" && !streamID.MatchString(original) || backing != "" && !streamID.MatchString(backing) {
			return "", "", ""
		}
		roles = AudioRoles(original, backing)
	}
	return PackageAudioRoles(roles, map[string]any{"streams": claim["streams"]})
}
