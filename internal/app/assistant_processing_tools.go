package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/schema"
	"github.com/mynora-dot/singverse-go-backend/internal/assistant"
	"github.com/mynora-dot/singverse-go-backend/internal/background"
	"github.com/mynora-dot/singverse-go-backend/internal/platform/search"
)

// Only the owning business service controls a task. The generic task service
// remains a read-only projection; it never dispatches SQL or arbitrary actions.
func assistantProcessingTools(v *applicationServices) []assistant.Definition {
	p := v.processing
	tools := []assistant.Definition{
		writeTool("audio_batch_create", "创建并启动音频批次", "整理", "/library/audio-processing", "为指定歌曲创建识别或伴奏批次；保留原始文件，按曲检查并报告部分失败。计算节点不可用时保留批次供业务页面继续", toolParameters{"songIds": listParam("真实歌曲标识，1～500 首", true), "kind": enumParam("FAST 快速识别、ANALYZE 深度识别、SEPARATE 生成伴奏", true, "FAST", "ANALYZE", "SEPARATE"), "backend": enumParam("计算引擎，默认 auto", false, "auto", "cuda", "rocm", "mps", "openvino", "rknn", "tensorrt", "migraphx", "coreml", "directml", "mlx")}, func(ctx context.Context, a toolArgs) (any, any, error) {
			preview, e := p.PreviewAudioBatch(ctx, audioInput(a))
			return preview, map[string]any{"kind": preview.Kind, "backend": preview.Backend, "count": len(preview.Targets)}, e
		}, func(ctx context.Context, a toolArgs, id string, preview assistant.Preview) (any, error) {
			targets, _ := preview.Targets.(map[string]any)
			result, e := p.CreateConfirmedAudioBatch(ctx, audioInput(a), fmt.Sprint(targets["revision"]), id)
			return taskResult(result, "AUDIO_BATCH", id, e)
		}),
		writeTool("audio_batch_cancel", "取消音频批次", "任务", "/library/audio-processing", "通过音频服务取消未完成成员，保留已经生成的结果", idParameters(), func(ctx context.Context, a toolArgs) (any, any, error) {
			b, e := p.AudioBatch(ctx, a.text("id"))
			return stableBatch(b), map[string]any{"status": "取消未完成音频项目"}, e
		}, func(ctx context.Context, a toolArgs, _ string, _ assistant.Preview) (any, error) {
			r, e := p.CancelAudioBatch(ctx, a.text("id"))
			return taskResult(r, "AUDIO_BATCH", a.text("id"), e)
		}),
		writeTool("classification_control", "控制歌曲分类任务", "任务", "/library/classification", "通过分类服务暂停、继续或取消指定批次；已经应用的分类保留", toolParameters{"id": stringParam("分类任务真实标识", true), "action": enumParam("任务操作", true, "pause", "resume", "cancel")}, func(ctx context.Context, a toolArgs) (any, any, error) {
			r, e := v.curation.ClassificationTask(ctx, a.text("id"))
			if e != nil {
				return nil, nil, e
			}
			snapshot := stableBatch(r)
			state := fmt.Sprint(snapshot["status"])
			action := a.text("action")
			if action == "pause" && state != "RUNNING" || action == "resume" && state != "PAUSED" || action == "cancel" && state != "RUNNING" && state != "PAUSED" {
				return nil, nil, invalidTool("当前分类任务状态不支持该操作，请重新查询")
			}
			return snapshot, map[string]any{"status": map[string]string{"pause": "暂停", "resume": "继续", "cancel": "取消"}[action]}, nil
		}, func(ctx context.Context, a toolArgs, _ string, _ assistant.Preview) (any, error) {
			r, e := v.curation.ControlClassificationTask(ctx, a.text("id"), a.text("action"))
			return taskResult(r, "CLASSIFICATION", a.text("id"), e)
		}),
		writeTool("tv_settings_update", "更新电视文字与轮播设置", "客厅", "/system/tv-settings", "更新电视首页文字和轮播间隔，已有图片保留；上传及排序图片请打开电视设置", toolParameters{"revision": numberParam("查询电视设置获得的修订号", true), "headline": stringParam("电视标题，1～80 字", true), "subtitle": stringParam("电视说明，最多 160 字，可为空", true), "autoRotate": {Type: schema.Boolean, Required: true}, "rotationSeconds": numberParam("轮播间隔，5～60 秒", true)}, func(ctx context.Context, a toolArgs) (any, any, error) {
			if e := validateTV(a); e != nil {
				return nil, nil, e
			}
			current, e := v.devices.TVSettings(ctx)
			if e != nil {
				return nil, nil, e
			}
			if current.Revision != int64(a.number("revision", 0)) {
				return nil, nil, invalidTool("电视设置已变化，请重新查询")
			}
			return current, map[string]any{"headline": a.text("headline"), "subtitle": a.text("subtitle"), "autoRotate": a.boolean("autoRotate"), "rotationSeconds": a.number("rotationSeconds", 0)}, nil
		}, func(ctx context.Context, a toolArgs, _ string, _ assistant.Preview) (any, error) {
			current, e := v.devices.TVSettings(ctx)
			if e != nil {
				return nil, e
			}
			current.Revision = int64(a.number("revision", 0))
			current.Headline = a.text("headline")
			current.Subtitle = a.text("subtitle")
			current.AutoRotate = a.boolean("autoRotate")
			current.RotationSeconds = a.number("rotationSeconds", 0)
			return v.devices.SaveTVSettings(ctx, current)
		}),
		readTool("task_items", "查询任务逐项结果", "任务", "/system/tasks", "读取指定后台任务的逐曲结果、失败原因和真实入库标识", toolParameters{"id": stringParam("后台任务中心的真实标识", true), "page": numberParam("从 0 开始", false)}, func(ctx context.Context, a toolArgs) (any, error) {
			return v.tasks.TaskItems(ctx, a.text("id"), a.number("page", 0), 50)
		}),
	}
	tools[0].Reconcile = func(ctx context.Context, _ json.RawMessage, id string) (any, error) { return p.AudioBatch(ctx, id) }
	return tools
}

