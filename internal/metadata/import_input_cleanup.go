package metadata

import (
	"context"
	"os"
	"path/filepath"
	"time"
)

// cleanupImportInputs releases only the uploaded archive. Once applying and
// baseline commit finish, verification/retry use durable rows and asset refs.
// Unlike history expiry, this must never delete entries, receipts or leases.
func (s *Service) cleanupImportInputs(ctx context.Context) error {
	jobs, e := s.rows(ctx, s.db, "workflow-inputPending", map[string]any{"now": now()})
	if e != nil {
		return e
	}
	for _, job := range jobs {
		if e = ctx.Err(); e != nil {
			return e
		}
		folder, removeErr := s.importFolder(text(job["id"]))
		if removeErr == nil {
			removeErr = os.Remove(filepath.Join(folder, "bundle.zip"))
			if os.IsNotExist(removeErr) {
				removeErr = nil
			}
		}
		result := map[string]any{"id": job["id"], "inputState": "RELEASED", "cleanupError": nil, "cleanupAfter": nil}
		if removeErr != nil {
			// Keep cleanup retry independent of business outcomes; never repeat the
			// import because a file was locked or its directory was unavailable.
			result["inputState"] = "PENDING"
			result["cleanupError"] = "上传资料包未能回收，后台将重试清理"
			result["cleanupAfter"] = time.Now().UTC().Add(time.Minute).Format(time.RFC3339Nano)
		}
		if _, e = s.exec(ctx, s.db, "workflow-inputResult", result); e != nil {
			return e
		}
	}
	return nil
}
