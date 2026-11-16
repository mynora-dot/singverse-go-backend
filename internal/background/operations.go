package background

import (
	"context"
	"github.com/mynora-dot/singverse-go-backend/internal/platform/store"
)

func (s *Service) Task(ctx context.Context, id string) (map[string]any, error) {
	var out doc
	e := store.WithinTx(ctx, s.db, func(tx store.DBTX) error {
		r, e := s.require(ctx, tx, id)
		if e != nil {
			return e
		}
		out, e = s.view(ctx, tx, r, false)
		return e
	})
	return out, e
}

func (s *Service) TaskItems(ctx context.Context, id string, page, size int) (map[string]any, error) {
	if page < 0 || page > 1000000 || size < 1 || size > 100 {
		return nil, bad("分页参数无效")
	}
	f := doc{"taskId": id, "status": nil, "page": page, "size": size, "offset": int64(page) * int64(size)}
	items := []doc{}
	counts := doc{}
	var total int64
	e := store.WithinTx(ctx, s.db, func(tx store.DBTX) error {
		if _, e := s.require(ctx, tx, id); e != nil {
			return e
		}
		groups, e := s.rows(ctx, tx, "itemStates", f)
		if e != nil {
			return e
		}
		for _, g := range groups {
			counts[text(g, "status")] = number(g, "amount")
		}
		rows, e := s.rows(ctx, tx, "items", f)
		if e != nil {
			return e
		}
		for _, r := range rows {
			items = append(items, doc{"id": r["id"], "songId": r["song_id"], "title": r["title"], "status": r["status"], "message": r["message"], "execution": read(r["execution_json"])})
		}
		q, a, e := s.statement("itemCount", f)
		if e != nil {
			return e
		}
		return tx.QueryRowContext(ctx, q, a...).Scan(&total)
	})
	return doc{"items": items, "counts": counts, "page": page, "size": size, "totalElements": total}, e
}
