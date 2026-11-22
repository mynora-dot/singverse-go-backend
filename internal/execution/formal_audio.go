package execution

import (
	"context"
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
	return compareAudioStreams(first, second, false)
}

// Legacy technical snapshots omitted some stream labels that the accompanying
// role claim retained. Missing labels in these two package records may match.
func compareAudioStreams(first, second any, allowMissing bool) bool {
	a, b := audioOnly(first), audioOnly(second)
	if len(a) == 0 || len(a) != len(b) {
		return false
	}
	for i, raw := range a {
		left, right := obj(raw), obj(b[i])
		id := metastandard.PackageStreamID(left)
		if id == "" || id != metastandard.PackageStreamID(right) {
			return false
		}
		x, y := comparableStreamFields(left), comparableStreamFields(right)
		for j, v := range x {
			if v != y[j] && !(allowMissing && (v == "" || y[j] == "")) {
				return false
			}
		}
	}
	return true
}

func comparableStreamFields(stream map[string]any) [4]string {
	codec := str(stream, "codec")
	if codec == "" {
		codec = str(stream, "codec_name")
	}
	tags := obj(stream["tags"])
	language, title := str(stream, "language"), str(stream, "title")
	if language == "" {
		language = streamTag(tags, "language")
	}
	if language == "und" {
		language = ""
	}
	if title == "" {
		title = streamTag(tags, "title")
	}
	runes := []rune(title)
	if len(runes) > 120 {
		title = string(runes[:120])
	}
	return [4]string{codec, str(stream, "channels"), language, title}
}

func streamTag(tags map[string]any, name string) string {
	if value := str(tags, name); value != "" {
		return value
	}
	for key := range tags {
		if strings.EqualFold(key, name) {
			return str(tags, key)
		}
	}
	return ""
}

func documentAudioDuration(d metastandard.Document) float64 {
	if oneOf(d.States["durationMs"].Status, "CLEARED", "NOT_APPLICABLE", "STALE") {
		return -1
	}
	duration := number(d.Fields["durationMs"]) / 1000
	observed := number(obj(d.Fields["observation"])["durationMs"]) / 1000
	if duration > 0 && observed > 0 && math.Abs(duration-observed) > .25 {
		return -1
	}
	if duration == 0 {
		return observed
	}
	return duration
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
	return map[string]any{"state": "CONFIRMED", "originalId": original, "backingId": backing, "streams": audioOnly(observed["streams"]), "duration": documentAudioDuration(d)}, nil
}
func (s *Service) projectFormalRoles(ctx context.Context, db store.DBTX, song, value map[string]any, includePackageHint bool) error {
	id := str(song, "id")
	d, err := s.library.Formal().Read(ctx, db, id)
	if err != nil {
		return err
	}
	receipt, err := s.optional(ctx, db, "audio-reuse-context", map[string]any{"song": id})
	if err != nil {
		return err
	}
	if hint := projectAudioDocument(song, d, receipt, value); includePackageHint && hint != nil {
		value["packageRoleHint"] = hint
	}
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
		id := metastandard.PackageStreamID(stream)
		if id == "" {
			return nil
		}
		stream["id"], stream["kind"] = id, "audio"
		out = append(out, stream)
	}
	return out
}

type packageAudioMapping struct {
	original, backing, basis string
	streams                  []any
	duration                 float64
}

func importedAudioMapping(song map[string]any, d metastandard.Document, receipt map[string]any) packageAudioMapping {
	state := d.States["audioRoles"]
	if str(receipt, "shared_verification") == "" || oneOf(state.Decision, "SET", "CLEAR") || oneOf(state.Status, "CLEARED", "STALE", "NOT_APPLICABLE") {
		return packageAudioMapping{}
	}
	roles := d.Fields["audioRoles"]
	if roles != nil && state.Origin.Kind != "SHARED" {
		return packageAudioMapping{}
	}
	observation := d.Fields["observation"]
	observedState := d.States["observation"]
	sharedObservation := observedState.Origin.Kind == "SHARED" && observedState.Status == "PRESENT"
	streams := declaredAudioStreams(observation)
	duration := documentAudioDuration(d)
	if roles != nil {
		o, b, basis := metastandard.PackageAudioRoles(roles, observation)
		if basis == "STREAM_TITLES" && !sharedObservation {
			return packageAudioMapping{}
		}
		return packageAudioMapping{o, b, basis, streams, duration}
	}
	if oneOf(observedState.Status, "CLEARED", "STALE", "NOT_APPLICABLE") {
		return packageAudioMapping{}
	}
	// Older imports may retain the sender's portable claim only in this table.
	// Its fingerprint prevents applying it to a replaced source file.
	legacy := decoded(receipt["claim_json"])
	if str(legacy, "fingerprint") == contentFingerprint(song) {
		claim := obj(legacy["claim"])
		if claim["streams"] == nil && len(streams) > 0 {
			claim = copyMap(claim)
			claim["streams"] = streams
		}
		o, b, basis := metastandard.LegacyPackageAudioRoles(claim)
		claimStreams := declaredAudioStreams(claim)
		claimDuration := number(claim["duration"])
		if len(streams) > 0 && len(claimStreams) > 0 && !compareAudioStreams(streams, claimStreams, true) ||
			duration > 0 && claimDuration > 0 && math.Abs(duration-claimDuration) > .25 {
			return packageAudioMapping{}
		}
		if basis != "" {
			if len(claimStreams) == 0 {
				claimStreams = streams
			}
			if claimDuration <= 0 || duration < 0 {
				claimDuration = duration
			}
			return packageAudioMapping{o, b, basis, claimStreams, claimDuration}
		}
		if str(claim, "originalId") != "" || str(claim, "backingId") != "" {
			return packageAudioMapping{}
		}
	}
	if !sharedObservation {
		return packageAudioMapping{}
	}
	o, b, basis := metastandard.PackageAudioRoles(nil, observation)
	return packageAudioMapping{o, b, basis, streams, duration}
}

