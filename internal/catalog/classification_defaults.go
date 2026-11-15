package catalog

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"

	"github.com/mynora-dot/singverse-go-backend/internal/platform/store"
)

//go:embed assets/classification/*.jpg assets/classification/defaults.json
var classificationImages embed.FS

type classificationDefault struct {
	ID        string `json:"id"`
	Dimension string `json:"dimension"`
	Name      string `json:"name"`
}

func classificationDefaults() ([]classificationDefault, error) {
	data, err := classificationImages.ReadFile("assets/classification/defaults.json")
	if err != nil {
		return nil, err
	}
	var defaults []classificationDefault
	err = json.Unmarshal(data, &defaults)
	return defaults, err
}

// Called inside the startup transaction while the classification lock is held.
// Remember the completed import separately from tag revisions: cover changes do
// not invalidate classification snapshots, and deleted covers stay deleted.
func (s *Service) seedClassificationCovers(ctx context.Context, tx store.DBTX) error {
	var initialized int
	if err := tx.QueryRowContext(ctx, s.q["c_coverDefaultsState"]).Scan(&initialized); err != nil {
		return err
	}
	if initialized != 0 {
		return nil
	}
	defaults, err := classificationDefaults()
	if err != nil {
		return fmt.Errorf("read built-in classification vocabulary: %w", err)
	}
	tags, err := s.tags(ctx, tx)
	if err != nil {
		return err
	}
	byID := make(map[string]Tag, len(tags))
	for _, tag := range tags {
		byID[tag.ID] = tag
	}
	for _, preset := range defaults {
		tag, exists := byID[preset.ID]
		// An upgrade only fills untouched default tags. Never restore missing
		// tags or replace artwork, renamed labels, aliases or disabled labels.
		if !exists || tag.CoverPath != nil || tag.Revision != 1 || !tag.Active || tag.Dimension != preset.Dimension || tag.Name != preset.Name {
			continue
		}
		data, err := classificationImages.ReadFile("assets/classification/" + preset.ID + ".jpg")
		if err != nil {
			return fmt.Errorf("read built-in classification cover %s: %w", preset.ID, err)
		}
		if err = s.exec(ctx, tx, "c_insertCover", preset.ID, sha(string(data)), data); err != nil {
			return err
		}
	}
	return s.exec(ctx, tx, "c_coverDefaultsInitialized")
}