func taskResult(result any, kind, id string, err error) (any, error) {
	if err != nil {
		return nil, err
	}
	return map[string]any{"result": result, "tasks": []any{map[string]any{"id": background.ID(kind, id), "businessId": id, "type": kind}}}, nil
}

func downloadTaskResult(result map[string]any) any {
	ids := map[string]bool{}
	if b, ok := result["batch"].(map[string]any); ok {
		if id, ok := b["id"].(string); ok {
			ids[id] = true
		}
	}
	if related, ok := result["relatedBatchIds"].([]string); ok {
		for _, id := range related {
			ids[id] = true
		}
	}
	tasks := []any{}
	for id := range ids {
		tasks = append(tasks, map[string]any{"id": background.ID("LX_DOWNLOAD", id), "businessId": id, "type": "LX_DOWNLOAD"})
	}
	return map[string]any{"result": result, "tasks": tasks}
}

func audioInput(a toolArgs) map[string]any {
	ids := []any{}
	for _, id := range a.list("songIds") {
		ids = append(ids, id)
	}
	return map[string]any{"ids": ids, "kind": a.text("kind"), "backend": a.text("backend")}
}

func stableBatch(value any) map[string]any {
	b, _ := json.Marshal(value)
	m := map[string]any{}
	_ = json.Unmarshal(b, &m)
	out := map[string]any{}
	for _, k := range []string{"id", "name", "kind", "backend", "createdAt", "sourceId", "quality", "status", "state"} {
		if m[k] != nil {
			out[k] = m[k]
		}
	}
	return out
}

func validateTV(a toolArgs) error {
	if strings.TrimSpace(a.text("headline")) == "" || search.UTF16Length(a.text("headline")) > 80 || search.UTF16Length(a.text("subtitle")) > 160 || a.number("rotationSeconds", 0) < 5 || a.number("rotationSeconds", 0) > 60 {
		return invalidTool("电视标题最多 80 字、说明最多 160 字，轮播间隔为 5～60 秒")
	}
	return nil
}
