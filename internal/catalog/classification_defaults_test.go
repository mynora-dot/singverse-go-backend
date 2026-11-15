package catalog

import (
	"bytes"
	"context"
	"image"
	"testing"

	"github.com/jmoiron/sqlx"
	"github.com/mynora-dot/singverse-go-backend/internal/platform/store"
	"github.com/mynora-dot/singverse-go-backend/internal/platform/testutil"
)

func TestBuiltInClassificationCovers(t *testing.T) {
	s, router := fixture(t) // Exercise the real startup path on a fresh database.
	ctx := context.Background()
	presets, err := classificationDefaults()
	if err != nil || len(presets) != 34 {
		t.Fatalf("default vocabulary: %d %v", len(presets), err)
	}
	tags := classificationTags(t, s)
	if len(tags) != len(presets) {
		t.Fatalf("default vocabulary does not match the installation seeds: %d", len(tags))
	}
	seen := map[string]bool{}
	for _, preset := range presets {
		tag := tags[preset.ID]
		if tag.ID != preset.ID || tag.Name != preset.Name || tag.Dimension != preset.Dimension || !tag.Active || tag.Revision != 1 || len(tag.Aliases) != 0 || tag.Count != 0 {
			t.Fatalf("default tag differs from the installation seed: %+v", tag)
		}
		data, err := classificationImages.ReadFile("assets/classification/" + preset.ID + ".jpg")
		if err != nil {
			t.Fatal(err)
		}
		cfg, kind, err := image.DecodeConfig(bytes.NewReader(data))
		if err != nil || kind != "jpeg" || cfg.Width != 1280 || cfg.Height != 720 || len(data) > 2000000 {
			t.Fatalf("invalid default image %s: %+v %s %v", preset.ID, cfg, kind, err)
		}
		if tag.CoverPath == nil || *tag.CoverPath != "/api/v1/classification/tags/"+preset.ID+"/cover" || tag.CoverVersion == nil || *tag.CoverVersion != sha(string(data)) || seen[*tag.CoverVersion] {
			t.Fatalf("missing, duplicated or incorrect default cover: %+v", tag)
		}
		seen[*tag.CoverVersion] = true
		assertJPEG(t, router, *tag.CoverPath, data)
	}
	before := encode(tags)
	if err := s.recover(ctx); err != nil {
		t.Fatal(err)
	}
	if encode(classificationTags(t, s)) != before {
		t.Fatal("restart changed initialized vocabulary or cover versions")
	}
	// Both replacing and deliberately removing a built-in cover survive restart.
	replacement, _ := classificationImages.ReadFile("assets/classification/scene-1.jpg")
	first := tags["genre-0"]
	if _, err := s.tagCover(ctx, first.ID, *first.CoverVersion, replacement, false); err != nil {
		t.Fatal(err)
	}
	removed := tags["mood-0"]
	if _, err := s.tagCover(ctx, removed.ID, *removed.CoverVersion, nil, true); err != nil {
		t.Fatal(err)
	}
	request(t, router, "GET", *removed.CoverPath, nil, 404)
	if _, err := s.saveTag(ctx, "", SaveTag{"CUSTOM", "我的分类", []string{}, true, 0}); err != nil {
		t.Fatal(err)
	}
	before = encode(classificationTags(t, s))
	if err := s.recover(ctx); err != nil {
		t.Fatal(err)
	}
	if encode(classificationTags(t, s)) != before {
		t.Fatal("restart restored a removed cover or changed user artwork")
	}
	assertJPEG(t, router, *first.CoverPath, replacement)
}

func TestClassificationCoverUpgradePreservesEdits(t *testing.T) {
	s, _ := fixture(t)
	exerciseClassificationCoverUpgrade(t, s)
}

func TestMySQLClassificationCoverDefaults(t *testing.T) {
	db, _ := testutil.MySQLDatabase(t)
	q, err := store.ReadQueries(files, "sql")
	if err != nil {
		t.Fatal(err)
	}
	s := &Service{db: db, q: q}
	seedClassificationCovers(t, s)
	for _, tag := range classificationTags(t, s) {
		if tag.CoverPath == nil || tag.CoverVersion == nil {
			t.Fatalf("MySQL default cover missing: %+v", tag)
		}
	}
	exerciseClassificationCoverUpgrade(t, s)
}