func (m packageAudioMapping) conflictsWithProbe(value map[string]any) bool {
	return value["streams"] != nil && (len(m.streams) > 0 && !matchingAudioStreams(m.streams, value["streams"]) ||
		m.duration > 0 && math.Abs(m.duration-number(value["duration"])) > .25)
}

func (m packageAudioMapping) activate(receipt, value map[string]any, origin string) bool {
	if m.duration <= 0 || m.duration > 604800 {
		return false
	}
	ids := map[string]bool{}
	for _, raw := range m.streams {
		ids[str(obj(raw), "id")] = true
	}
	hasRoles := len(ids) == 1 && m.backing == "" || len(ids) > 1 && m.backing != m.original && ids[m.backing]
	if len(ids) != len(m.streams) || !ids[m.original] || !hasRoles {
		return false
	}
	value["receiverProbed"] = value["streams"] != nil
	value["state"], value["originalId"], value["backingId"] = "CONFIRMED", m.original, m.backing
	value["streams"], value["duration"], value["origin"] = m.streams, m.duration, origin
	value["reuseBasis"] = str(decoded(receipt["location_observation"]), "verificationLevel")
	return true
}

func projectAudioDocument(song map[string]any, d metastandard.Document, receipt, value map[string]any) map[string]any {
	delete(value, "originalSelection")
	state, exists := d.States["audioRoles"]
	mapping := importedAudioMapping(song, d, receipt)
	var hint map[string]any
	pending := unverifiedWebDAVImport(song) || receipt["shared_verification"] != nil && str(receipt, "shared_verification") != "PRESENT"
	if mapping.basis != "" && !mapping.conflictsWithProbe(value) {
		verified := !pending && sharedAudioReceipt(song, receipt)
		if verified && mapping.activate(receipt, value, "SHARED") {
			return nil
		}
		verification := str(receipt, "shared_verification")
		if verification == "PRESENT" && !verified {
			verification = "PENDING"
		}
		hint = map[string]any{"basis": mapping.basis, "originalId": mapping.original, "verification": verification}
		if mapping.backing != "" {
			hint["backingId"] = mapping.backing
		}
	}
	// An unavailable binding never becomes playable merely because old job results
	// or a shared declaration still contain plausible stream indices.
	if !audioLocationUsable(song, receipt) || pending {
		value["state"] = "UNKNOWN"
		delete(value, "originalId")
		delete(value, "backingId")
		return hint
	}
	if !exists || oneOf(state.Status, "MISSING", "UNREAD") && state.Decision == "AUTO" {
		if value["streams"] == nil && len(mapping.streams) <= 1 && singleAudioSong(str(song, "relative_path")) {
			value["state"], value["origin"] = "CONFIRMED", "SOURCE"
			delete(value, "originalId")
			delete(value, "backingId")
			value["originalSelection"] = map[string]any{"kind": "UNIQUE_AUDIO"}
			value["receiverProbed"] = false
		}
		if str(value, "state") == "CONFIRMED" {
			return nil
		}
		return hint
	}
	value["state"] = "UNKNOWN"
	delete(value, "originalId")
	delete(value, "backingId")
	if state.Status == "CLEARED" || state.Status == "STALE" {
		return nil
	}
	original, backing := metastandard.AudioRoleIDs(d.Fields["audioRoles"])
	// Reuse the exporting receiver's complete stream declaration only after the
	// receiving root/account was checked. This is distinct from local probing.
	if state.Decision == "SET" && sharedAudioReceipt(song, receipt) {
		manual := packageAudioMapping{original: original, backing: backing, streams: declaredAudioStreams(d.Fields["observation"]), duration: documentAudioDuration(d)}
		if manual.conflictsWithProbe(value) {
			return nil
		}
		if manual.activate(receipt, value, audioRoleOrigin(state)) {
			return nil
		}
	}
	ids := map[string]bool{}
	for _, stream := range array(value["streams"]) {
		ids[str(obj(stream), "id")] = true
	}
	if !ids[original] || backing != "" && !ids[backing] {
		return hint
	}
	if state.Decision != "SET" {
		observed := obj(d.Fields["observation"])
		duration := documentAudioDuration(d)
		invalidPackageRoles := state.Origin.Kind == "SHARED" && receipt["shared_verification"] != nil && mapping.basis == ""
		if invalidPackageRoles || !matchingAudioStreams(observed["streams"], value["streams"]) ||
			duration <= 0 || math.Abs(duration-number(value["duration"])) > .25 {
			return hint
		}
	}
	value["state"], value["originalId"], value["backingId"] = "CONFIRMED", original, backing
	value["origin"], value["receiverProbed"] = audioRoleOrigin(state), true
	return nil
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
