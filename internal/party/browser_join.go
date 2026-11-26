package party

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/mynora-dot/singverse-go-backend/internal/platform/store"
)

func tvOnlineAt(session document, now time.Time) bool {
	seen, err := time.Parse(time.RFC3339Nano, str(session, "tvSeenAt"))
	return err == nil && now.Sub(seen) < 15*time.Second
}

func (s *Service) onlineTVSession(ctx context.Context, tx store.DBTX, device document, now time.Time) (document, error) {
	sid := str(device, "sessionId")
	if sid == "" || boolean(device, "revoked") {
		return nil, nil
	}
	session, err := s.getRecord(ctx, tx, sid)
	if err != nil {
		var missing *failure
		if errors.As(err, &missing) && missing.status == 404 {
			return nil, nil
		}
		return nil, err
	}
	if session.kind != "session" || str(session.doc, "deviceId") != str(device, "id") ||
		str(session.doc, "status") != "ACTIVE" || !tvOnlineAt(session.doc, now) {
		return nil, nil
	}
	return session.doc, nil
}

// These browser routes use the administrator Cookie/CSRF boundary. The issued
// credential is the same session-scoped participant identity as a QR join.
func (s *Service) onlineTelevisionsHTTP(c *gin.Context) {
	s.run(c, func(tx store.DBTX) (any, error) {
		ctx := c.Request.Context()
		devices, err := s.list(ctx, tx, "device")
		if err != nil {
			return nil, err
		}
		items := make([]document, 0, len(devices))
		now := s.now()
		for _, device := range devices {
			session, err := s.onlineTVSession(ctx, tx, device, now)
			if err != nil {
				return nil, err
			}
			if session != nil {
				items = append(items, document{
					"deviceId": device["id"], "deviceName": device["name"], "sessionId": session["id"],
					"acceptingRequests": boolean(session, "acceptingRequests"),
				})
			}
		}
		sort.SliceStable(items, func(i, j int) bool { return str(items[i], "deviceName") < str(items[j], "deviceName") })
		return document{"items": items}, nil
	})
}

func (s *Service) joinTelevisionHTTP(c *gin.Context) {
	input, ok := decode(c)
	if !ok {
		return
	}
	sid, err := text(input, "sessionId", 64)
	if err != nil {
		s.report(c, err)
		return
	}
	s.run(c, func(tx store.DBTX) (any, error) {
		ctx := c.Request.Context()
		deviceID := c.Param("id")
		if err := s.lock(ctx, tx, deviceID); err != nil {
			return nil, err
		}
		device, err := s.getRecord(ctx, tx, deviceID)
		if err != nil {
			return nil, err
		}
		if device.kind != "device" {
			return nil, fail(404, "电视不存在")
		}
		if str(device.doc, "sessionId") != sid {
			return nil, fail(409, "电视已换场，请重新选择")
		}
		if err = s.lock(ctx, tx, sid); err != nil {
			return nil, err
		}
		session, err := s.onlineTVSession(ctx, tx, device.doc, s.now())
		if err != nil {
			return nil, err
		}
		if session == nil {
			return nil, fail(409, "电视已离线或聚会已结束，请重新选择")
		}
		participant, err := s.issueParticipant(ctx, tx, sid, input)
		if err != nil {
			return nil, err
		}
		participant["deviceName"] = device.doc["name"]
		return participant, nil
	})
}
