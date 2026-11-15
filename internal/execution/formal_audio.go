package execution

import (
	"context"
	"fmt"
	"math"
	"path/filepath"
	"strings"

	"github.com/mynora-dot/singverse-go-backend/internal/metastandard"
	"github.com/mynora-dot/singverse-go-backend/internal/metastore"
	"github.com/mynora-dot/singverse-go-backend/internal/platform/store"
)

func formalAudioEvidence(fields map[string]any) metastore.Evidence {
	return metastore.Evidence{Origin: metastandard.Origin{Kind: "FILE"}, Fields: fields}
}
func (s *Service) saveFormalProbe(ctx context.Context, tx store.DBTX, media string, probe map[string]any) error {
	if len(array(probe["streams"])) == 0 || number(probe["duration"]) <= 0 || number(probe["duration"]) > 604800 {
		return nil
	}
	current, err := s.library.Formal().Read(ctx, tx, media)
	if err != nil {
		return err
	}
	// A roles-only probe cannot replace a complete video/audio observation.
	if prior := array(obj(current.Fields["observation"])["streams"]); len(prior) > len(audioOnly(prior)) && len(array(probe["streams"])) == len(audioOnly(probe["streams"])) {
		return nil
	}
	_, _, err = s.library.Formal().Observe(ctx, tx, media, formalAudioEvidence(map[string]any{"observation": metastandard.NormalizeObservation(map[string]any{"streams": probe["streams"], "duration": probe["duration"]}), "durationMs": int64(math.Round(number(probe["duration"]) * 1000))}))
	return err
}
func audioOnly(streams any) []any {
	out := []any{}
	for _, raw := range array(streams) {
		stream := obj(raw)
		if str(stream, "kind") == "audio" || str(stream, "codec_type") == "audio" {
			out = append(out, raw)
		}
	}
	return out
}

// Scanning retains FFprobe's source fields while compute returns Stream views.
// Compare the same source identity in both representations, without replacing
// a complete video observation with an audio-only probe.
func matchingAudioStreams(first, second any) bool {
	canonical := func(value any) []any {
		out := []any{}
		for _, raw := range audioOnly(value) {
			stream := obj(raw)
			id := str(stream, "id")
			if id == "" && stream["index"] != nil {
				id = fmt.Sprintf("stream:%d", integer(stream["index"]))
			}
			codec := str(stream, "codec")
			if codec == "" {
				codec = str(stream, "codec_name")
			}
			tags := obj(stream["tags"])
			language, title := str(stream, "language"), str(stream, "title")
			if language == "" {
				language = str(tags, "language")
			}
			if language == "" {
				language = "und"
			}
			if title == "" {
				title = str(tags, "title")
			}
			runes := []rune(title)
			if len(runes) > 120 {
				title = string(runes[:120])
			}
			out = append(out, map[string]any{"id": id, "index": integer(stream["index"]), "codec": codec, "channels": integer(stream["channels"]), "language": language, "title": title})
		}
		return out
	}
	a, b := canonical(first), canonical(second)
	return len(a) > 0 && encoded(a) == encoded(b)
}

