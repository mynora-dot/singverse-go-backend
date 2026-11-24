package metadata

import (
	"archive/zip"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/mynora-dot/singverse-go-backend/internal/metastandard"
	"github.com/mynora-dot/singverse-go-backend/internal/platform/store"
)

const (
	shareArchiveVersion = 5
	shareArchiveName    = "bundle.zip"
	shareArchiveFiles   = 30001
	shareArchiveRecords = 100000
	shareArchivePage    = 12 << 20
)

var shareAssetExtensions = map[string]string{
	"image/jpeg":           ".jpg",
	"image/png":            ".png",
	"image/webp":           ".webp",
	"application/json":     ".json",
	"text/plain":           ".txt",
	"application/x-subrip": ".srt",
	"text/vtt":             ".vtt",
}

func shareAssetName(id, mime string) string {
	return "assets/" + id + shareAssetExtensions[mime]
}

func sharePageName(directory string, number int) string {
	return fmt.Sprintf("%s/%06d.json", directory, number)
}

func (s *Service) exportShareArchive(ctx context.Context, job map[string]any) error {
	if number(job["total_count"]) > shareArchiveRecords {
		return &problem{413, "资料包超过十万条目，请缩小分享范围"}
	}
	folder, e := s.shareFolder(text(job["id"]))
	if e != nil {
		return e
	}
	// The archive is published only after its directory and all entries are durable.
	// Retrying uses the same frozen snapshot and replaces any unfinished ZIP.
	temp := filepath.Join(folder, "bundle.tmp")
	if e = os.Remove(temp); e != nil && !os.IsNotExist(e) {
		return e
	}
	f, e := os.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		return e
	}
	defer os.Remove(temp)
	defer f.Close()

	z := zip.NewWriter(f)
	if e = s.writeShareArchive(ctx, job, z); e != nil {
		return e
	}
	if e = z.Close(); e != nil {
		return e
	}
	st, e := f.Stat()
	if e != nil {
		return e
	}
	if st.Size() > uploadLimit {
		return &problem{413, "资料包超过 8 GiB，请缩小分享范围"}
	}
	if e = f.Sync(); e != nil {
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	if e = os.Rename(temp, filepath.Join(folder, shareArchiveName)); e != nil {
		return e
	}
	dir, e := os.Open(folder)
	if e != nil {
		return e
	}
	e = dir.Sync()
	dir.Close()
	if e != nil {
		return e
	}
	return s.transaction(ctx, func(tx store.DBTX) error {
		return s.shareProgress(ctx, tx, job, "SUCCEEDED", "", number(job["total_count"]), 0, nil)
	})
}

func (s *Service) writeShareArchive(ctx context.Context, job map[string]any, z *zip.Writer) error {
	files := map[string]any{}
	expanded := int64(0)
	modified, _ := time.Parse(time.RFC3339Nano, text(job["created_at"]))
	write := func(name string, method uint16, data []byte, digest string) error {
		if e := ctx.Err(); e != nil {
			return e
		}
		expanded += int64(len(data))
		if expanded > uploadLimit || (name != "index.json" && len(files) >= shareArchiveFiles-1) {
			return &problem{413, "资料包大小或文件数量超过上限，请缩小分享范围"}
		}
		entry, e := z.CreateHeader(&zip.FileHeader{Name: name, Method: method, Modified: modified})
		if e != nil {
			return e
		}
		if _, e = entry.Write(data); e != nil {
			return e
		}
		if name != "index.json" {
			files[name] = map[string]any{"sha256": digest, "size": len(data)}
		}
		return nil
	}
	writeJSON := func(name string, value any) error {
		data, e := metastandard.Canonical([]byte(encode(value)))
		if e != nil {
			return e
		}
		return write(name, zip.Deflate, data, metastandard.Digest(data))
	}
	progress := func(cursor string, processed int64) error {
		return s.transaction(ctx, func(tx store.DBTX) error {
			return s.shareProgress(ctx, tx, job, "RUNNING", cursor, processed, 0, nil)
		})
	}
	if e := progress("", 0); e != nil {
		return e
	}

	assets := map[string]bool{}
	writeAsset := func(id string) error {
		if assets[id] {
			return nil
		}
		asset, data, e := s.library.Formal().ReadAsset(ctx, s.db, id)
		if e != nil {
			return e
		}
		if shareAssetExtensions[asset.MIME] == "" {
			return bad("不支持的资料附件类型")
		}
		method := uint16(zip.Deflate)
		if strings.HasPrefix(asset.MIME, "image/") {
			method = zip.Store
		}
		if e = write(shareAssetName(id, asset.MIME), method, data, id); e != nil {
			return e
		}
		assets[id] = true
		return nil
	}

	records := []string{}
	cursor := ""
	processed := int64(0)
	baseID := text(job["base_id"])
	for {
		rows, e := s.rows(ctx, s.db, "share-page", map[string]any{"id": job["id"], "cursor": cursor, "baseId": nullable(baseID)})
		if e != nil {
			return e
		}
		if len(rows) == 0 {
			break
		}
		entries := make([]map[string]any, 0, len(rows))
		estimated := 2
		for _, row := range rows {
			item, e := portableShareEntry(row)
			if e != nil {
				return e
			}
			cost := len(encode(item)) + 1
			if len(entries) > 0 && estimated+cost > shareArchivePage {
				break
			}
			for _, raw := range array(item["documents"]) {
				doc, e := metastandard.Decode([]byte(encode(raw)))
				if e != nil {
					return e
				}
				for _, id := range metastandard.AssetIDs(doc) {
					if e = writeAsset(id); e != nil {
						return e
					}
				}
			}
			entries = append(entries, item)
			estimated += cost
			cursor = text(row["song_id"])
		}
		name := sharePageName("records", len(records)+1)
		if e = writeJSON(name, entries); e != nil {
			return e
		}
		records = append(records, name)
		processed += int64(len(entries))
		if e = progress(cursor, processed); e != nil {
			return e
		}
	}

	deletions := []string{}
	if e := s.visitShareDeletionPages(ctx, job, func(page int, paths []string) error {
		name := sharePageName("deletions", page)
		deletions = append(deletions, name)
		return writeJSON(name, map[string]any{"advisoryOnly": true, "paths": paths})
	}); e != nil {
		return e
	}
	index := map[string]any{
		"format": "singverse-share-set", "version": shareArchiveVersion, "metadataSchema": "1.0",
		"id": job["id"], "collectionId": job["collection_id"], "collectionRevision": strconv.FormatInt(number(job["collection_revision"]), 10),
		"baseId": baseID, "total": processed, "records": records, "deletions": deletions, "files": files,
	}
	digest, e := metastandard.DocumentDigest([]byte(encode(index)))
	if e != nil {
		return e
	}
	index["contentDigest"] = digest
	return writeJSON("index.json", index)
}
