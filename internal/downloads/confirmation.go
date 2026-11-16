package downloads

import (
	"context"
	"crypto/sha256"
	"encoding/hex"

	"github.com/mynora-dot/singverse-go-backend/internal/platform/search"
	"github.com/mynora-dot/singverse-go-backend/internal/platform/store"
)

// DownloadPreview performs database reads only. No directories, batches, items
// or jobs are created before administrator confirmation.
func (s *Service) DownloadPreview(ctx context.Context, in BatchInput) (map[string]any, error) {
	return s.downloadPreview(ctx, s.db, in)
}
func (s *Service) downloadPreview(ctx context.Context, tx store.DBTX, in BatchInput) (map[string]any, error) {
	if len(in.TrackIDs) < 1 || len(in.TrackIDs) > 500 || search.UTF16Length(in.Name) > 200 {
		return nil, bad("请选择 1～500 首歌曲，批次名称最多 200 字")
	}
	tracks := []Track{}
	for _, id := range in.TrackIDs {
		r, e := s.cached(ctx, tx, id)
		if e != nil {
			return nil, e
		}
		t, e := parseTrack(str(r, "track_json"))
		if e != nil {
			return nil, e
		}
		tracks = append(tracks, t)
	}
	return s.previewTracks(ctx, tx, in, tracks)
}
func (s *Service) previewTracks(ctx context.Context, tx store.DBTX, in BatchInput, selected []Track) (map[string]any, error) {
	if len(in.TrackIDs) < 1 || len(in.TrackIDs) > 500 {
		return nil, bad("请选择 1～500 首歌曲")
	}
	if search.UTF16Length(in.Name) > 200 {
		return nil, bad("批次名称最多 200 字")
	}
	target, script, e := s.requireSource(ctx, tx)
	if e != nil {
		return nil, e
	}
	enabled, e := capabilities(script)
	if e != nil {
		return nil, e
	}
	quality := in.Quality
	if quality == "" {
		quality = "auto"
	}
	if quality != "auto" && !contains(qualities, quality) {
		return nil, bad("音质无效")
	}
	tracks := []map[string]any{}
	seen := map[string]bool{}
	for _, track := range selected {
		if seen[track.ID] {
			return nil, bad("歌曲选择重复")
		}
		seen[track.ID] = true
		if len(enabled[track.Platform]) == 0 || quality != "auto" && !contains(enabled[track.Platform], quality) {
			return nil, bad("所选歌曲或音质不受当前音源支持")
		}
		hash := sha256.Sum256([]byte(encode(track)))
		tracks = append(tracks, map[string]any{"id": track.ID, "title": track.Title, "artists": track.Artists, "album": track.Album, "platform": track.Platform, "selectionRevision": hex.EncodeToString(hash[:])})
	}
	// No script, environment or credential is disclosed in the immutable preview.
	scriptHash := sha256.Sum256([]byte(encode(script)))
	v := map[string]any{"tracks": tracks, "sourceId": target["source_id"], "path": target["source_path"], "scriptId": script["id"], "quality": quality, "name": in.Name, "scriptRevision": hex.EncodeToString(scriptHash[:])}
	hash := sha256.Sum256([]byte(encode(v)))
	v["revision"] = hex.EncodeToString(hash[:])
	return v, nil
}
func (s *Service) RetryPreview(ctx context.Context, id string) (map[string]any, error) {
	in, tracks, e := s.retryInput(ctx, id)
	if e != nil {
		return nil, e
	}
	return s.previewTracks(ctx, s.db, in, tracks)
}
func (s *Service) RetryConfirmed(ctx context.Context, id, expected, receipt string) (map[string]any, error) {
	if row, e := s.row(ctx, s.db, "batch", map[string]any{"id": receipt}); e != nil {
		return nil, e
	} else if row != nil {
		b, e := s.batchView(ctx, s.db, row)
		return map[string]any{"batch": b, "recovered": true}, e
	}
	in, tracks, e := s.retryInput(ctx, id)
	if e != nil {
		return nil, e
	}
	preview, e := s.previewTracks(ctx, s.db, in, tracks)
	if e != nil {
		return nil, e
	}
	if str(preview, "revision") != expected {
		return nil, conflict("重试对象或下载配置已变化，请重新确认")
	}
	if e = s.remember(ctx, tracks); e != nil {
		return nil, e
	}
	return s.createBatch(ctx, in, expected, receipt)
}

func (s *Service) CreateConfirmed(ctx context.Context, in BatchInput, expected, requestID string) (map[string]any, error) {
	return s.createBatch(ctx, in, expected, requestID)
}
func (s *Service) Batch(ctx context.Context, id string) (map[string]any, error) {
	return s.batch(ctx, s.db, id)
}

// ReconcileDownload checks both the receipt's batch and active merged items.
// Absence is evidence for manual review, never permission to create a new batch.
func (s *Service) ReconcileDownload(ctx context.Context, in BatchInput, id string) (map[string]any, error) {
	r, e := s.row(ctx, s.db, "batch", map[string]any{"id": id})
	if e != nil {
		return nil, e
	}
	if r != nil {
		return s.batchView(ctx, s.db, r)
	}
	target, _, e := s.requireSource(ctx, s.db)
	if e != nil {
		return nil, e
	}
	quality := in.Quality
	if quality == "" {
		quality = "auto"
	}
	items := []map[string]any{}
	for _, track := range in.TrackIDs {
		r, e := s.row(ctx, s.db, "activeItem", map[string]any{"activeKey": activeKey(str(target, "source_id"), track, quality)})
		if e != nil {
			return nil, e
		}
		if r != nil {
			items = append(items, map[string]any{"id": r["id"], "batchId": r["batch_id"], "status": r["status"]})
		}
	}
	return map[string]any{"items": items, "message": "已核对回执批次与当前活动下载。未查到的项目请在下载记录核对；不会自动重放"}, nil
}

func (s *Service) ReconcileRetry(ctx context.Context, originalID, receipt string) (map[string]any, error) {
	r, e := s.row(ctx, s.db, "batch", map[string]any{"id": receipt})
	if e != nil {
		return nil, e
	}
	if r != nil {
		return s.batchView(ctx, s.db, r)
	}
	in, _, e := s.retryInput(ctx, originalID)
	if e != nil {
		return nil, e
	}
	return s.ReconcileDownload(ctx, in, receipt)
}
