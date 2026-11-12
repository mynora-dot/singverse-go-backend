package execution

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/mynora-dot/singverse-go-backend/internal/library"
	"github.com/mynora-dot/singverse-go-backend/internal/platform/store"
)

const analysisRangeBytes = int64(1 << 20)

type analysisReadKey struct{}

var analysisContentRange = regexp.MustCompile(`^bytes ([0-9]+)-([0-9]+)/([0-9]+)$`)
var analysisRequestRange = regexp.MustCompile(`^bytes=([0-9]+)-([0-9]+)$`)

func analysisReadFingerprint(song map[string]any) string {
	return digest("analysis-read:v1", playbackFingerprint(song), song["remote_binding"], song["remote_generation"])
}

// validateAnalysisRead is also used in the final result transaction: an old node
// result must not update roles after an account/root rebind or DAV generation change.
func (s *Service) validateAnalysisRead(ctx context.Context, db store.DBTX, job map[string]any) error {
	if !oneOf(str(job, "kind"), "PROBE", "FAST", "ANALYZE") {
		return nil
	}
	if str(job, "read_session_id") == "" {
		return fail(409, "解析输入读取会话不存在")
	}
	state, err := s.one(ctx, db, "analysis-read-find", map[string]any{"id": job["read_session_id"]})
	if err != nil {
		return err
	}
	song, err := s.song(ctx, db, str(job, "song_id"))
	if err != nil {
		return err
	}
	if str(state, "fingerprint") != analysisReadFingerprint(song) {
		return fail(409, "解析输入来源绑定已变化")
	}
	return nil
}

// A session is durable and shared by automatic PROBE -> FAST, but an explicit
// re-analysis gets a new session. Budget reservations survive business restarts.
func (s *Service) bindAnalysisRead(ctx context.Context, tx store.DBTX, job map[string]any, automatic bool) error {
	if !oneOf(str(job, "kind"), "PROBE", "FAST", "ANALYZE") {
		return nil
	}
	song, err := s.song(ctx, tx, str(job, "song_id"))
	if err != nil {
		return err
	}
	id := str(job, "id")
	if automatic && str(job, "kind") == "FAST" {
		history, err := s.maps(ctx, tx, "audio-for-song", map[string]any{"song": job["song_id"], "fingerprint": job["fingerprint"]})
		if err != nil {
			return err
		}
		for _, prior := range history {
			if str(prior, "kind") == "PROBE" && str(prior, "state") == "SUCCEEDED" && str(prior, "read_session_id") != "" && str(prior, "read_fingerprint") == analysisReadFingerprint(song) {
				id = str(prior, "read_session_id")
				break
			}
		}
	}
	maxBytes, maxRequests, maxElapsed := int64(64<<20), int64(128), int64(180000)
	if str(job, "kind") == "ANALYZE" {
		maxBytes, maxRequests, maxElapsed = 128<<20, 256, 300000
	}
	args := map[string]any{"id": id, "fingerprint": analysisReadFingerprint(song), "bytes": maxBytes, "requests": maxRequests, "elapsed": maxElapsed, "now": now()}
	if _, err = s.exec(ctx, tx, "analysis-read-insert", args); err != nil {
		return err
	}
	_, err = s.exec(ctx, tx, "analysis-read-bind", map[string]any{"id": job["id"], "session": id})
	if err == nil {
		job["read_session_id"] = id
	}
	return err
}

func (s *Service) analysisSession(ctx context.Context, job map[string]any) (string, error) {
	if id := str(job, "read_session_id"); id != "" {
		return id, nil
	}
	// Only queued legacy work can establish a new binding snapshot. An already
	// running old node has no provable input binding, so its late result is rejected.
	err := store.WithinTx(ctx, s.db, func(tx store.DBTX) error {
		current, err := s.one(ctx, tx, "audio-job", map[string]any{"id": job["id"]})
		if err != nil {
			return err
		}
		if str(current, "read_session_id") == "" {
			if str(current, "state") != "QUEUED" {
				return fail(409, "旧解析任务缺少来源绑定快照，请重新读取音轨")
			}
			if err = s.bindAnalysisRead(ctx, tx, current, false); err != nil {
				return err
			}
			job["read_session_id"] = current["id"]
		} else {
			job["read_session_id"] = current["read_session_id"]
		}
		return nil
	})
	return str(job, "read_session_id"), err
}

