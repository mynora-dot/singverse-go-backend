package party

import (
	"context"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/mynora-dot/singverse-go-backend/internal/platform/store"
)

func (s *Service) capabilitiesHTTP(c *gin.Context) {
	input, ok := decode(c)
	if !ok {
		return
	}
	s.runAsActor(c, func(ctx context.Context, tx store.DBTX, actor document) (any, error) {
		if str(actor, "kind") != "device" {
			return nil, fail(403, "仅电视可声明能力")
		}
		if err := s.lock(ctx, tx, c.Param("id")); err != nil {
			return nil, err
		}
		session, err := s.get(ctx, tx, c.Param("id"))
		if err != nil {
			return nil, err
		}
		if err = check(actor, session); err != nil {
			return nil, err
		}
		capabilities := []string{}
		for _, value := range stringList(input["capabilities"]) {
			switch value {
			case "AUTO_ADVANCE", "SEEK", "AUDIO_ROLE", "APPLY_AUDIO_ROLES", "REACTIONS":
				found := false
				for _, old := range capabilities {
					if old == value {
						found = true
					}
				}
				if !found {
					capabilities = append(capabilities, value)
				}
			}
		}
		wasFair := fair(session)
		session["capabilities"] = capabilities
		if supports(session, "AUTO_ADVANCE") {
			session["queuePolicy"] = "FAIR"
			if !wasFair && (len(object(session, "playback")) > 0 || firstQueueItem(array(session, "queue"), "WAITING") != nil) {
				session["manualStopped"] = !boolean(object(session, "playback"), "confirmed")
				if boolean(session, "manualStopped") {
					if err = s.finishActivity(ctx, tx, session, "INTERRUPTED"); err != nil {
						return nil, err
					}
					for _, raw := range array(session, "queue") {
						item := raw.(map[string]any)
						if str(item, "status") == "HANDOFF" || str(item, "status") == "PLAYING" {
							item["status"] = "WAITING"
						}
					}
					delete(session, "playback")
					cancelPendingCommands(session)
				}
				session["queueRevision"] = number(session, "queueRevision") + 1
			}
			scheduleFair(session)
			for _, raw := range array(session, "queue") {
				session["nextSequence"] = max(number(session, "nextSequence"), number(raw.(map[string]any), "sequence"))
			}
		} else {
			session["queuePolicy"] = "LEGACY"
		}
		session["tvSeenAt"] = s.now().UTC().Format(time.RFC3339Nano)
		if err = s.save(ctx, tx, session); err != nil {
			return nil, err
		}
		s.prefetchChanged(session, false, false)
		return s.snapshot(session, actor), nil
	})
}

func (s *Service) reactionHTTP(c *gin.Context) {
	input, ok := decode(c)
	if !ok {
		return
	}
	s.runAsActor(c, func(ctx context.Context, tx store.DBTX, actor document) (any, error) {
		if err := s.lock(ctx, tx, c.Param("id")); err != nil {
			return nil, err
		}
		session, err := s.get(ctx, tx, c.Param("id"))
		if err != nil {
			return nil, err
		}
		if err = check(actor, session); err != nil {
			return nil, err
		}
		if !supports(session, "REACTIONS") {
			return nil, fail(409, "请更新电视版本以使用互动")
		}
		requestID, err := text(input, "requestId", 80)
		if err != nil {
			return nil, err
		}
		kind := str(input, "kind")
		if kind != "LIKE" && kind != "CLAP" && kind != "CHEER" {
			return nil, fail(400, "互动类型无效")
		}
		now := s.now().UnixMilli()
		receipts := object(session, "reactionReceipts")
		for key, value := range receipts {
			if number(value.(map[string]any), "expiresAt") <= now {
				delete(receipts, key)
			}
		}
		key := str(actor, "id") + ":" + requestID
		if raw, exists := receipts[key]; exists {
			old := raw.(map[string]any)
			if str(old, "kind") != kind {
				return nil, fail(409, "请求标识已用于其他互动")
			}
			return document{"cursor": old["cursor"], "accepted": true}, nil
		}
		rates := object(session, "reactionRates")
		for key, raw := range rates {
			if number(raw.(map[string]any), "second") != now/1000 {
				delete(rates, key)
			}
		}
		actorKey := "actor:" + str(actor, "id")
		if number(object(rates, actorKey), "count") >= 3 || number(object(rates, "session"), "count") >= 20 {
			return nil, fail(429, "互动太密集，稍后再送出")
		}
		for _, key := range []string{actorKey, "session"} {
			old := object(rates, key)
			rates[key] = document{"second": now / 1000, "count": number(old, "count") + 1}
		}
		cursor := number(session, "reactionCursor") + 1
		event := document{"cursor": cursor, "kind": kind, "ownerName": actor["name"], "attemptId": object(session, "playback")["attemptId"], "createdAt": now, "expiresAt": now + 3000}
		events := []any{}
		for _, raw := range array(session, "reactions") {
			if number(raw.(map[string]any), "expiresAt") > now {
				events = append(events, raw)
			}
		}
		session["reactions"] = append(events, event)
		receipts[key] = document{"kind": kind, "cursor": cursor, "expiresAt": now + 60000}
		session["reactionReceipts"], session["reactionRates"], session["reactionCursor"] = receipts, rates, cursor
		if err = s.save(ctx, tx, session); err != nil {
			return nil, err
		}
		return document{"cursor": cursor, "accepted": true}, nil
	})
}
