package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/schema"
	"github.com/mynora-dot/singverse-go-backend/internal/assistant"
	"github.com/mynora-dot/singverse-go-backend/internal/catalog"
	"github.com/mynora-dot/singverse-go-backend/internal/downloads"
	"github.com/mynora-dot/singverse-go-backend/internal/feedback"
	"github.com/mynora-dot/singverse-go-backend/internal/platform/search"
)

type toolArgs map[string]json.RawMessage

func toolArguments(raw json.RawMessage) toolArgs {
	v := toolArgs{}
	_ = json.Unmarshal(raw, &v)
	return v
}
func (v toolArgs) text(key string) string {
	var out string
	_ = json.Unmarshal(v[key], &out)
	return out
}
func (v toolArgs) number(key string, fallback int) int {
	if v[key] == nil {
		return fallback
	}
	var out int
	_ = json.Unmarshal(v[key], &out)
	return out
}
func (v toolArgs) list(key string) []string {
	out := []string{}
	_ = json.Unmarshal(v[key], &out)
	return out
}
func (v toolArgs) boolean(key string) bool {
	var out bool
	_ = json.Unmarshal(v[key], &out)
	return out
}
func stringParam(desc string, required bool) *schema.ParameterInfo {
	return &schema.ParameterInfo{Type: schema.String, Desc: desc, Required: required}
}
func numberParam(desc string, required bool) *schema.ParameterInfo {
	return &schema.ParameterInfo{Type: schema.Integer, Desc: desc, Required: required}
}
func listParam(desc string, required bool) *schema.ParameterInfo {
	return &schema.ParameterInfo{Type: schema.Array, Desc: desc, Required: required, ElemInfo: &schema.ParameterInfo{Type: schema.String}}
}
func enumParam(desc string, required bool, values ...string) *schema.ParameterInfo {
	return &schema.ParameterInfo{Type: schema.String, Desc: desc, Required: required, Enum: values}
}

type toolParameters = map[string]*schema.ParameterInfo

func idParameters() toolParameters {
	return toolParameters{"id": stringParam("先查询获得的真实对象标识", true)}
}
func pageParameters() toolParameters {
	return toolParameters{"q": stringParam("搜索关键词", false), "page": numberParam("从 0 开始，默认 0", false)}
}
func readTool(name, title, group, page, description string, params toolParameters, read func(context.Context, toolArgs) (any, error)) assistant.Definition {
	return assistant.Definition{Name: name, Title: title, Group: group, Page: page, Description: description, Parameters: params, Read: func(ctx context.Context, raw json.RawMessage) (any, error) { return read(ctx, toolArguments(raw)) }}
}
func writeTool(name, title, group, page, impact string, params toolParameters, prepare func(context.Context, toolArgs) (any, any, error), execute func(context.Context, toolArgs, string, assistant.Preview) (any, error)) assistant.Definition {
	return assistant.Definition{Name: name, Title: title, Group: group, Page: page, Description: impact + "。必须等待管理员确认", Write: true, Parameters: params,
		Prepare: func(ctx context.Context, raw json.RawMessage) (assistant.Preview, error) {
			a := toolArguments(raw)
			targets, changes, e := prepare(ctx, a)
			if e != nil {
				return assistant.Preview{}, e
			}
			return assistant.Preview{Summary: title, Targets: targets, Changes: changes, Impact: impact, Revision: assistant.Hash([]any{raw, targets, changes}), Page: page}, nil
		},
		Execute: func(ctx context.Context, raw json.RawMessage, id string, p assistant.Preview) (any, error) {
			return execute(ctx, toolArguments(raw), id, p)
		},
	}
}
func invalidTool(message string) error { return &toolProblem{message} }

type toolProblem struct{ message string }

func (e *toolProblem) Error() string { return e.message }
func (*toolProblem) StatusCode() int { return 400 }
func boundedPage(a toolArgs) (int, error) {
	p := a.number("page", 0)
	if p < 0 || p > 1000000 {
		return 0, invalidTool("分页参数无效")
	}
	return p, nil
}

