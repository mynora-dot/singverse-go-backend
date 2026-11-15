package catalog

import (
	"context"
	"testing"
)

func TestTaskViewPaginationCountsByStatus(t *testing.T) {
	s, _ := fixture(t)
	ctx := context.Background()
	taskID := id()
	if err := s.exec(ctx, s.db, "c_insertTask", taskID, encode(ModelConfig{}), "[]", "[]", "[]", now(), now(), now()); err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"PENDING", "REVIEW", "REVIEW", "APPLIED"} {
		itemID := id()
		songID := id()
		if err := s.exec(ctx, s.db, "c_insertItem", itemID, taskID, songID, encode(Input{SongID: songID}), "v1", 0, now()); err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.ExecContext(ctx, "UPDATE classification_item SET status=? WHERE id=?", status, itemID); err != nil {
			t.Fatal(err)
		}
	}
	r, err := s.requireTask(ctx, s.db, taskID)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		status string
		count  int64
	}{{"", 4}, {"REVIEW", 2}, {"PENDING", 1}, {"FAILED", 0}} {
		t.Run(test.status, func(t *testing.T) {
			view, err := s.taskView(ctx, s.db, r, 0, 0, test.status)
			if err != nil {
				t.Fatal(err)
			}
			if num(view["total"]) != 4 || num(view["totalElements"]) != test.count || num(view["completed"]) != 3 {
				t.Fatalf("incorrect pagination summary: %v", view)
			}
			counts := view["counts"].(map[string]int64)
			if counts["REVIEW"] != 2 || counts["FAILED"] != 0 {
				t.Fatalf("incorrect counts: %v", counts)
			}
		})
	}
}
