package metadata

import (
	"context"
	"encoding/base64"
	"fmt"
	"html"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/mynora-dot/singverse-go-backend/internal/metastandard"
)

const platformLyricsLimit = 256 * 1024
const miguLyricsHost = "d.musicapp.migu.cn"

var qqSongMID = regexp.MustCompile(`^[A-Za-z0-9]{1,32}$`)
var kugouSongHash = regexp.MustCompile(`^[a-fA-F0-9]{32}$`)
var miguLyricResource = regexp.MustCompile(`^/data/oss/resource/[A-Za-z0-9/_-]{1,280}(?:\.lrc)?$`)
var lyricHeader = regexp.MustCompile(`(?i)^\[(?:ti|ar|al|au|by|offset|length|re|ve|id|kana|high|language|total|tool):[^\]]*\]$`)
var lyricLineStamps = regexp.MustCompile(`^(?:\[\d{1,3}:\d{2}(?:[.,:]\d{1,3})?\])+`)

func (p *musicProvider) lyrics(ctx context.Context, track Track) (map[string]any, error) {
	id := *track.ExternalID
	switch p.name {
	case "NETEASE":
		if !publicMusicID.MatchString(id) {
			return nil, nil
		}
		q := url.Values{"id": {id}, "lv": {"-1"}}
		root, err := p.request(ctx, "GET", "https://music.163.com/api/song/lyric?"+q.Encode(), nil, map[string]string{"Referer": "https://music.163.com/"})
		if err != nil {
			return nil, err
		}
		if text(root["code"]) != "200" {
			return nil, &problem{503, "网易云歌词暂时不可用"}
		}
		if boolean(root["nolyric"]) || boolean(root["uncollected"]) {
			return nil, nil
		}
		return lyricResponseText(path(root, "lrc", "lyric"), false)
	case "QQ":
		if !qqSongMID.MatchString(id) {
			return nil, nil
		}
		q := url.Values{"songmid": {id}, "g_tk": {"5381"}, "format": {"json"}, "nobase64": {"1"}}
		root, err := p.request(ctx, "GET", "https://c.y.qq.com/lyric/fcgi-bin/fcg_query_lyric_new.fcg?"+q.Encode(), nil, map[string]string{"Referer": "https://y.qq.com/"})
		if err != nil {
			return nil, err
		}
		if text(root["code"]) != "0" || text(root["retcode"]) != "0" {
			return nil, &problem{503, "QQ 音乐歌词暂时不可用"}
		}
		return lyricResponseText(root["lyric"], true)
	case "KUGOU":
		return p.kugouLyrics(ctx, track)
	case "KUWO":
		return p.kuwoLyrics(ctx, id)
	case "MIGU":
		return p.miguLyrics(ctx, track.LyricsPath)
	}
	return nil, nil
}

func lyricResponseText(value any, unescapeHTML bool) (map[string]any, error) {
	if value == nil {
		return nil, nil
	}
	raw, ok := value.(string)
	if !ok {
		return nil, &problem{503, "平台歌词响应格式无效"}
	}
	if unescapeHTML {
		raw = html.UnescapeString(raw)
	}
	return lyricsFromText(raw)
}

func (p *musicProvider) kugouLyrics(ctx context.Context, track Track) (map[string]any, error) {
	id := *track.ExternalID
	if !kugouSongHash.MatchString(id) {
		return nil, nil
	}
	q := url.Values{"ver": {"1"}, "man": {"no"}, "client": {"mobi"}, "hash": {id}}
	if track.DurationMs != nil {
		q.Set("duration", strconv.Itoa(*track.DurationMs))
	}
	root, err := p.request(ctx, "GET", "https://krcs.kugou.com/search?"+q.Encode(), nil, nil)
	if err != nil {
		return nil, err
	}
	if text(root["status"]) != "200" {
		return nil, &problem{503, "酷狗歌词暂时不可用"}
	}
	candidates := array(root["candidates"])
	if len(candidates) == 0 {
		return nil, nil
	}
	choice, _ := candidates[0].(map[string]any)
	lyricID, key := text(choice["id"]), text(choice["accesskey"])
	if !publicMusicID.MatchString(lyricID) || key == "" || len(key) > 256 {
		return nil, &problem{503, "酷狗歌词响应无效"}
	}
	q = url.Values{"ver": {"1"}, "client": {"pc"}, "id": {lyricID}, "accesskey": {key}, "fmt": {"lrc"}, "charset": {"utf8"}}
	root, err = p.request(ctx, "GET", "https://lyrics.kugou.com/download?"+q.Encode(), nil, nil)
	if err != nil {
		return nil, err
	}
	encoded := text(root["content"])
	if text(root["status"]) != "200" || len(encoded) > base64.StdEncoding.EncodedLen(platformLyricsLimit) {
		return nil, &problem{503, "酷狗歌词响应无效"}
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, &problem{503, "酷狗歌词编码无效"}
	}
	return lyricsFromText(string(decoded))
}

