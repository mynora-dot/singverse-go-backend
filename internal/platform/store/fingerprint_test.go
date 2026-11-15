package store

import (
	"context"
	"testing"

	"github.com/jmoiron/sqlx"
)

func TestQueryFingerprintDistinguishesBoundaries(t *testing.T) {
	db, err := sqlx.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	seen := map[string]string{}
	for _, query := range []string{"SELECT NULL, ''", "SELECT '', NULL", "SELECT 'ab', 'c'", "SELECT 'a', 'bc'", "SELECT '', ''", "SELECT 'a', '' UNION ALL SELECT 'b', ''"} {
		value, err := QueryFingerprint(context.Background(), db, query)
		if err != nil {
			t.Fatal(err)
		}
		if prior, ok := seen[value]; ok {
			t.Fatalf("ambiguous encoding: %s and %s", prior, query)
		}
		seen[value] = query
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = QueryFingerprint(ctx, db, "SELECT 'a'"); err == nil {
		t.Fatal("cancelled query succeeded")
	}
}
