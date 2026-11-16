package execution

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// AudioBatchTarget is the target snapshot used in a confirmation. It contains
// display fields and content identity, never filesystem paths or credentials.
type AudioBatchTarget struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Fingerprint string `json:"fingerprint"`
	Category    string `json:"category"`
}

type AudioBatchPreview struct {
	Kind     string             `json:"kind"`
	Backend  string             `json:"backend"`
	Recipe   map[string]any     `json:"recipe"`
	Targets  []AudioBatchTarget `json:"targets"`
	Revision string             `json:"revision"`
	Page     string             `json:"page"`
}

// PreviewAudioBatch resolves a write confirmation without inserting a batch,
// projecting a task, acquiring the preparation slot or starting a goroutine.
// The caller must re-resolve and compare Revision before executing approval.
// The legacy HTTP preview still prepares an actual batch after this read step.
func (s *Service) PreviewAudioBatch(ctx context.Context, input map[string]any) (AudioBatchPreview, error) {
	selection, err := s.resolveBatchTargets(ctx, input, true)
	if err != nil {
		return AudioBatchPreview{}, err
	}
	return audioBatchPreview(selection)
}
func audioBatchPreview(selection *batchSelection) (AudioBatchPreview, error) {
	preview := AudioBatchPreview{
		Kind: selection.kind, Backend: selection.backend,
		Recipe: selection.spec.view(), Targets: make([]AudioBatchTarget, 0, len(selection.selected)),
		Page: "/library/audio-processing",
	}
	for _, row := range selection.selected {
		preview.Targets = append(preview.Targets, AudioBatchTarget{
			ID: str(row, "id"), Title: str(row, "title"),
			Fingerprint: str(row, "fingerprint"), Category: str(row, "category"),
		})
	}
	b, err := json.Marshal(preview)
	if err != nil {
		return AudioBatchPreview{}, err
	}
	sum := sha256.Sum256(b)
	preview.Revision = hex.EncodeToString(sum[:])
	return preview, nil
}

// CreateConfirmedAudioBatch uses the frozen, freshly resolved selection. The
// receipt ID is also the business ID, allowing read-only reconciliation after a
// crash. Preparation runs after approval; each member checks its fingerprint.
func (s *Service) CreateConfirmedAudioBatch(ctx context.Context, input map[string]any, expected, id string) (map[string]any, error) {
	if prior, e := s.optional(ctx, s.db, "batch-batch", map[string]any{"id": id}); e != nil {
		return nil, e
	} else if prior != nil {
		return s.batchDetail(ctx, id, 0, 50)
	}
	selection, e := s.resolveBatchTargets(ctx, input, true)
	if e != nil {
		return nil, e
	}
	p, e := audioBatchPreview(selection)
	if e != nil {
		return nil, e
	}
	if p.Revision != expected {
		return nil, fail(409, "音频目标或处理方案已变化，请重新确认")
	}
	return s.createAudioBatch(ctx, selection, id, false)
}