// cachedAnalysisSegment only looks up an existing entry. The reader registration
// and copying of a block are protected by the same mutex as cache eviction.
func (s *Service) cachedAnalysisSegment(ctx context.Context, song map[string]any, fp string, start, end, size int64) ([]byte, bool) {
	if size <= 0 {
		return nil, false
	}
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	entry, err := s.optional(ctx, s.db, "cache-by-key", map[string]any{"key": hash("original:v1:" + str(song, "id") + ":" + fp)})
	if err != nil || entry == nil || str(entry, "state") == "DELETING" {
		return nil, false
	}
	id := str(entry, "id")
	folder, err := s.cacheFolder(id)
	if err != nil {
		return nil, false
	}
	s.cacheReaders[id]++
	defer func() { s.cacheReaders[id]-- }()
	index := start / originalBlockBytes
	data, ok := readOriginalBlock(folder, "original_"+strconv.FormatInt(index, 10)+".block", min(originalBlockBytes, size-index*originalBlockBytes))
	if !ok {
		return nil, false
	}
	from, to := start-index*originalBlockBytes, end-index*originalBlockBytes+1
	if from < 0 || to > int64(len(data)) || to < from {
		return nil, false
	}
	return append([]byte(nil), data[from:to]...), true
}

func (s *Service) analysisOriginal(ctx context.Context, w http.ResponseWriter, r *http.Request, song map[string]any, session string, fetch func(http.ResponseWriter, *http.Request) error, guard func() error) (bool, error) {
	if r.Method != "GET" {
		return true, fail(405, "解析输入仅支持有界 GET")
	}
	raw := r.Header.Get("Range")
	if len(raw) > 100 {
		return true, fail(400, "解析输入范围超限")
	}
	parts := analysisRequestRange.FindStringSubmatch(raw)
	if len(parts) != 3 {
		return true, fail(400, "解析输入仅支持有界字节范围")
	}
	start, e1 := strconv.ParseInt(parts[1], 10, 64)
	end, e2 := strconv.ParseInt(parts[2], 10, 64)
	if e1 != nil || e2 != nil || start < 0 || end < start || end-start >= analysisRangeBytes {
		return true, fail(400, "解析输入范围超限")
	}
	fp := playbackFingerprint(song)
	state, err := s.one(ctx, s.db, "analysis-read-find", map[string]any{"id": session})
	if err != nil {
		return true, err
	}
	if str(state, "fingerprint") != analysisReadFingerprint(song) {
		return true, fail(409, "解析输入来源绑定已变化")
	}
	size := integer(song["file_size"])
	if state["source_size"] != nil {
		size = integer(state["source_size"])
	}
	if size > 0 {
		if start >= size {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", size))
			w.WriteHeader(416)
			return true, nil
		}
		end = min(end, size-1)
	}
	output := make([]byte, 0, end-start+1)
	for position := start; position <= end; {
		if err = guard(); err != nil {
			return true, err
		}
		segmentEnd := end
		if remaining := originalBlockBytes - position%originalBlockBytes; remaining <= end-position {
			segmentEnd = position + remaining - 1
		}
		data, hit := s.cachedAnalysisSegment(ctx, song, fp, position, segmentEnd, size)
		if !hit {
			amount := segmentEnd - position + 1
			allowance := min(int64(20000), integer(state["max_elapsed_ms"])-integer(state["elapsed_ms"]))
			if allowance <= 0 {
				return true, fail(413, "解析输入读取预算已用尽")
			}
			result, e := s.exec(ctx, s.db, "analysis-read-reserve", map[string]any{"id": session, "bytes": amount, "elapsed": allowance, "now": now()})
			if e != nil {
				return true, e
			}
			count, e := result.RowsAffected()
			if e != nil {
				return true, e
			}
			if count != 1 {
				return true, fail(413, "解析输入读取预算已用尽")
			}
			began := time.Now()
			readCtx, cancel := context.WithTimeout(ctx, time.Duration(allowance)*time.Millisecond)
			request := r.Clone(readCtx)
			request.Header = r.Header.Clone()
			request.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", position, segmentEnd))
			request.Header.Del("If-Range")
			capture := &originalCapture{header: make(http.Header), limit: amount}
			e = fetch(capture, request)
			cancel()
			// Reserve the full timeout before reading. A crash keeps the reservation;
			// normal completion refunds unused time, never resets prior phases.
			finalCtx, done := context.WithTimeout(context.Background(), 5*time.Second)
			_, refundErr := s.exec(finalCtx, s.db, "analysis-read-finish", map[string]any{"id": session, "refund": max(int64(0), allowance-time.Since(began).Milliseconds()), "now": now()})
			done()
			if e != nil {
				return true, e
			}
			if refundErr != nil {
				return true, refundErr
			}
			if capture.status != 206 {
				return true, fail(422, "WebDAV 不支持受限范围读取")
			}
			match := analysisContentRange.FindStringSubmatch(capture.header.Get("Content-Range"))
			if match == nil {
				return true, fail(409, "解析输入范围或媒体长度已变化")
			}
			actualStart, startErr := strconv.ParseInt(match[1], 10, 64)
			actualEnd, endErr := strconv.ParseInt(match[2], 10, 64)
			total, sizeErr := strconv.ParseInt(match[3], 10, 64)
			if startErr != nil || endErr != nil || sizeErr != nil || total <= 0 || actualStart != position || actualEnd != min(segmentEnd, total-1) || actualEnd < actualStart || int64(capture.body.Len()) != actualEnd-position+1 || size > 0 && size != total {
				return true, fail(409, "解析输入范围或媒体长度已变化")
			}
			if known := str(song, "content_etag"); known != "" && capture.header.Get("ETag") != known {
				return true, fail(409, "解析输入媒体版本已变化")
			}
			// First observed version is pinned durably, including between PROBE and FAST.
			_, versionErr := s.exec(ctx, s.db, "analysis-read-observe", map[string]any{"id": session, "size": total, "etag": capture.header.Get("ETag"), "modified": capture.header.Get("Last-Modified")})
			if versionErr != nil {
				return true, versionErr
			}
			// MySQL reports zero affected rows when an identical version is
			// observed again. Compare the pinned values, not the update count.
			state, err = s.one(ctx, s.db, "analysis-read-find", map[string]any{"id": session})
			if err != nil {
				return true, err
			}
			if integer(state["source_size"]) != total || str(state, "etag") != capture.header.Get("ETag") || str(state, "modified") != "" && capture.header.Get("Last-Modified") != "" && str(state, "modified") != capture.header.Get("Last-Modified") {
				return true, fail(409, "解析输入媒体版本已变化")
			}
			size = total
			end = min(end, total-1)
			segmentEnd = actualEnd
			data = capture.body.Bytes()
		}
		output = append(output, data...)
		position = segmentEnd + 1
	}
	if err = guard(); err != nil {
		return true, err
	}
	current, err := s.song(ctx, s.db, str(song, "id"))
	if err != nil {
		return true, err
	}
	if analysisReadFingerprint(current) != str(state, "fingerprint") {
		return true, fail(409, "解析输入来源绑定已变化")
	}
	originalHeaders(w, size, start, end, 206)
	w.WriteHeader(206)
	_, err = w.Write(output)
	return true, err
}

func analysisContext(ctx context.Context, session string) context.Context {
	return context.WithValue(library.WithAnalysisRead(ctx), analysisReadKey{}, session)
}
