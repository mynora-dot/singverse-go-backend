package party

import (
	"context"
	"crypto/rand"
	"math/big"
	"sort"

	"github.com/mynora-dot/singverse-go-backend/internal/platform/store"
)

func fair(session document) bool { return str(session, "queuePolicy") == "FAIR" }
func supports(session document, capability string) bool {
	for _, value := range stringList(session["capabilities"]) {
		if value == capability {
			return true
		}
	}
	return false
}

// Rotation is persistent, including temporarily empty owners. Starting after
// the last normal singer lets a newly arrived person join the current round.
// A temporary priority song never consumes a normal turn.
func scheduleFair(session document) {
	if !fair(session) {
		return
	}
	rotation := stringList(session["rotation"])
	known := map[string]bool{}
	for _, owner := range rotation {
		known[owner] = true
	}
	buckets := map[string][]document{}
	kept := []any{}
	var priority document
	for i, raw := range array(session, "queue") {
		item := raw.(map[string]any)
		item["priority"] = false
		if _, exists := item["sequence"]; !exists {
			item["sequence"] = int64(i + 1)
		}
		if str(item, "status") != "WAITING" {
			kept = append(kept, item)
			continue
		}
		owner := str(item, "ownerId")
		if !known[owner] {
			rotation = append(rotation, owner)
			known[owner] = true
		}
		if str(item, "id") == str(session, "priorityItemId") {
			priority = item
			continue
		}
		buckets[owner] = append(buckets[owner], item)
	}
	for owner := range buckets {
		sort.SliceStable(buckets[owner], func(i, j int) bool {
			return number(buckets[owner][i], "sequence") < number(buckets[owner][j], "sequence")
		})
	}
	start := 0
	for i, owner := range rotation {
		if owner == str(session, "lastServedOwner") {
			start = (i + 1) % len(rotation)
		}
	}
	if priority != nil {
		priority["priority"] = true
		kept = append(kept, priority)
	} else {
		delete(session, "priorityItemId")
	}
	for {
		added := false
		for i := range rotation {
			owner := rotation[(start+i)%len(rotation)]
			if len(buckets[owner]) == 0 {
				continue
			}
			kept = append(kept, buckets[owner][0])
			buckets[owner] = buckets[owner][1:]
			added = true
		}
		if !added {
			break
		}
	}
	session["rotation"], session["queue"] = rotation, kept
}

func changeFairOrder(session, input document, action string) error {
	switch action {
	case "SHUFFLE":
		rotation := stringList(session["rotation"])
		for i := len(rotation) - 1; i > 0; i-- {
			n, err := rand.Int(rand.Reader, big.NewInt(int64(i+1)))
			if err != nil {
				return err
			}
			rotation[i], rotation[n.Int64()] = rotation[n.Int64()], rotation[i]
		}
		session["rotation"] = rotation
		delete(session, "lastServedOwner")
	case "TOP", "UNTOP":
		item, _ := queueItem(array(session, "queue"), str(input, "itemId"))
		if item == nil || str(item, "status") != "WAITING" {
			return fail(409, "只能调整待唱歌曲")
		}
		if action == "TOP" {
			session["priorityItemId"] = item["id"]
		} else if str(session, "priorityItemId") == str(item, "id") {
			delete(session, "priorityItemId")
		}
	default:
		return fail(409, "轮唱模式请使用下一首优先或公平打乱")
	}
	scheduleFair(session)
	return nil
}

func cancelPendingCommands(session document) {
	for _, raw := range array(session, "commandResults") {
		result := raw.(map[string]any)
		if str(result, "status") == "SENT" {
			result["status"], result["detail"] = "FAILED", "播放实例已更换"
		}
	}
	session["commands"] = []any{}
}

