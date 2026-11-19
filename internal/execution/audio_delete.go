package execution

import (
	"context"
	"errors"
	"os"
	"path/filepath"
)

// Delete only the derived file. Keep the terminal job and its source observations
// so delayed node cleanup can still finish without re-running media processing.
func (s *Service) deleteAudio(ctx context.Context, song, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	row, err := s.requireAudio(ctx, s.db, song, id)
	if err != nil {
		return err
	}
	if str(row, "kind") != "SEPARATE" {
		return fail(422, "只能删除生成的伴奏版本，源音轨和识别记录不能删除")
	}
	if str(row, "state") != "SUCCEEDED" {
		return fail(409, "请等待伴奏生成完成；进行中的任务可先取消")
	}
	status, err := s.audioStatus(ctx, song)
	if err != nil {
		return err
	}
	if str(obj(status["backingResource"]), "jobId") == id {
		return fail(409, "当前使用的伴奏不能删除，请先切换到其他完整伴奏")
	}
	dir, err := os.OpenRoot(s.cfg.AudioDerivedPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer dir.Close()
	err = dir.Remove(filepath.Base(s.audioPath(row)))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
