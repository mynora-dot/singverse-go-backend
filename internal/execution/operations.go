package execution

import "context"

func (s *Service) AudioBatches(ctx context.Context, page, size int) (map[string]any, error) {
	return s.batches(ctx, page, size, "")
}
func (s *Service) AudioBatch(ctx context.Context, id string) (map[string]any, error) {
	return s.batchDetail(ctx, id, 0, 50)
}
func (s *Service) CancelAudioBatch(ctx context.Context, id string) (map[string]any, error) {
	return s.cancelBatch(ctx, id)
}
func (s *Service) CacheSettings(ctx context.Context) (map[string]any, error) {
	return s.cacheSettings(ctx)
}
func (s *Service) OfflineTranscodes(ctx context.Context) ([]any, error) {
	return s.offlineTranscodes(ctx)
}
