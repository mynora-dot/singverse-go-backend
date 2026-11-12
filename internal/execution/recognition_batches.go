package execution

import (
	"context"
	"errors"

	"github.com/jmoiron/sqlx"
	"github.com/mynora-dot/singverse-go-backend/internal/background"
)

func batchKind(row map[string]any) string {
	if kind := str(row, "kind"); kind != "" {
		return kind
	}
	return "SEPARATE"
}

func recognitionKind(kind string) bool { return oneOf(kind, "FAST", "ANALYZE") }

func recognitionReview(status map[string]any, id string) string {
	for _, value := range array(status["jobs"]) {
		job := obj(value)
		if str(job, "id") != id {
			continue
		}
		if str(job, "resultState") == "CONFIDENT" {
			return "识别已完成，但当前资料未采用建议，请检查音轨标记或手动订正"
		}
		if reason := map[string]string{
			"BOTH_VOCAL_TRACKS":     "两条音轨都检测到明显人声，不能安全认定伴奏，请试听确认",
			"CONFLICTING_WINDOWS":   "不同片段的识别结果不一致，请试听确认",
			"CONFLICTING_LABELS":    "音轨标签与音频分析结果冲突，请试听确认",
			"AMBIGUOUS_VOCALS":      "原唱与伴奏的人声差异不足，请试听确认",
			"NO_RELIABLE_VOCALS":    "采样片段没有足够的可靠人声，请试听确认",
			"FAST_MODEL_MISSING":    "快速人声识别模型未就绪，请准备环境后重试",
			"INSUFFICIENT_DURATION": "歌曲过短，无法取得足够独立片段，请试听确认",
		}[str(job, "reason")]; reason != "" {
			return reason
		}
	}
	return "AI 未能确定原唱与伴奏，请试听后人工确认"
}

func recognitionFailure(job map[string]any) string {
	if str(job, "error") != "AUDIO_EXECUTION_FAILED" {
		return str(job, "error")
	}
	result := decoded(job["result_json"])
	timings := obj(result["timingsMs"])
	if number(timings["extract"]) > 0 && number(timings["separate"]) == 0 && result["inference"] == nil {
		return "音频片段读取或解码失败，请检查媒体来源与文件是否可读"
	}
	return "音频识别执行失败，请查看任务详情并检查计算节点"
}

// Confirmed roles are never candidates for bulk re-identification. Unknown
// stream counts are probed first; only a pair of source tracks can be labelled.
func recognitionCategory(status map[string]any) string {
	if str(status, "state") == "CONFIRMED" && str(status, "backingId") != "" {
		return "READY"
	}
	if status["streams"] == nil {
		return "UNPROBED"
	}
	if len(array(status["streams"])) != 2 {
		return "INAPPLICABLE"
	}
	if str(status, "state") == "CONFIRMED" {
		return "READY"
	}
	for _, value := range array(status["jobs"]) {
		job := obj(value)
		if oneOf(str(job, "kind"), "PROBE", "FAST", "ANALYZE") && oneOf(str(job, "state"), "QUEUED", "RUNNING") {
			return "PROCESSING"
		}
	}
	return "UNCONFIRMED"
}

func (s *Service) advanceRecognitionMember(ctx context.Context, item map[string]any) error {
	song, id, job := str(item, "song_id"), str(item, "batch_id"), str(item, "job_id")
	status, err := s.audioStatus(ctx, song)
	if err != nil {
		return err
	}
	category := recognitionCategory(status)
	if str(item, "state") == "PROCESSING" && job != "" {
		// The job has already finished; uncertain output is a review result, not
		// a successful role assignment. Its original diagnostic remains on the job.
		if category == "READY" {
			return s.updateBatchItem(ctx, item, "SUCCEEDED", job, "")
		}
		return s.updateBatchItem(ctx, item, "REVIEW", job, recognitionReview(status, job))
	}
	if !oneOf(category, "UNPROBED", "UNCONFIRMED") {
		detail := map[string]string{"READY": "已有确认标记，予以保留", "PROCESSING": "已有其他识别任务", "INAPPLICABLE": "仅双音轨支持自动区分原唱与伴奏"}[category]
		return s.updateBatchItem(ctx, item, "SKIPPED", job, detail)
	}
	kind, phase, backend := batchKind(item), "PROCESSING", str(item, "backend")
	if category == "UNPROBED" {
		kind, phase, backend = "PROBE", "PROBING", ""
	}
	result, err := s.submitAudioOrigins(ctx, song, kind, backend, nil, []string{background.ID("AUDIO_BATCH", id)})
	if err != nil {
		var f *failure
		if errors.As(err, &f) && f.status == 404 {
			return s.updateBatchItem(ctx, item, "FAILED", job, "歌曲已删除")
		}
		return s.updateBatchItem(ctx, item, str(item, "state"), job, "等待计算节点或识别环境")
	}
	return s.updateBatchItem(ctx, item, phase, str(result, "id"), "")
}

// Repair historical REVIEW receipts only when the recorded confident result
// matches the current verified roles and unchanged media. Never infer a role
// from an uncertain result or override a local correction/clear.
func (s *Service) recoverRecognitionResults(ctx context.Context) error {
	batchCursor, songCursor, recovered := "", "", int64(0)
	for {
		items, err := s.maps(ctx, s.db, "batch-recognition-recovery", map[string]any{"batch": batchCursor, "song": songCursor})
		if err != nil {
			return err
		}
		if len(items) == 0 {
			break
		}
		for _, item := range items {
			batchCursor, songCursor = str(item, "batch_id"), str(item, "song_id")
			result := decoded(item["result_json"])
			err = s.transaction(ctx, "AUDIO_BATCH", batchCursor, func(tx *sqlx.Tx) error {
				state, detail := str(item, "state"), str(item, "error")
				if state == "REVIEW" {
					status, e := s.audioStatusWith(ctx, tx, songCursor)
					if e != nil {
						var missing *failure
						if errors.As(e, &missing) && missing.status == 404 {
							return nil
						}
						return e
					}
					if str(result, "state") == "CONFIDENT" && str(status, "fingerprint") == str(item, "fingerprint") && str(status, "state") == "CONFIRMED" && str(status, "originalId") == str(result, "original") && str(status, "backingId") == str(result, "backing") {
						state, detail = "SUCCEEDED", ""
					} else {
						detail = recognitionReview(status, str(item, "job_id"))
					}
				} else {
					detail = recognitionFailure(map[string]any{"error": str(item, "job_error"), "result_json": item["result_json"]})
				}
				if state == str(item, "state") && detail == str(item, "error") {
					return nil
				}
				_, e := s.exec(ctx, tx, "batch-repair-recognition-item", map[string]any{"batch": batchCursor, "song": songCursor, "job": str(item, "job_id"), "previous": str(item, "state"), "state": state, "error": detail, "now": now()})
				if e != nil {
					return e
				}
				_, e = s.exec(ctx, tx, "batch-touch", map[string]any{"id": batchCursor, "now": now()})
				return e
			})
			if err != nil {
				return err
			}
			if str(item, "state") == "REVIEW" && str(result, "state") == "CONFIDENT" {
				recovered++
			}
		}
	}
	if recovered > 0 {
		s.log.Info("recognition result receipts reconciled", "candidates", recovered)
	}
	return nil
}
