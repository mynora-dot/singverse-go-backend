package background

import (
	"errors"
	"testing"
	"time"

	"github.com/mynora-dot/singverse-go-backend/internal/platform/store"
)

func TestWebDAVScopeRecoveryUsesPublishedObservations(t *testing.T) {
	s := fixture(t)
	ctx := t.Context()
	at := time.Now().UTC().Format(time.RFC3339Nano)
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := s.db.ExecContext(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO media_source(id,name,source_type,location,location_hash,username,created_at,updated_at)
		VALUES('dav','DAV','WEBDAV','https://example.invalid/dav/','root','',?,?)`, at, at)
	exec(`INSERT INTO media_scan_task(id,source_id,status,phase,created_at,total_songs)
		VALUES('scan','dav','RUNNING','SCANNING',?,1)`, at)
	project := func() error {
		return store.WithinTx(ctx, s.db, func(tx store.DBTX) error { return s.Project(ctx, tx, "WEBDAV_SCAN", "scan") })
	}
	if err := project(); err != nil {
		t.Fatal(err)
	}
	// Metadata enrichment can update file_size; execution uses identity_size.
	for _, id := range []string{"observed", "other-scan", "archived"} {
		exec(`INSERT INTO media_song(id,source_id,relative_path,path_hash,file_size,scanned_at,parser_profile_id,parser_revision,result_json)
			VALUES(?,'dav',?,?,999,?,'default',1,'{}')`, id, id+".mkv", id, at)
		scan, archived := "scan", any(nil)
		if id == "other-scan" {
			scan = "newer-scan"
		}
		if id == "archived" {
			archived = at
		}
		exec(`INSERT INTO webdav_resource(song_id,source_id,binding,relative_path,path_hash,task_id,last_seen_at,last_scan_id,identity_size,archived_at)
			VALUES(?,'dav','binding',?,?,?, ?,?,50,?)`, id, id+".mkv", id, scan, at, scan, archived)
	}
	exec(`UPDATE media_scan_task SET status='SUCCEEDED',phase='COMMITTING',finished_at=? WHERE id='scan'`, at)
	rollback := errors.New("publication rolled back")
	if err := store.WithinTx(ctx, s.db, func(tx store.DBTX) error {
		if err := s.Project(ctx, tx, "WEBDAV_SCAN", "scan"); err != nil {
			return err
		}
		return rollback
	}); !errors.Is(err, rollback) {
		t.Fatal(err)
	}
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM background_scan_scope`).Scan(&n); err != nil || n != 0 {
		t.Fatal("scope escaped failed publication", n, err)
	}
	// No staged rows remain, matching the scans stuck in the old release.
	for range 2 {
		if err := s.Maintain(ctx); err != nil {
			t.Fatal(err)
		}
	}
	batch := ID("WEBDAV_SCAN", "scan")
	r, err := s.row(ctx, s.db, "find", doc{"id": batch})
	if err != nil || number(r, "scope_sealed") != 1 || text(r, "batch_status") != "QUEUED" || number(read(r["batch_counts_json"]), "pendingDiscovery") != 1 {
		t.Fatal("historical scan was not recovered", r, err)
	}
	var song, fingerprint string
	if err := s.db.QueryRowContext(ctx, `SELECT song_id,fingerprint FROM background_scan_scope`).Scan(&song, &fingerprint); err != nil || song != "observed" || fingerprint != sha("observed:50:null") {
		t.Fatal("recovery guessed membership or changed the media fingerprint", song, fingerprint, err)
	}
	if err := s.SkipPendingAudio(ctx, "计算节点未配置或已停用"); err != nil {
		t.Fatal(err)
	}
	if err := s.Maintain(ctx); err != nil {
		t.Fatal(err)
	}
	r, err = s.row(ctx, s.db, "find", doc{"id": batch})
	if err != nil || text(r, "batch_status") != "COMPLETED" || number(read(r["batch_counts_json"]), "skipped") != 1 {
		t.Fatal("reconciliation reopened settled work", r, err)
	}
}

func TestWebDAVEmptyAndFailedScansCloseWithoutAudioWork(t *testing.T) {
	for _, status := range []string{"SUCCEEDED", "FAILED", "CANCELLED", "INTERRUPTED"} {
		t.Run(status, func(t *testing.T) {
			s := fixture(t)
			ctx := t.Context()
			at := time.Now().UTC().Format(time.RFC3339Nano)
			if _, err := s.db.ExecContext(ctx, `INSERT INTO media_scan_task(id,source_id,status,phase,created_at)
				VALUES('scan','missing-source','RUNNING','SCANNING',?)`, at); err != nil {
				t.Fatal(err)
			}
			for _, state := range []string{"RUNNING", status} {
				if _, err := s.db.ExecContext(ctx, `UPDATE media_scan_task SET status=? WHERE id='scan'`, state); err != nil {
					t.Fatal(err)
				}
				if err := store.WithinTx(ctx, s.db, func(tx store.DBTX) error { return s.Project(ctx, tx, "WEBDAV_SCAN", "scan") }); err != nil {
					t.Fatal(err)
				}
			}
			r, err := s.row(ctx, s.db, "find", doc{"id": ID("WEBDAV_SCAN", "scan")})
			if err != nil || number(r, "scope_sealed") != 1 || text(r, "batch_status") != normalize(status) || number(read(r["batch_counts_json"]), "pendingDiscovery") != 0 {
				t.Fatal("terminal scan invented pending audio work", r, err)
			}
		})
	}
}