func (s *Service) advanceFair(ctx context.Context, tx store.DBTX, session document, skip bool) error {
	if skip {
		if err := s.finishActivity(ctx, tx, session, "SKIPPED"); err != nil {
			return err
		}
		for _, raw := range array(session, "queue") {
			item := raw.(map[string]any)
			if str(item, "status") == "PLAYING" || str(item, "status") == "HANDOFF" {
				item["status"] = "SKIPPED"
			}
		}
	}
	cancelPendingCommands(session)
	scheduleFair(session)
	candidate := firstQueueItem(array(session, "queue"), "WAITING")
	if candidate == nil {
		if p := object(session, "playback"); len(p) > 0 {
			p["state"] = "ENDED"
		}
		return enqueuePlaybackCommand(session, "STOP")
	}
	if str(candidate, "id") == str(session, "priorityItemId") {
		delete(session, "priorityItemId")
	} else {
		session["lastServedOwner"] = candidate["ownerId"]
	}
	candidate["status"], candidate["priority"] = "HANDOFF", false
	session["manualStopped"] = false
	session["playback"] = document{"itemId": candidate["id"], "songId": candidate["songId"], "attemptId": id(), "state": "PREPARING", "confirmed": true, "eventSeq": int64(-1), "positionMs": int64(0), "audioRole": "backing"}
	if err := s.startActivity(ctx, tx, session, candidate); err != nil {
		return err
	}
	scheduleFair(session)
	return enqueuePlaybackCommand(session, "LOAD")
}

func playbackAction(action string) bool {
	switch action {
	case "NEXT", "PAUSE", "RESUME", "RESTART", "STOP", "SEEK", "AUDIO_ROLE", "APPLY_AUDIO_ROLES":
		return true
	}
	return false
}

func (s *Service) fairControl(ctx context.Context, tx store.DBTX, session, input document, action string) error {
	p := object(session, "playback")
	if playbackAction(action) && (str(input, "attemptId") == "" || str(input, "attemptId") != str(p, "attemptId")) {
		return fail(409, "当前歌曲已变化，请核对后重试")
	}
	switch action {
	case "NEXT":
		return s.advanceFair(ctx, tx, session, true)
	case "START":
		state := str(p, "state")
		if !boolean(session, "manualStopped") && (state == "PLAYING" || state == "PREPARING" || state == "BUFFERING" || state == "PAUSED") {
			return fail(409, "当前歌曲尚未结束")
		}
		return s.advanceFair(ctx, tx, session, false)
	case "SEEK", "AUDIO_ROLE", "APPLY_AUDIO_ROLES":
		if !supports(session, action) {
			return fail(409, "请更新电视版本以使用此功能")
		}
		if boolean(session, "manualStopped") || str(p, "state") == "ENDED" {
			return fail(409, "电视当前没有正在播放的歌曲")
		}
	}
	if action == "RESUME" && boolean(session, "manualStopped") {
		return s.advanceFair(ctx, tx, session, false)
	}
	if action == "RESTART" {
		cancelPendingCommands(session)
		session["manualStopped"] = false
		delete(p, "terminal")
	}
	if action == "STOP" {
		cancelPendingCommands(session)
		session["manualStopped"] = true
		p["state"] = "IDLE"
		for _, raw := range array(session, "queue") {
			item := raw.(map[string]any)
			if str(item, "id") == str(p, "itemId") {
				item["status"] = "SKIPPED"
			}
		}
	}
	if action == "SEEK" {
		position, ok := integral(input["positionMs"])
		if !ok || position < 0 || position > 86400000 || (number(p, "durationMs") > 0 && position > number(p, "durationMs")) {
			return fail(400, "定位进度无效")
		}
	}
	if action == "AUDIO_ROLE" && str(input, "role") != "original" && str(input, "role") != "backing" {
		return fail(400, "请选择原唱或伴奏")
	}
	if action == "APPLY_AUDIO_ROLES" && str(input, "songId") != str(p, "songId") {
		return fail(409, "歌曲已切换，标记将在下次播放时生效")
	}
	if action == "PAUSE" || action == "RESUME" || action == "RESTART" || action == "STOP" {
		if err := s.controlQueuePlayback(ctx, tx, session, action); err != nil {
			return err
		}
	} else if err := enqueuePlaybackCommand(session, action); err != nil {
		return err
	}
	commands := array(session, "commands")
	if len(commands) > 0 {
		command := commands[len(commands)-1].(map[string]any)
		for _, key := range []string{"positionMs", "role", "songId"} {
			if value, ok := input[key]; ok {
				command[key] = value
			}
		}
	}
	return nil
}