// The composition root injects only concrete business capabilities. The Agent
// has no database, HTTP router or application service locator exposed as a tool.
func assistantTools(v *applicationServices) []assistant.Definition {
	lib, curation, models, processing, manager := v.media, v.curation, v.models, v.processing, v.downloads
	tools := []assistant.Definition{
		readTool("songs_search", "查询曲库歌曲", "曲库", "/library/songs", "按歌名、歌手、语言查询真实歌曲；返回稳定歌曲标识", toolParameters{"q": stringParam("歌名或关键词", false), "artist": stringParam("歌手名称", false), "language": stringParam("语言，如国语、粤语", false), "limit": numberParam("1～100，默认 30", false)}, func(ctx context.Context, a toolArgs) (any, error) {
			limit := a.number("limit", 30)
			if limit < 1 || limit > 100 {
				return nil, invalidTool("查询数量应为 1～100")
			}
			songs, e := lib.LibrarySongs(ctx, nil)
			if e != nil {
				return nil, e
			}
			items := []map[string]any{}
			total := 0
			for _, raw := range songs {
				s := curation.SongSummary(raw)
				encoded, _ := json.Marshal(s)
				haystack := search.Searchable(string(encoded))
				if !strings.Contains(haystack, search.Normalize(a.text("q"))) || !strings.Contains(search.Searchable(fmt.Sprint(s["artists"])), search.Normalize(a.text("artist"))) || !strings.Contains(search.Searchable(fmt.Sprint(s["language"])), search.Normalize(a.text("language"))) {
					continue
				}
				total++
				if len(items) < limit {
					items = append(items, s)
				}
			}
			return map[string]any{"items": items, "total": total}, nil
		}),
		readTool("song_get", "查询歌曲详情", "曲库", "/library/songs", "查看当前歌曲身份、元数据、分类与修订信息", idParameters(), func(ctx context.Context, a toolArgs) (any, error) {
			s, e := lib.Song(ctx, a.text("id"))
			if e != nil {
				return nil, e
			}
			return map[string]any{"song": curation.SongSummary(s), "metadata": s["metadata"], "classification": s["classification"]}, nil
		}),
		readTool("playlists_list", "查询歌单", "曲库", "/library/playlists", "查看已保存歌单及歌曲数量", pageParameters(), func(ctx context.Context, a toolArgs) (any, error) {
			p, e := boundedPage(a)
			if e != nil {
				return nil, e
			}
			return curation.Playlists(ctx, a.text("q"), p, 30)
		}),
		readTool("playlist_get", "查看歌单歌曲", "曲库", "/library/playlists", "读取歌单、歌曲标识和当前修订号", idParameters(), func(ctx context.Context, a toolArgs) (any, error) { return curation.Playlist(ctx, a.text("id")) }),
		readTool("tags_list", "查询分类标签", "曲库", "/library/classification", "语言、年代、曲风、情绪、场景与自定义标签", nil, func(ctx context.Context, _ toolArgs) (any, error) { return curation.Tags(ctx) }),
		readTool("favorites_list", "查询收藏歌曲", "曲库", "/library/favorites", "查看家庭共享收藏及修订号", pageParameters(), func(ctx context.Context, a toolArgs) (any, error) {
			p, e := boundedPage(a)
			if e != nil {
				return nil, e
			}
			return curation.Favorites(ctx, a.text("q"), p, 30)
		}),
		readTool("artists_list", "查询歌手档案", "曲库", "/library/artists", "只读查询已持久化歌手档案、分类与图片状态", toolParameters{"q": stringParam("歌手名称", false)}, func(ctx context.Context, a toolArgs) (any, error) {
			items, e := curation.ArtistProfiles(ctx)
			if e != nil {
				return nil, e
			}
			out := []map[string]any{}
			for _, item := range items {
				if strings.Contains(search.Searchable(fmt.Sprint(item["name"])), search.Normalize(a.text("q"))) {
					out = append(out, item)
					if len(out) == 100 {
						break
					}
				}
			}
			return out, nil
		}),
		readTool("sources_list", "查询媒体库", "加歌", "/library/media", "读取媒体源与扫描状态；不读取密码", nil, func(ctx context.Context, _ toolArgs) (any, error) { return lib.Sources(ctx) }),
		readTool("source_get", "查询媒体库详情", "加歌", "/library/media", "查看媒体源、定时扫描和当前修订时间", idParameters(), func(ctx context.Context, a toolArgs) (any, error) { return lib.Source(ctx, a.text("id")) }),
		readTool("filename_parse", "预览文件名识别", "加歌", "/library/media/recognition", "按已有识别规则解析文件名，不写入曲库", toolParameters{"filename": stringParam("一个媒体文件名，不上传文件", true)}, func(ctx context.Context, a toolArgs) (any, error) {
			return lib.PreviewFilename(ctx, a.text("filename"))
		}),
		readTool("downloads_search", "搜索可下载歌曲", "加歌", "/library/downloads", "使用已配置音源检索并缓存选择标识，不创建下载任务", toolParameters{"q": stringParam("歌名或歌手，1～100 字", true), "platform": enumParam("平台，默认 all", false, "all", "wy", "tx", "kg", "kw", "mg"), "page": numberParam("从 1 开始，默认 1", false)}, func(ctx context.Context, a toolArgs) (any, error) {
			return manager.Search(ctx, a.text("q"), a.text("platform"), a.number("page", 1))
		}),
		readTool("downloads_share_preview", "预览平台分享链接", "加歌", "/library/downloads", "读取歌曲、专辑或歌单分享链接，不创建下载任务", toolParameters{"url": stringParam("受支持音乐平台的分享链接", true), "page": numberParam("从 1 开始，默认 1", false)}, func(ctx context.Context, a toolArgs) (any, error) {
			return manager.Preview(ctx, a.text("url"), a.number("page", 1))
		}),
		readTool("downloads_status", "查询下载音源状态", "加歌", "/library/downloads", "查看音源、平台能力与下载保存位置，不读取脚本和凭据", nil, func(ctx context.Context, _ toolArgs) (any, error) { return manager.Status(ctx) }),
		readTool("downloads_batches", "查询下载批次", "任务", "/library/downloads", "查看真实下载批次及成功、失败、排队数量", pageParameters(), func(ctx context.Context, a toolArgs) (any, error) {
			return manager.Batches(ctx, a.number("page", 0), 30)
		}),
		readTool("downloads_items", "查询下载批次明细", "任务", "/library/downloads", "查看下载项目、入库结果与真实歌曲标识", toolParameters{"id": stringParam("下载批次标识", true), "page": numberParam("从 0 开始", false)}, func(ctx context.Context, a toolArgs) (any, error) {
			return manager.Items(ctx, a.text("id"), a.number("page", 0), 50)
		}),
		readTool("metadata_song", "查询歌曲匹配信息", "整理", "/library/songs/metadata", "查看歌曲元数据匹配与确认详情", idParameters(), func(ctx context.Context, a toolArgs) (any, error) { return v.tags.SongDetail(ctx, a.text("id")) }),
		readTool("classification_tasks", "查询分类任务", "任务", "/library/classification", "查看分类批次和结果", pageParameters(), func(ctx context.Context, a toolArgs) (any, error) {
			return curation.ClassificationTasks(ctx, a.number("page", 0), 30)
		}),
		readTool("classification_task", "查询分类任务明细", "任务", "/library/classification", "查看指定任务的分类建议和项目状态", idParameters(), func(ctx context.Context, a toolArgs) (any, error) {
			return curation.ClassificationTask(ctx, a.text("id"))
		}),
		readTool("audio_status", "查询歌曲音轨状态", "整理", "/library/audio-processing", "查看原唱、伴奏、识别及处理任务", idParameters(), func(ctx context.Context, a toolArgs) (any, error) { return processing.AudioStatus(ctx, a.text("id")) }),
		readTool("audio_batches", "查询音频批次", "任务", "/library/audio-processing", "查看快速识别、深度识别和伴奏批次", pageParameters(), func(ctx context.Context, a toolArgs) (any, error) {
			return processing.AudioBatches(ctx, a.number("page", 0), 30)
		}),
		readTool("audio_batch", "查询音频批次明细", "任务", "/library/audio-processing", "查看真实处理进度与逐曲结果", idParameters(), func(ctx context.Context, a toolArgs) (any, error) { return processing.AudioBatch(ctx, a.text("id")) }),
		readTool("transcodes_list", "查询离线转码", "整理", "/library/audio-processing", "查看已经创建的转码任务与结果", nil, func(ctx context.Context, _ toolArgs) (any, error) { return processing.OfflineTranscodes(ctx) }),
		readTool("devices_list", "查询电视设备", "客厅", "/system/assistant", "只读列出电视设备与连接状态，不读取设备凭据", nil, func(ctx context.Context, _ toolArgs) (any, error) { return v.devices.Devices(ctx) }),
		readTool("playback_summary", "查询播放与热门统计", "客厅", "/library/playback-history", "最近 30 天的真实播放统计", nil, func(ctx context.Context, _ toolArgs) (any, error) { return v.activity.Summary30d(ctx) }),
		readTool("feedback_list", "查询缺歌心愿", "客厅", "/library/missing-feedback", "查询缺歌反馈、处理情况和关联歌曲", toolParameters{"q": stringParam("歌名或歌手关键词", false), "status": enumParam("默认 ALL", false, "ALL", "PENDING", "PROCESSING", "RESOLVED", "CLOSED"), "page": numberParam("从 0 开始", false)}, func(ctx context.Context, a toolArgs) (any, error) {
			status := a.text("status")
			if status == "" {
				status = "ALL"
			}
			return v.missingSongs.List(ctx, a.text("q"), status, a.number("page", 0), 30)
		}),
		readTool("feedback_get", "查看缺歌心愿详情", "客厅", "/library/missing-feedback", "读取心愿、处理备注和修订号", idParameters(), func(ctx context.Context, a toolArgs) (any, error) { return v.missingSongs.Get(ctx, a.text("id")) }),
		readTool("platform_settings", "查询音乐平台设置", "系统", "/system/music-platforms", "查看匹配平台、间隔与自动写入配置", nil, func(ctx context.Context, _ toolArgs) (any, error) { return v.tags.PlatformSettings(ctx) }),
		readTool("cache_status", "查询缓存使用情况", "系统", "/library/cache", "查看缓存容量与设置，不清理文件", nil, func(ctx context.Context, _ toolArgs) (any, error) {
			usage, e := processing.CacheUsage(ctx)
			if e != nil {
				return nil, e
			}
			settings, e := processing.CacheSettings(ctx)
			return map[string]any{"usage": usage, "settings": settings}, e
		}),
		readTool("compute_settings", "查询计算节点状态", "系统", "/system/compute", "查看已配置节点和能力，不发起配对或读取令牌", nil, func(ctx context.Context, _ toolArgs) (any, error) { return v.node.View(ctx) }),
		readTool("ai_services", "查询模型与搜索服务", "系统", "/system/ai/models", "查看模型、搜索与功能绑定，不解密密钥", nil, func(ctx context.Context, _ toolArgs) (any, error) {
			m, e := models.List(ctx, "MODEL")
			if e != nil {
				return nil, e
			}
			searches, e := models.List(ctx, "SEARCH")
			if e != nil {
				return nil, e
			}
			f, e := models.Features(ctx)
			return map[string]any{"models": m, "searches": searches, "features": f}, e
		}),
		readTool("runtime_status", "查询后台运行概况", "系统", "/system", "读取曲库、任务、节点与服务状态", nil, func(ctx context.Context, _ toolArgs) (any, error) { return v.overview.Collect(ctx) }),
		readTool("tasks_active", "查询活动后台任务", "任务", "/system/tasks", "读取真实后台任务及所属业务页面", nil, func(ctx context.Context, _ toolArgs) (any, error) { return v.tasks.ActiveSnapshot(ctx, 50) }),
		readTool("task_detail", "查询后台任务详情", "任务", "/system/tasks", "读取任务状态、进度、结果及所属业务标识", idParameters(), func(ctx context.Context, a toolArgs) (any, error) { return v.tasks.Task(ctx, a.text("id")) }),
	}
	tools = append(tools, assistantWriteTools(v)...)
	tools = append(tools, assistantProcessingTools(v)...)
	// Form navigation is explicit in the coverage catalogue, including operations
	// requiring files, credentials or complex business editors.
	forms := []struct{ name, title, group, page, description string }{
		{"song_editor_form", "歌曲编辑与移除", "曲库", "/library/songs", "编辑单曲资料、人工音轨修正、移除索引或处理原始文件；核对完整对象后使用歌曲工作台表单"},
		{"playback_history_form", "播放记录与热门明细", "客厅", "/library/playback-history", "筛选逐次播放记录、客户端与时间范围，查看热门歌曲和实际播放结果"},
		{"account_security_form", "账号与安全表单", "系统", "/system/parameters", "修改密码、MFA、账号和安全设置；不要在对话中输入密码或验证码"},
		{"media_source_form", "媒体源配置表单", "加歌", "/library/media", "创建、编辑、删除媒体库、WebDAV 凭据、路径和定时扫描"},
		{"filename_rules_form", "文件名规则编辑表单", "加歌", "/library/media/recognition", "编辑识别规则、解析方案和重新识别范围"},
		{"download_source_form", "下载音源导入表单", "加歌", "/library/downloads", "上传或导入音源脚本、配置环境、切换及删除音源；脚本和凭据不进入对话"},
		{"share_import_form", "分享与元数据导入表单", "加歌", "/library/media", "上传分享包、预览并确认媒体导入；文件通过现有表单上传"},
		{"metadata_editor_form", "歌曲信息匹配与编辑", "整理", "/library/songs/metadata", "匹配、比较和确认歌曲信息，编辑封面与歌词，处理批量部分失败"},
		{"classification_editor_form", "分类与标签编辑", "曲库", "/library/classification", "编辑、停用标签和歌曲分类，审核建议、恢复历史或启动分类批次"},
		{"artist_images_form", "歌手图片管理", "整理", "/library/artists", "上传、比较及审核歌手图片，管理自动抓图和批次"},
		{"audio_processing_form", "音频处理工作台", "整理", "/library/audio-processing", "批量音轨识别、伴奏方案、模型安装、音轨修正和转码配置；文件通过现有表单上传"},
		{"client_downloads_form", "客户端下载信息", "客厅", "/system/client-downloads", "查看当前随服务提供的手机及电视客户端下载包和安装说明"},
		{"music_platform_form", "音乐平台配置表单", "系统", "/system/music-platforms", "配置匹配平台、自动写入和抓图服务；密钥通过已有表单保存"},
		{"cache_settings_form", "缓存设置与清理表单", "系统", "/library/cache", "核对缓存范围后清理，调整目录、容量及缓存策略"},
		{"compute_pairing_form", "计算节点配置表单", "系统", "/system/compute", "发现、配对、路径映射、运行环境和模型配置；令牌通过原表单输入"},
		{"ai_configuration_form", "AI 服务配置表单", "系统", "/system/ai/features/assistant", "模型与搜索服务绑定、密钥管理、原生工具连接测试和助手入口配置"},
		{"task_controls_form", "后台任务控制入口", "任务", "/system/tasks", "查看所有任务与逐项结果，在所属业务页面取消、重试或处理审核项"},
	}
	for _, f := range forms {
		form := f
		tools = append(tools, readTool(form.name, form.title, form.group, form.page, form.description, nil, func(context.Context, toolArgs) (any, error) {
			return map[string]any{"name": form.title, "message": form.description, "page": form.page, "formRequired": true}, nil
		}))
	}
	// Optional search uses exactly the service bound to assistant; it does not
	// silently fall back to another feature's provider or ambient credentials.
	tools = append(tools, readTool("web_search", "联网搜索", "系统", "/system/ai/features/assistant", "使用助手绑定的可选搜索服务；搜索内容不构成业务授权", toolParameters{"q": stringParam("搜索关键词", true)}, func(ctx context.Context, a toolArgs) (any, error) {
		f, e := models.Feature(ctx, "assistant")
		if e != nil {
			return nil, e
		}
		if f.SearchServiceID == nil {
			return nil, invalidTool("助手未绑定搜索服务，请在助手设置中选择")
		}
		snap, e := models.Snapshot(ctx, *f.SearchServiceID)
		if e != nil {
			return nil, e
		}
		return models.Search(ctx, snap, a.text("q"))
	}))
	return tools
}

