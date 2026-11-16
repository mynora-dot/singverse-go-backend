package library

import (
	"context"
	"github.com/mynora-dot/singverse-go-backend/internal/platform/store"
)

// Sources is also used by the HTTP list; credentials remain excluded by Source.
func (s *Service) Sources(ctx context.Context) ([]Source, error) {
	out := []Source{}
	rows, e := s.db.QueryxContext(ctx, s.q["source-columns"]+s.q["source-filter"]+s.q["source-order"], "", "%%", "%%", "", "", "", "", 100, 0)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	for rows.Next() {
		v, e := sourceRow(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *Service) Source(ctx context.Context, id string) (Source, error) {
	return s.findSource(ctx, s.db, id)
}
func (s *Service) StartScan(ctx context.Context, id string) (ScanTask, error) {
	return s.startTask(ctx, id)
}
func (s *Service) StartIngest(ctx context.Context, id string) (string, error) {
	var task string
	e := s.transaction(ctx, func(tx store.DBTX) error {
		if _, e := s.findSource(ctx, tx, id); e != nil {
			return e
		}
		var e error
		task, e = s.enqueueIngest(ctx, tx, id)
		return e
	})
	return task, e
}
func (s *Service) PreviewFilename(ctx context.Context, filename string) (Preview, error) {
	st, e := s.state(ctx, s.db)
	if e != nil {
		return Preview{}, e
	}
	cfg, e := configuration(st)
	if e != nil {
		return Preview{}, e
	}
	return Parse(filename, cfg.Rules)
}
