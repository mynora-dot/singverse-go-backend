package metadata

import "context"

func (s *Service) PlatformSettings(ctx context.Context) (Settings, error) {
	return s.settings(ctx, s.db, false)
}
func (s *Service) SongDetail(ctx context.Context, id string) (map[string]any, error) {
	return s.detail(ctx, s.db, id)
}
