package execution

import (
	"context"
	"fmt"
)

func (s *Service) initializeCacheBudget(ctx context.Context, available func(string) (int64, error)) error {
	if s.cfg.SongCacheBytes <= 0 {
		return nil
	}
	settings, err := s.cacheSettings(ctx)
	if err != nil {
		return err
	}
	// Revision zero identifies the automatic installation default. Any explicit
	// save takes precedence over startup sizing, including after an upgrade.
	if integer(settings["revision"]) != 0 {
		return nil
	}
	free, err := available(s.cfg.SongCachePath)
	if err != nil {
		return fmt.Errorf("detect song cache available space: %w", err)
	}
	budget := s.cfg.SongCacheBytes
	if free < budget {
		free = max(free, 0)
		// Floor 80% using integer arithmetic without overflowing on large volumes.
		budget = free/5*4 + free%5*4/5
	}
	_, err = s.exec(ctx, s.db, "cache-initialize", map[string]any{"bytes": budget})
	return err
}
