package metadata

import (
	"bytes"
	"context"
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/mynora-dot/singverse-go-backend/internal/metastandard"
	"github.com/mynora-dot/singverse-go-backend/internal/platform/store"
)

func TestSongCoverImmediatelyReadsReplacement(t *testing.T) {
	f := setup(t)
	_, ids := f.local(t, "A-Song.mkv")
	id := ids[0]
	apply := func(data []byte) {
		t.Helper()
		review := request(t, f.router, "GET", "/api/v1/metadata/songs/"+id+"/review", nil, 200)
		asset := uploadRequest(t, f, id, text(review["fingerprint"]), "image/png", data, 201)
		assetID := text(asset["id"])
		request(t, f.router, "POST", "/api/v1/metadata/songs/"+id+"/review/apply", ReviewApply{RequestID: uuid(), Fingerprint: text(review["fingerprint"]), Version: text(review["version"]), Fields: []string{"cover"}, CoverAssetID: &assetID}, 200)
	}
	old := tinyPNG(t)
	apply(old)
	if _, err := f.s.PersistCover(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	next := append(append([]byte{}, old...), 1)
	apply(next)
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/metadata/songs/"+id+"/cover", nil))
	if w.Code != 200 || !bytes.Equal(w.Body.Bytes(), next) {
		t.Fatalf("replacement was not visible immediately: status=%d old=%t", w.Code, bytes.Equal(w.Body.Bytes(), old))
	}
	page := request(t, f.router, "GET", "/api/v1/media-songs", nil, 200)
	if text(path(array(page["items"])[0].(map[string]any), "metadata", "track", "coverUrl")) == "" {
		t.Fatal("replacement lost from the library projection")
	}
}

func TestSongCoverRetainsPriorArtworkWhenReplacementFails(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	_, ids := f.local(t, "A-Song.mkv")
	id := ids[0]
	song, _, err := f.lib.FormalIDs(ctx, f.db, id)
	if err != nil {
		t.Fatal(err)
	}
	old := tinyPNG(t)
	err = f.s.transaction(ctx, func(tx store.DBTX) error {
		asset, err := f.lib.Formal().PutAsset(ctx, tx, old, "image/png")
		if err != nil {
			return err
		}
		if err = f.lib.Formal().Decide(ctx, tx, song, map[string]any{"cover": metastandard.AssetRef{AssetID: asset.ID}}, metastandard.Origin{Kind: "UPLOAD"}); err != nil {
			return err
		}
		return f.s.enqueueAttachment(ctx, tx, id, "download:replacement", Origin{Kind: "DOWNLOAD"})
	})
	if err != nil {
		t.Fatal(err)
	}
	f.s.SetDownloadCover(func(context.Context, string) (Cover, error) { return Cover{}, errors.New("replacement unavailable") })
	cover, err := f.s.SongCover(ctx, id)
	if err != nil || !bytes.Equal(cover.Bytes, old) {
		t.Fatalf("prior artwork lost while replacement is unavailable: %v", err)
	}
}
