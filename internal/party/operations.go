package party

import (
	"context"

	"github.com/mynora-dot/singverse-go-backend/internal/platform/store"
)

func (s *Service) Devices(ctx context.Context) ([]map[string]any, error) {
	docs, e := s.list(ctx, s.db, "device")
	if e != nil {
		return nil, e
	}
	out := []map[string]any{}
	for _, d := range docs {
		v := map[string]any{}
		for _, k := range []string{"id", "name", "revision", "revoked", "createdAt", "updatedAt", "lastSeenAt"} {
			if value, ok := d[k]; ok {
				v[k] = value
			}
		}
		out = append(out, v)
	}
	return out, nil
}
func (s *Service) RevokeDevice(ctx context.Context, id string) error {
	_, e := s.transact(ctx, func(tx store.DBTX) (any, error) { return nil, s.revoke(ctx, tx, id) })
	return e
}
