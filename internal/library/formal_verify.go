package library

import (
	"context"
	"path"
	"time"

	"github.com/mynora-dot/singverse-go-backend/internal/platform/store"
)

// The persisted PENDING location is the queue. Restarting a process loses no
// work, and a result only applies while the same binding is still current.
func (s *Service) verifyLocationLoop() {
	defer s.wg.Done()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			s.verifyLocations(s.ctx)
		}
	}
}
func (s *Service) verifyLocations(ctx context.Context) {
	rows, e := store.Maps(ctx, s.db, s.q["formal-pending-locations"])
	if e != nil {
		return
	}
	type directoryKey struct{ source, path string }
	directories := make([]directoryKey, 0, len(rows))
	groups := make(map[directoryKey][]map[string]any)
	for _, row := range rows {
		directory := path.Dir(row["relative_path"].(string))
		if directory == "." {
			directory = ""
		}
		key := directoryKey{row["source_id"].(string), directory}
		if _, ok := groups[key]; !ok {
			directories = append(directories, key)
		}
		groups[key] = append(groups[key], row)
	}
	// Reuse one listing for siblings in this batch, then release it before
	// reading another directory. No observation survives into the next batch.
	for _, directory := range directories {
		files, missing, err := s.MediaDirectory(ctx, directory.source, directory.path)
		if ctx.Err() != nil {
			return
		}
		for _, row := range groups[directory] {
			s.verifyLocation(ctx, row, files, missing, err)
		}
	}
}

func (s *Service) verifyLocation(ctx context.Context, row map[string]any, files map[string]*int64, missing bool, err error) {
	id, source, relative := row["id"].(string), row["source_id"].(string), row["relative_path"].(string)
	state := "PRESENT"
	if missing {
		state = "MISSING"
	} else if err != nil {
		state = "UNREACHABLE"
	} else if size, ok := files[relative]; !ok {
		state = "MISSING"
	} else if old, ok := row["file_size"].(int64); ok && size != nil && old != *size {
		state = "MISMATCH"
	}
	_ = s.transaction(ctx, func(tx store.DBTX) error {
		r, e := tx.ExecContext(ctx, s.q["formal-verify-location"], state, id, source, relative)
		if e != nil {
			return e
		}
		n, e := r.RowsAffected()
		if e != nil || n == 0 {
			return e
		}
		if _, e = tx.ExecContext(ctx, s.q["formal-verify-shared"], state, now(), id); e != nil {
			return e
		}
		observation := map[string]any{"verifiedAt": now()}
		if size := files[relative]; size != nil {
			observation["size"] = *size
		}
		if err != nil {
			observation["error"] = "连接或读取目录失败，请检查来源诊断后重试"
		}
		return s.formal.Location(ctx, tx, id, source, relative, "", state, observation)
	})
}
