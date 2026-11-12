package execution

import (
	"context"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/mynora-dot/singverse-go-backend/internal/platform/store"
)

func (s *Service) recordAudioCleanup(ctx context.Context, db store.DBTX, id string, result map[string]any) error {
	state := str(result, "cleanupState")
	if !oneOf(state, "PENDING", "RELEASED") {
		state = ""
	}
	_, err := s.exec(ctx, db, "audio-cleanup-mark", map[string]any{"id": id, "state": state, "next": now() + 30000})
	return err
}

func (s *Service) watchCancelledAudioCleanup(ctx context.Context, id string) error {
	return s.watchAudioCleanupIn(ctx, s.db, id)
}

func (s *Service) watchAudioCleanupIn(ctx context.Context, db store.DBTX, id string) error {
	_, err := s.exec(ctx, db, "audio-cleanup-watch", map[string]any{"id": id, "next": now() + 1000})
	return err
}

// Completion and cleanup have separate lifecycles. Polling cleanup never submits
// media work or reapplies roles, and a missing node cannot change a terminal job.
func (s *Service) pollAudioCleanup(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	rows, err := s.maps(ctx, s.db, "audio-cleanup-pending", map[string]any{"now": now()})
	if err != nil {
		return
	}
	for _, row := range rows {
		if ctx.Err() != nil {
			return
		}
		id := str(row, "id")
		// Reserve the next poll before contacting a node. Errors and restarts
		// therefore retain a bounded retry rate rather than a tight loop.
		if _, err = s.exec(ctx, s.db, "audio-cleanup-defer", map[string]any{"id": id, "next": now() + 30000}); err != nil {
			continue
		}
		connection, e := s.connection(ctx, str(row, "node_origin"))
		if e != nil {
			continue
		}
		state, e := s.call(ctx, connection, "/internal/v1/audio-roles/tasks/"+id, "GET", nil)
		if e != nil || str(state, "id") != id || !oneOf(str(state, "state"), "SUCCEEDED", "FAILED", "CANCELLED") {
			continue
		}
		cleanup := obj(state["result"])
		_ = s.transaction(ctx, "AUDIO_JOB", id, func(tx *sqlx.Tx) error {
			current, e := s.one(ctx, tx, "audio-job", map[string]any{"id": id})
			if e != nil {
				return e
			}
			if !oneOf(str(current, "state"), "SUCCEEDED", "FAILED", "CANCELLED") || !oneOf(str(current, "cleanup_state"), "PENDING", "CHECK") {
				return nil
			}
			result := decoded(current["result_json"])
			if oneOf(str(cleanup, "cleanupState"), "PENDING", "RELEASED") {
				result["cleanupState"] = str(cleanup, "cleanupState")
				result["cleanupError"] = str(cleanup, "cleanupError")
			} else {
				// Older terminal nodes have no cleanup protocol. Stop checking
				// without claiming cleanup succeeded or exposing a guessed state.
				delete(result, "cleanupState")
				delete(result, "cleanupError")
			}
			if _, e = s.exec(ctx, tx, "audio-cleanup-result", map[string]any{"id": id, "result": encoded(result), "now": now()}); e != nil {
				return e
			}
			return s.recordAudioCleanup(ctx, tx, id, result)
		})
	}
}
