package store

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strconv"
	"time"
)

// QueryFingerprint hashes ordered rows without retaining their contents. Callers
// must supply a deterministic ORDER BY and the same fields as their projection.
// Length prefixes distinguish NULL, empty values, columns and row boundaries.
func QueryFingerprint(ctx context.Context, db DBTX, query string, args ...any) (string, error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return "", err
	}
	values := make([]any, len(columns))
	targets := make([]any, len(columns))
	for i := range values {
		targets[i] = &values[i]
	}
	digest := sha256.New()
	var size [8]byte
	var prefix [9]byte
	scratch := make([]byte, 0, 128)
	binary.BigEndian.PutUint64(size[:], uint64(len(columns)))
	digest.Write(size[:])
	for rows.Next() {
		if err = rows.Scan(targets...); err != nil {
			return "", err
		}
		for _, value := range values {
			data := scratch[:0]
			switch v := value.(type) {
			case nil:
				prefix[0] = 0
			case string:
				prefix[0] = 1
				data = append(data, v...)
			case []byte:
				prefix[0] = 1
				data = v
			case int64:
				prefix[0] = 2
				data = strconv.AppendInt(data, v, 10)
			case float64:
				prefix[0] = 3
				data = strconv.AppendFloat(data, v, 'g', -1, 64)
			case bool:
				prefix[0] = 4
				data = strconv.AppendBool(data, v)
			case time.Time:
				prefix[0] = 5
				data = v.AppendFormat(data, time.RFC3339Nano)
			default:
				return "", fmt.Errorf("unsupported fingerprint value %T", value)
			}
			binary.BigEndian.PutUint64(prefix[1:], uint64(len(data)))
			digest.Write(prefix[:])
			digest.Write(data)
			if _, ok := value.([]byte); !ok {
				scratch = data[:0]
			}
		}
	}
	if err = rows.Err(); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}