func exerciseClassificationCoverUpgrade(t *testing.T, s *Service) {
	t.Helper()
	ctx := context.Background()
	// Recreate the state immediately after upgrading an older installation.
	resetClassificationCovers(t, s.db)
	for tagID, input := range map[string]SaveTag{
		"language-0": {"LANGUAGE", "我的国语", []string{}, true, 1},
		"language-1": {"LANGUAGE", "粤语", []string{"广东话"}, true, 1},
		"language-2": {"LANGUAGE", "英语", []string{}, false, 1},
	} {
		if _, err := s.saveTag(ctx, tagID, input); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.saveTag(ctx, "", SaveTag{"CUSTOM", "用户新增", []string{}, true, 0}); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		"DELETE FROM classification_tag WHERE id='language-3'",
		"UPDATE classification_tag SET dimension='CUSTOM' WHERE id='language-4'",
		"UPDATE classification_tag SET name='旧库自定名称' WHERE id='language-5'",
	} {
		if _, err := s.db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	upload, _ := classificationImages.ReadFile("assets/classification/scene-0.jpg")
	if _, err := s.tagCover(ctx, "genre-0", "", upload, false); err != nil {
		t.Fatal(err)
	}
	before := classificationTags(t, s)
	seedClassificationCovers(t, s)
	after := classificationTags(t, s)
	if len(after) != len(before) || after["language-3"].ID != "" {
		t.Fatal("upgrade restored deleted tags or changed the vocabulary size")
	}
	for id, tag := range after {
		prior := before[id]
		if prior.Revision != tag.Revision || prior.Active != tag.Active || prior.Name != tag.Name || prior.Dimension != tag.Dimension || encode(prior.Aliases) != encode(tag.Aliases) {
			t.Fatalf("upgrade changed a user label: %+v", tag)
		}
		expectCover := true
		switch id {
		case "language-0", "language-1", "language-2", "language-4", "language-5":
			expectCover = false
		default:
			if tag.Name == "用户新增" {
				expectCover = false
			}
		}
		if (tag.CoverPath != nil) != expectCover {
			t.Fatalf("upgrade applied a cover to the wrong label: %+v", tag)
		}
	}
	if *after["genre-0"].CoverVersion != sha(string(upload)) {
		t.Fatal("upgrade replaced an uploaded cover")
	}
	beforeRestart := encode(after)
	seedClassificationCovers(t, s)
	if encode(classificationTags(t, s)) != beforeRestart {
		t.Fatal("repeated upgrade changed covers")
	}
}

func TestClassificationCoverSeedRollbackAndRetry(t *testing.T) {
	s, _ := fixture(t)
	resetClassificationCovers(t, s.db)
	if _, err := s.db.Exec("CREATE TRIGGER reject_default_cover BEFORE INSERT ON classification_tag_cover WHEN NEW.tag_id='decade-1' BEGIN SELECT RAISE(ABORT, 'cover test failure'); END"); err != nil {
		t.Fatal(err)
	}
	if err := s.recover(context.Background()); err == nil {
		t.Fatal("initialization failure ignored")
	}
	var covers, initialized int
	if err := s.db.Get(&covers, "SELECT COUNT(*) FROM classification_tag_cover"); err != nil {
		t.Fatal(err)
	}
	if err := s.db.Get(&initialized, s.q["c_coverDefaultsState"]); err != nil {
		t.Fatal(err)
	}
	if covers != 0 || initialized != 0 {
		t.Fatal("failed initialization left a partial import", covers, initialized)
	}
	if _, err := s.db.Exec("DROP TRIGGER reject_default_cover"); err != nil {
		t.Fatal(err)
	}
	if err := s.recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := s.db.Get(&covers, "SELECT COUNT(*) FROM classification_tag_cover"); err != nil || covers != 34 {
		t.Fatal("initialization did not retry after rollback", covers, err)
	}
}

func classificationTags(t *testing.T, s *Service) map[string]Tag {
	t.Helper()
	tags, err := s.tags(context.Background(), s.db)
	if err != nil {
		t.Fatal(err)
	}
	out := make(map[string]Tag, len(tags))
	for _, tag := range tags {
		out[tag.ID] = tag
	}
	return out
}

func seedClassificationCovers(t *testing.T, s *Service) {
	t.Helper()
	ctx := context.Background()
	if err := store.WithinTx(ctx, s.db, func(tx store.DBTX) error {
		if err := s.lock(ctx, tx); err != nil {
			return err
		}
		return s.seedClassificationCovers(ctx, tx)
	}); err != nil {
		t.Fatal(err)
	}
}

func resetClassificationCovers(t *testing.T, db *sqlx.DB) {
	t.Helper()
	for _, query := range []string{"DELETE FROM classification_tag_cover", "UPDATE classification_settings SET builtin_covers_initialized=0 WHERE id=1"} {
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
}