func (p *musicProvider) kuwoLyrics(ctx context.Context, id string) (map[string]any, error) {
	if !publicMusicID.MatchString(id) {
		return nil, nil
	}
	q := url.Values{"musicId": {id}}
	root, err := p.request(ctx, "GET", "https://www.kuwo.cn/openapi/v1/www/lyric/getlyric?"+q.Encode(), nil, map[string]string{"Referer": "https://www.kuwo.cn/play_detail/" + id})
	if err != nil {
		return nil, err
	}
	if text(root["code"]) != "200" {
		return nil, &problem{503, "酷我歌词暂时不可用"}
	}
	var lrc strings.Builder
	for _, raw := range array(path(root, "data", "lrclist")) {
		line, _ := raw.(map[string]any)
		seconds, err := strconv.ParseFloat(text(line["time"]), 64)
		body := html.UnescapeString(text(line["lineLyric"]))
		if strings.TrimSpace(body) == "" {
			continue
		}
		if err != nil || !(seconds >= 0 && seconds < 60000) {
			return nil, &problem{503, "酷我歌词时间轴无效"}
		}
		ms := int64(math.Round(seconds * 1000))
		fmt.Fprintf(&lrc, "[%02d:%02d.%03d]%s\n", ms/60000, ms/1000%60, ms%1000, body)
		if lrc.Len() > platformLyricsLimit {
			return nil, &problem{503, "酷我歌词超过大小限制"}
		}
	}
	return lyricsFromText(lrc.String())
}

func miguLyricsPath(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host != miguLyricsHost || u.User != nil || u.RawQuery != "" || u.Fragment != "" || !miguLyricResource.MatchString(u.Path) {
		return ""
	}
	return u.Path
}

func (p *musicProvider) miguLyrics(ctx context.Context, assetPath string) (map[string]any, error) {
	if !miguLyricResource.MatchString(assetPath) {
		return nil, nil
	}
	req, err := http.NewRequestWithContext(ctx, "GET", "https://"+miguLyricsHost+assetPath, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", providerUserAgent)
	resp, err := p.service.client.Do(req)
	if err != nil {
		return nil, &problem{503, "咪咕歌词连接失败，请稍后重试"}
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNotFound:
		return nil, nil
	case http.StatusTooManyRequests:
		return nil, &problem{429, "平台返回限流，请稍后重试"}
	case http.StatusOK:
	default:
		return nil, &problem{503, fmt.Sprintf("咪咕歌词返回 HTTP %d", resp.StatusCode)}
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, platformLyricsLimit+1))
	if err != nil {
		return nil, &problem{503, "咪咕歌词响应无效"}
	}
	return lyricsFromText(string(data))
}

func lyricsFromText(raw string) (map[string]any, error) {
	value, err := metastandard.LyricsAsset("text/plain", []byte(raw))
	if err != nil {
		return nil, &problem{503, "平台歌词超过大小限制或编码无效"}
	}
	content := strings.TrimSpace(text(value["text"]))
	if strings.HasPrefix(content, "<") || strings.HasPrefix(content, "{") || strings.ContainsRune(content, 0) {
		return nil, &problem{503, "平台歌词响应格式无效"}
	}
	for _, line := range strings.Split(content, "\n") {
		body := strings.TrimSpace(lyricLineStamps.ReplaceAllString(strings.TrimSpace(line), ""))
		if body == "" || body == "暂无歌词" || body == "纯音乐，请欣赏" || body == "此歌曲为没有填词的纯音乐，请您欣赏" || lyricHeader.MatchString(body) {
			continue
		}
		value["text"] = content
		if metastandard.ValidateField("VARIANT", "lyrics", value) != nil {
			return nil, &problem{503, "平台歌词超过大小限制或格式无效"}
		}
		return value, nil
	}
	return nil, nil
}
