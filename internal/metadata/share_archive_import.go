package metadata

import (
	"archive/zip"
	"maps"
	"strings"

	"github.com/mynora-dot/singverse-go-backend/internal/metastandard"
)

type shareArchiveEntry struct {
	file   *zip.File
	digest string
}

func (entry shareArchiveEntry) read() ([]byte, error) {
	size := int64(entry.file.UncompressedSize64)
	data, e := readZipFile(entry.file, size)
	if e != nil {
		return nil, e
	}
	if int64(len(data)) != size || metastandard.Digest(data) != entry.digest {
		return nil, bad("资料包文件的大小或 SHA-256 不匹配")
	}
	return data, nil
}

func shareArchivePages(index map[string]any, directory string) ([]string, error) {
	raw, ok := index[directory].([]any)
	if !ok || len(raw) >= shareArchiveFiles {
		return nil, bad("资料页清单无效")
	}
	pages := make([]string, 0, len(raw))
	for i, name := range raw {
		expected := sharePageName(directory, i+1)
		if name != expected {
			return nil, bad("资料页名称或顺序无效")
		}
		pages = append(pages, expected)
	}
	return pages, nil
}

func visitShareArchive(index map[string]any, indexFile *zip.File, z *zip.Reader, visit func(map[string]any) error, install func(string, string, []byte) error) error {
	if e := onlyKeys(index, "format", "version", "metadataSchema", "id", "collectionId", "collectionRevision", "baseId", "total", "records", "deletions", "files", "contentDigest"); e != nil {
		return e
	}
	if !v4Integer(index["version"], shareArchiveVersion, shareArchiveVersion) || index["metadataSchema"] != "1.0" || !v4Base(index["baseId"]) || !v4Integer(index["total"], 0, shareArchiveRecords) {
		return bad("资料清单的版本、基线或条目总数无效")
	}
	base := map[string]any{
		"version": shareArchiveVersion, "collectionId": index["collectionId"], "collectionRevision": index["collectionRevision"],
		"setId": index["id"], "baseId": index["baseId"], "completeSnapshot": true, "contentDigest": index["contentDigest"], "deletionHints": int64(0),
	}
	if _, _, e := sharedIdentity(base); e != nil {
		return e
	}
	claimed := text(index["contentDigest"])
	delete(index, "contentDigest")
	actual, e := metastandard.DocumentDigest([]byte(encode(index)))
	if e != nil || claimed != actual {
		return bad("资料清单摘要不匹配")
	}
	records, e := shareArchivePages(index, "records")
	if e != nil {
		return e
	}
	if deletions, ok := index["deletions"].([]any); !ok || len(deletions) != 0 {
		return bad("全量资料包的删除提示清单必须为空")
	}

	declared, ok := index["files"].(map[string]any)
	if !ok || len(declared) != len(z.File)-1 || !indexFile.Mode().IsRegular() {
		return bad("资料包文件清单不完整")
	}
	pages := map[string]bool{}
	for _, name := range records {
		pages[name] = true
	}
	files := make(map[string]shareArchiveEntry, len(declared))
	assetTypes := map[string]string{}
	assetNames := []string{}
	expanded := indexFile.UncompressedSize64
	for _, file := range z.File {
		if file == indexFile {
			continue
		}
		spec, ok := declared[file.Name].(map[string]any)
		if !ok || files[file.Name].file != nil || !file.Mode().IsRegular() {
			return bad("资料包包含重复或未知文件")
		}
		if e = onlyKeys(spec, "sha256", "size"); e != nil {
			return e
		}
		digest, ok := spec["sha256"].(string)
		if !ok || !metastandard.SHA256.MatchString(digest) {
			return bad("资料包文件摘要无效")
		}
		maxSize := int64(metastandard.MaxJSON)
		if !pages[file.Name] {
			mime := ""
			for candidate := range shareAssetExtensions {
				if file.Name == shareAssetName(digest, candidate) {
					mime = candidate
					break
				}
			}
			if mime == "" || assetTypes[digest] != "" {
				return bad("附件名称无效或重复")
			}
			assetTypes[digest] = mime
			assetNames = append(assetNames, file.Name)
			maxSize = metastandard.ImageLimit
		}
		if !v4Integer(spec["size"], 1, maxSize) || file.UncompressedSize64 != uint64(number(spec["size"])) {
			return bad("资料包文件大小无效")
		}
		expanded += file.UncompressedSize64
		if expanded > uint64(uploadLimit) {
			return &problem{413, "资料包展开后超过 8 GiB"}
		}
		files[file.Name] = shareArchiveEntry{file: file, digest: digest}
	}
	for name := range pages {
		if files[name].file == nil {
			return bad("资料包缺少清单中的文件")
		}
	}

	for _, name := range assetNames {
		entry := files[name]
		data, e := entry.read()
		if e != nil {
			return e
		}
		mime := assetTypes[entry.digest]
		if _, _, e = metastandard.InspectAsset(data, mime); e != nil {
			return bad(e.Error())
		}
		if !strings.HasPrefix(mime, "image/") {
			if _, e = metastandard.LyricsAsset(mime, data); e != nil {
				return bad(e.Error())
			}
		}
		if install != nil {
			if e = install(text(base["setId"]), mime, data); e != nil {
				return e
			}
		}
	}

	return visitShareRecords(records, files, assetTypes, base, number(index["total"]), visit)
}

func visitShareRecords(pages []string, files map[string]shareArchiveEntry, assets map[string]string, header map[string]any, total int64, visit func(map[string]any) error) error {
	paths := map[string]bool{}
	digests := map[string]string{}
	for _, name := range pages {
		data, e := files[name].read()
		if e != nil {
			return e
		}
		raw, e := metastandard.Parse(data)
		if e != nil {
			return bad(e.Error())
		}
		entries, ok := raw.([]any)
		if !ok || len(entries) == 0 || len(entries) > 5000 || int64(len(paths)+len(entries)) > total {
			return bad("资料页条目数量无效")
		}
		for _, raw := range entries {
			docs, e := unpackPortableEntry(raw)
			if e != nil {
				return e
			}
			relative := text(raw.(map[string]any)["path"])
			if !validPortablePath(relative) || paths[relative] {
				return bad("歌曲路径重复或无效")
			}
			paths[relative] = true
			for _, doc := range docs {
				if e = checkPortableDocumentDigest(digests, doc); e != nil {
					return e
				}
				for _, field := range []string{"cover", "portrait", "lyricsAsset"} {
					ref, ok := doc.Fields[field].(map[string]any)
					if !ok {
						continue
					}
					mime := assets[text(ref["assetId"])]
					if mime == "" || strings.HasPrefix(mime, "image/") != (field != "lyricsAsset") {
						return bad("缺少被引用的附件或附件类型不符")
					}
				}
			}
		}
		doc := maps.Clone(header)
		doc["entries"] = entries
		if e = visit(doc); e != nil {
			return e
		}
	}
	if int64(len(paths)) != total {
		return bad("资料条目总数与清单不符")
	}
	if len(pages) == 0 {
		header["entries"] = []any{}
		return visit(header)
	}
	return nil
}