// Shared observations remain declarations from the exporting receiver. A local
// directory receipt can authorize reuse without pretending that we ran PROBE.
func (s *Service) formalAudioClaim(ctx context.Context, tx store.DBTX, id string) (map[string]any, error) {
	d, err := s.library.Formal().Read(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	original, backing := metastandard.AudioRoleIDs(d.Fields["audioRoles"])
	observed := obj(d.Fields["observation"])
	return map[string]any{"state": "CONFIRMED", "originalId": original, "backingId": backing, "streams": audioOnly(observed["streams"]), "duration": number(d.Fields["durationMs"]) / 1000}, nil
}
func (s *Service) projectFormalRoles(ctx context.Context, db store.DBTX, id string, value map[string]any) error {
	d, err := s.library.Formal().Read(ctx, db, id)
	if err != nil {
		return err
	}
	song, err := s.song(ctx, db, id)
	if err != nil {
		return err
	}
	receipt, err := s.optional(ctx, db, "audio-reuse-context", map[string]any{"song": id})
	if err != nil {
		return err
	}
	projectAudioDocument(song, d, receipt, value)
	return nil
}

func singleAudioSong(relative string) bool {
	switch strings.ToLower(filepath.Ext(relative)) {
	case ".flac", ".mp3", ".wav", ".ogg", ".oga", ".opus", ".aac", ".ape", ".aiff", ".aif", ".wma":
		return true
	}
	return false
}

func audioLocationUsable(song, receipt map[string]any) bool {
	if receipt == nil || str(receipt, "location_state") == "" {
		return true
	}
	return str(receipt, "location_state") == "PRESENT" && str(receipt, "location_source_id") == str(song, "source_id") && str(receipt, "location_path") == str(song, "relative_path")
}

func sharedAudioReceipt(song, receipt map[string]any) bool {
	if !audioLocationUsable(song, receipt) || str(receipt, "shared_verification") != "PRESENT" {
		return false
	}
	proof := decoded(receipt["location_observation"])
	if proof["size"] != nil && song["file_size"] != nil && integer(proof["size"]) != integer(song["file_size"]) {
		return false
	}
	binding := hash(str(song, "location") + "\x00" + str(song, "username") + "\x00" + str(song, "password_encrypted"))
	return str(proof, "verificationBinding") == hash(str(song, "location")) && str(proof, "verificationSourceBinding") == binding && oneOf(str(proof, "verificationLevel"), "PATH_ONLY", "PATH_AND_SIZE")
}

func declaredAudioStreams(observation any) []any {
	streams := audioOnly(obj(observation)["streams"])
	out := make([]any, 0, len(streams))
	for _, raw := range streams {
		stream := copyMap(obj(raw))
		id := str(stream, "id")
		if id == "" && stream["index"] != nil {
			id = fmt.Sprintf("stream:%d", integer(stream["index"]))
		}
		if !audioPattern.MatchString(id) {
			return nil
		}
		stream["id"], stream["kind"] = id, "audio"
		out = append(out, stream)
	}
	return out
}

func projectAudioDocument(song map[string]any, d metastandard.Document, receipt, value map[string]any) {
	delete(value, "originalSelection")
	state, exists := d.States["audioRoles"]
	// An unavailable binding never becomes playable merely because old job results
	// or a shared declaration still contain plausible stream indices.
	if !audioLocationUsable(song, receipt) {
		value["state"] = "UNKNOWN"
		delete(value, "originalId")
		delete(value, "backingId")
		return
	}
	if !exists || oneOf(state.Status, "MISSING", "UNREAD") && state.Decision == "AUTO" {
		if value["streams"] == nil && singleAudioSong(str(song, "relative_path")) {
			value["state"], value["origin"] = "CONFIRMED", "SOURCE"
			delete(value, "originalId")
			delete(value, "backingId")
			value["originalSelection"] = map[string]any{"kind": "UNIQUE_AUDIO"}
			value["receiverProbed"] = false
		}
		return
	}
	value["state"] = "UNKNOWN"
	delete(value, "originalId")
	delete(value, "backingId")
	if state.Status == "CLEARED" || state.Status == "STALE" {
		return
	}
	original, backing := metastandard.AudioRoleIDs(d.Fields["audioRoles"])
	// Reuse the exporting receiver's complete stream declaration only after the
	// receiving root/account was checked. This is distinct from local probing.
	if (state.Origin.Kind == "SHARED" || state.Decision == "SET") && sharedAudioReceipt(song, receipt) {
		streams := declaredAudioStreams(d.Fields["observation"])
		ids := map[string]bool{}
		for _, raw := range streams {
			ids[str(obj(raw), "id")] = true
		}
		duration := number(d.Fields["durationMs"]) / 1000
		if len(streams) == len(ids) && ids[original] && (len(ids) == 1 && backing == "" || len(ids) >= 2 && backing != original && ids[backing]) && duration > 0 && duration <= 604800 {
			// A real receiver probe, if present, must not contradict the package.
			if value["streams"] != nil && (!matchingAudioStreams(streams, value["streams"]) || math.Abs(duration-number(value["duration"])) > .25) {
				return
			}
			value["receiverProbed"] = value["streams"] != nil
			value["state"], value["originalId"], value["backingId"] = "CONFIRMED", original, backing
			value["streams"], value["duration"], value["origin"] = streams, duration, audioRoleOrigin(state)
			value["reuseBasis"] = str(decoded(receipt["location_observation"]), "verificationLevel")
			return
		}
	}
	ids := map[string]bool{}
	for _, stream := range array(value["streams"]) {
		ids[str(obj(stream), "id")] = true
	}
	if !ids[original] || backing != "" && !ids[backing] {
		return
	}
	if state.Decision != "SET" {
		observed := obj(d.Fields["observation"])
		if !matchingAudioStreams(audioOnly(observed["streams"]), value["streams"]) || math.Abs(number(d.Fields["durationMs"])/1000-number(value["duration"])) > .25 {
			return
		}
	}
	value["state"], value["originalId"], value["backingId"] = "CONFIRMED", original, backing
	value["origin"], value["receiverProbed"] = audioRoleOrigin(state), true
}
func (s *Service) bootstrapFormalAudio(ctx context.Context) error {
	cursor := ""
	for {
		rows, err := s.maps(ctx, s.db, "formal-audio-pending", map[string]any{"cursor": cursor})
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return nil
		}
		if err = store.WithinTx(ctx, s.db, func(tx store.DBTX) error {
			for _, row := range rows {
				id := str(row, "id")
				media, err := s.song(ctx, tx, id)
				if err != nil {
					return err
				}
				fp := contentFingerprint(media)
				history, err := s.maps(ctx, tx, "audio-for-song", map[string]any{"song": id, "fingerprint": fp})
				if err != nil {
					return err
				}
				history = currentAudioHistory(media, history)
				for _, job := range history {
					if str(job, "state") == "SUCCEEDED" && len(array(decoded(job["result_json"])["streams"])) > 0 {
						if err = s.saveFormalProbe(ctx, tx, id, decoded(job["result_json"])); err != nil {
							return err
						}
						break
					}
				}
				role, err := s.optional(ctx, tx, "audio-role", map[string]any{"song": id})
				if err != nil {
					return err
				}
				if role != nil && str(role, "fingerprint") == fp {
					current, err := s.library.Formal().Read(ctx, tx, id)
					if err != nil {
						return err
					}
					if _, exists := current.States["audioRoles"]; exists {
						continue
					}
					fields := map[string]any{"audioRoles": metastandard.AudioRoles(str(role, "original_id"), str(role, "backing_id"))}
					if str(role, "origin") == "MANUAL" {
						err = s.library.Formal().Decide(ctx, tx, id, fields, metastandard.Origin{Kind: "MANUAL"})
					} else {
						_, _, err = s.library.Formal().Observe(ctx, tx, id, formalAudioEvidence(fields))
					}
					if err != nil {
						return err
					}
				}
			}
			return nil
		}); err != nil {
			return err
		}
		cursor = str(rows[len(rows)-1], "id")
	}
}

// Receiver observations need their own binding/generation. A pre-session result
// cannot prove its input identity; keep local decisions but require a fresh read
// or verified package before exposing its old stream mapping.
func currentAudioJob(song, job map[string]any) bool {
	return !oneOf(str(job, "kind"), "PROBE", "FAST", "ANALYZE") || str(job, "read_session_id") != "" && str(job, "read_fingerprint") == analysisReadFingerprint(song)
}

func currentAudioHistory(song map[string]any, history []map[string]any) []map[string]any {
	out := make([]map[string]any, 0, len(history))
	for _, job := range history {
		if currentAudioJob(song, job) {
			out = append(out, job)
		}
	}
	return out
}

func audioRoleOrigin(state metastandard.FieldState) string {
	if oneOf(state.Origin.Kind, "SHARED", "MANUAL") {
		return state.Origin.Kind
	}
	return "AUTO"
}