func assistantWriteTools(v *applicationServices) []assistant.Definition {
	curation, lib, manager := v.curation, v.media, v.downloads
	playlistParams := func(update bool) toolParameters {
		p := toolParameters{"name": stringParam("歌单名称，1～120 字", true), "description": stringParam("歌单说明，最多 1000 字，可为空", true), "songIds": listParam("先查询获得的歌曲标识，最多 500 首", true)}
		if update {
			p["id"] = stringParam("已有歌单标识", true)
			p["revision"] = numberParam("读取歌单获得的当前修订号", true)
		}
		return p
	}
	preparePlaylist := func(ctx context.Context, a toolArgs) (any, any, error) {
		if strings.TrimSpace(a.text("name")) == "" || search.UTF16Length(a.text("name")) > 120 || search.UTF16Length(a.text("description")) > 1000 {
			return nil, nil, invalidTool("歌单名称或说明长度无效")
		}
		songs, e := curation.ValidatePlaylist(ctx, a.list("songIds"))
		if e != nil {
			return nil, nil, e
		}
		var before any
		if a.text("id") != "" {
			before, e = curation.Playlist(ctx, a.text("id"))
			if e != nil {
				return nil, nil, e
			}
			m, _ := before.(map[string]any)
			if fmt.Sprint(m["revision"]) != fmt.Sprint(a.number("revision", 0)) {
				return nil, nil, invalidTool("歌单修订号已变化，请重新查询")
			}
		}
		return map[string]any{"songs": songs, "playlist": before}, map[string]any{"name": a.text("name"), "description": a.text("description"), "count": len(songs)}, nil
	}
	savePlaylist := func(ctx context.Context, a toolArgs, id string, p assistant.Preview) (any, error) {
		description := a.text("description")
		input := catalog.PlaylistSave{RequestID: id, Name: a.text("name"), Description: &description, SongIDs: a.list("songIds"), Revision: int64(a.number("revision", 0))}
		targets, _ := p.Targets.(map[string]any)
		input.ExpectedSongsHash = assistant.Hash(targets["songs"])
		return curation.SavePlaylist(ctx, a.text("id"), input)
	}
	tools := []assistant.Definition{
		writeTool("playlist_create", "创建歌单", "曲库", "/library/playlists", "创建家庭共享歌单，不复制或修改音视频文件", playlistParams(false), preparePlaylist, savePlaylist),
		writeTool("playlist_update", "修改歌单", "曲库", "/library/playlists", "替换指定歌单的名称、说明和完整歌曲列表", playlistParams(true), preparePlaylist, savePlaylist),
		writeTool("playlist_delete", "删除歌单", "曲库", "/library/playlists", "删除指定歌单，歌曲和原始媒体文件保留", toolParameters{"id": stringParam("歌单标识", true), "revision": numberParam("当前歌单修订号", true)}, func(ctx context.Context, a toolArgs) (any, any, error) {
			p, e := curation.Playlist(ctx, a.text("id"))
			if e != nil {
				return nil, nil, e
			}
			m, _ := p.(map[string]any)
			if fmt.Sprint(m["revision"]) != fmt.Sprint(a.number("revision", 0)) {
				return nil, nil, invalidTool("歌单修订号已变化")
			}
			return p, map[string]any{"status": "删除歌单"}, nil
		}, func(ctx context.Context, a toolArgs, _ string, _ assistant.Preview) (any, error) {
			e := curation.DeletePlaylist(ctx, a.text("id"), int64(a.number("revision", 0)))
			return map[string]any{"id": a.text("id"), "status": "已删除"}, e
		}),
		writeTool("favorites_change", "修改家庭收藏", "曲库", "/library/favorites", "批量添加或移除共享收藏，不删除歌曲", toolParameters{"songIds": listParam("歌曲标识，1～500 首", true), "selected": {Type: schema.Boolean, Required: true, Desc: "true 收藏，false 取消收藏"}, "revision": numberParam("查询收藏获得的修订号", true)}, func(ctx context.Context, a toolArgs) (any, any, error) {
			if len(a.list("songIds")) < 1 {
				return nil, nil, invalidTool("请选择歌曲")
			}
			current, e := curation.FavoritesState(ctx)
			if e != nil {
				return nil, nil, e
			}
			if fmt.Sprint(current["revision"]) != fmt.Sprint(a.number("revision", 0)) {
				return nil, nil, invalidTool("收藏修订号已变化")
			}
			songs, e := curation.ValidatePlaylist(ctx, a.list("songIds"))
			return map[string]any{"songs": songs, "favorites": current}, map[string]any{"selected": a.boolean("selected"), "count": len(songs)}, e
		}, func(ctx context.Context, a toolArgs, _ string, _ assistant.Preview) (any, error) {
			selected := a.boolean("selected")
			revision := int64(a.number("revision", 0))
			return curation.ChangeFavorites(ctx, catalog.FavoriteChange{SongIDs: a.list("songIds"), Selected: &selected, ExpectedRevision: &revision})
		}),
		writeTool("downloads_create", "下载并入库歌曲", "加歌", "/library/downloads", "创建一个真实下载批次，下载文件到已配置媒体库并自动入库；批次只确认一次", toolParameters{"trackIds": listParam("下载搜索或分享预览返回的选择标识，1～500 首", true), "quality": enumParam("下载音质", false, "auto", "flac24bit", "flac", "320k", "128k"), "name": stringParam("批次名称，最多 200 字", false)}, func(ctx context.Context, a toolArgs) (any, any, error) {
			preview, e := manager.DownloadPreview(ctx, downloads.BatchInput{TrackIDs: a.list("trackIds"), Quality: a.text("quality"), Name: a.text("name")})
			if e != nil {
				return nil, nil, e
			}
			return preview, map[string]any{"quality": preview["quality"], "name": a.text("name"), "path": preview["path"]}, nil
		}, func(ctx context.Context, a toolArgs, id string, p assistant.Preview) (any, error) {
			targets, _ := p.Targets.(map[string]any)
			r, e := manager.CreateConfirmed(ctx, downloads.BatchInput{TrackIDs: a.list("trackIds"), Quality: a.text("quality"), Name: a.text("name")}, fmt.Sprint(targets["revision"]), id)
			if e != nil {
				return nil, e
			}
			return downloadTaskResult(r), nil
		}),
		writeTool("downloads_cancel", "取消下载批次", "任务", "/library/downloads", "取消该下载批次未完成项目，已入库歌曲保留", idParameters(), func(ctx context.Context, a toolArgs) (any, any, error) {
			p, e := manager.Batch(ctx, a.text("id"))
			return stableBatch(p), map[string]any{"status": "取消未完成下载"}, e
		}, func(ctx context.Context, a toolArgs, _ string, _ assistant.Preview) (any, error) {
			e := manager.Cancel(ctx, a.text("id"))
			return taskResult(map[string]any{"id": a.text("id"), "status": "已提交取消"}, "LX_DOWNLOAD", a.text("id"), e)
		}),
		writeTool("downloads_retry", "重试下载批次", "任务", "/library/downloads", "通过下载服务重试该批次失败项目，保留已完成结果", idParameters(), func(ctx context.Context, a toolArgs) (any, any, error) {
			p, e := manager.RetryPreview(ctx, a.text("id"))
			return p, map[string]any{"status": "重试失败项目"}, e
		}, func(ctx context.Context, a toolArgs, id string, preview assistant.Preview) (any, error) {
			targets, _ := preview.Targets.(map[string]any)
			r, e := manager.RetryConfirmed(ctx, a.text("id"), fmt.Sprint(targets["revision"]), id)
			if e != nil {
				return nil, e
			}
			return downloadTaskResult(r), nil
		}),
		writeTool("artist_category", "修改歌手分类", "曲库", "/library/artists", "修改指定歌手的分类，保留歌曲与图片", toolParameters{"id": stringParam("歌手标识", true), "type": enumParam("歌手分类", true, "MALE", "FEMALE", "GROUP", "UNKNOWN"), "revision": numberParam("当前歌手修订号", true)}, func(ctx context.Context, a toolArgs) (any, any, error) {
			p, e := curation.ArtistProfile(ctx, a.text("id"))
			if e != nil {
				return nil, nil, e
			}
			if fmt.Sprint(p["revision"]) != fmt.Sprint(a.number("revision", 0)) {
				return nil, nil, invalidTool("歌手信息已变化")
			}
			return p, map[string]any{"type": a.text("type")}, nil
		}, func(ctx context.Context, a toolArgs, _ string, _ assistant.Preview) (any, error) {
			return curation.ArtistCategory(ctx, a.text("id"), a.text("type"), int64(a.number("revision", 0)))
		}),
		writeTool("feedback_update", "处理缺歌心愿", "客厅", "/library/missing-feedback", "更新心愿状态、备注和关联歌曲，不新增或下载歌曲", toolParameters{"id": stringParam("心愿标识", true), "revision": numberParam("当前修订号", true), "status": enumParam("处理状态", true, "PENDING", "PROCESSING", "RESOLVED", "CLOSED"), "handlingNote": stringParam("处理备注", true), "resolvedSongId": stringParam("已入库歌曲标识，未关联时为空", false)}, func(ctx context.Context, a toolArgs) (any, any, error) {
			p, e := v.missingSongs.PreviewUpdate(ctx, a.text("id"), feedbackInput(a))
			return p, map[string]any{"status": a.text("status"), "handlingNote": a.text("handlingNote"), "resolvedSongId": a.text("resolvedSongId")}, e
		}, func(ctx context.Context, a toolArgs, _ string, _ assistant.Preview) (any, error) {
			return v.missingSongs.Update(ctx, a.text("id"), feedbackInput(a))
		}),
	}
	for _, name := range []string{"playlist_create", "playlist_update"} {
		for i := range tools {
			if tools[i].Name == name {
				tools[i].Reconcile = func(ctx context.Context, raw json.RawMessage, id string) (any, error) {
					a := toolArguments(raw)
					if a.text("id") != "" {
						id = a.text("id")
					}
					return curation.Playlist(ctx, id)
				}
			}
		}
	}
	for i := range tools {
		if tools[i].Name == "downloads_create" {
			tools[i].Reconcile = func(ctx context.Context, raw json.RawMessage, id string) (any, error) {
				a := toolArguments(raw)
				return manager.ReconcileDownload(ctx, downloads.BatchInput{TrackIDs: a.list("trackIds"), Quality: a.text("quality")}, id)
			}
		}
		if tools[i].Name == "downloads_retry" {
			tools[i].Reconcile = func(ctx context.Context, raw json.RawMessage, id string) (any, error) {
				return manager.ReconcileRetry(ctx, toolArguments(raw).text("id"), id)
			}
		}
	}
	// Local media scans use their task-based ingest service, never a blocking
	// filesystem scan in the conversation goroutine.
	tools = append(tools, writeTool("source_scan", "扫描媒体库", "加歌", "/library/media", "提交现有媒体扫描/入库任务；停止对话不会取消扫描", idParameters(), func(ctx context.Context, a toolArgs) (any, any, error) {
		source, e := lib.Source(ctx, a.text("id"))
		return source, map[string]any{"status": "提交扫描与入库任务"}, e
	}, func(ctx context.Context, a toolArgs, _ string, _ assistant.Preview) (any, error) {
		source, e := lib.Source(ctx, a.text("id"))
		if e != nil {
			return nil, e
		}
		if source.Type == "WEBDAV" {
			task, e := lib.StartScan(ctx, source.ID)
			return taskResult(task, "WEBDAV_SCAN", task.ID, e)
		}
		id, e := lib.StartIngest(ctx, source.ID)
		return taskResult(map[string]any{"taskId": id, "sourceId": source.ID, "status": "QUEUED"}, "SOURCE_INGEST", id, e)
	}))
	return tools
}

func feedbackInput(a toolArgs) feedback.Update {
	note := a.text("handlingNote")
	var song *string
	if id := a.text("resolvedSongId"); id != "" {
		song = &id
	}
	return feedback.Update{Revision: int64(a.number("revision", 0)), Status: a.text("status"), HandlingNote: &note, ResolvedSongID: song}
}
