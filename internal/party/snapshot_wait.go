package party

import (
	"context"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/mynora-dot/singverse-go-backend/internal/platform/store"
)

const maxSnapshotWaitMs = 8000

// Waiters hold neither a transaction nor the party mutex. Subscribe before
// checking the revision so a command committed between the check and wait
// cannot be missed. Signals are published only after a successful commit.
func (s *Service) watchSnapshot(id string) (<-chan struct{}, func()) {
	ch := make(chan struct{})
	s.snapshotMu.Lock()
	if s.waiters == nil {
		s.waiters = map[string]map[chan struct{}]struct{}{}
	}
	if s.waiters[id] == nil {
		s.waiters[id] = map[chan struct{}]struct{}{}
	}
	s.waiters[id][ch] = struct{}{}
	s.snapshotMu.Unlock()
	return ch, func() {
		s.snapshotMu.Lock()
		defer s.snapshotMu.Unlock()
		delete(s.waiters[id], ch)
		if len(s.waiters[id]) == 0 {
			delete(s.waiters, id)
		}
	}
}

func (s *Service) wakeSnapshots(id string) {
	s.snapshotMu.Lock()
	defer s.snapshotMu.Unlock()
	for ch := range s.waiters[id] {
		close(ch)
	}
	delete(s.waiters, id)
}

func (s *Service) readSnapshot(ctx context.Context, tx store.DBTX, actor document, id string, after *int64) (any, error) {
	device := str(actor, "kind") == "device"
	if device {
		if err := s.lock(ctx, tx, id); err != nil {
			return nil, err
		}
	}
	session, err := s.get(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	if err = check(actor, session); err != nil {
		return nil, err
	}
	if after != nil && number(session, "revision") == *after && str(session, "status") == "ACTIVE" {
		return nil, nil
	}
	if device {
		session["tvSeenAt"] = s.now().UTC().Format(time.RFC3339Nano)
		if err = s.save(ctx, tx, session); err != nil {
			return nil, err
		}
	}
	return s.snapshot(session, actor), nil
}

func (s *Service) sessionSnapshotHTTP(c *gin.Context) {
	waitValue, hasWait := c.GetQuery("waitMs")
	afterValue, hasAfter := c.GetQuery("afterStateRevision")
	if !hasWait && !hasAfter {
		s.runAsActor(c, func(ctx context.Context, tx store.DBTX, actor document) (any, error) {
			return s.readSnapshot(ctx, tx, actor, c.Param("id"), nil)
		})
		return
	}
	wait, waitErr := strconv.Atoi(waitValue)
	after, afterErr := strconv.ParseInt(afterValue, 10, 64)
	if !hasWait || !hasAfter || waitErr != nil || afterErr != nil || wait < 1 || wait > maxSnapshotWaitMs || after < 0 {
		s.report(c, fail(400, "快照等待参数无效"))
		return
	}
	c.Header("Cache-Control", "no-store")
	id := c.Param("id")
	changed, release := s.watchSnapshot(id)
	defer release()
	ctx := c.Request.Context()
	value, err := s.transact(ctx, func(tx store.DBTX) (any, error) {
		actor, err := s.actor(ctx, tx, c)
		if err != nil {
			return nil, err
		}
		return s.readSnapshot(ctx, tx, actor, id, &after)
	})
	if err != nil {
		s.report(c, err)
		return
	}
	if value != nil {
		c.JSON(200, value)
		return
	}
	timer := time.NewTimer(time.Duration(wait) * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return
	case <-s.stop:
		s.report(c, fail(503, "服务正在关闭"))
		return
	case <-changed:
	case <-timer.C:
	}
	// Reauthenticate after waiting: a revoked participant/device must never
	// receive a fresh snapshot based on an earlier authorization decision.
	s.runAsActor(c, func(ctx context.Context, tx store.DBTX, actor document) (any, error) {
		return s.readSnapshot(ctx, tx, actor, id, nil)
	})
}
