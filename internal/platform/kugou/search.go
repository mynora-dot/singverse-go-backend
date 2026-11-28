// The catalogue request is adapted from ktv-home (MIT); see docs/third-party/ktv-home-LICENSE.txt.
package kugou

import (
	"crypto/md5"
	"encoding/hex"
	"maps"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Search builds an anonymous catalogue request for songs or artists.
func Search(kind, keyword, deviceID string, page, size int) (string, map[string]string) {
	route := "v3/search/song"
	if kind == "author" {
		route = "v1/search/author"
	}
	at := strconv.FormatInt(time.Now().Unix(), 10)
	q := url.Values{
		"dfid": {"-"}, "mid": {deviceID}, "uuid": {"-"}, "appid": {"1005"},
		"clientver": {"20489"}, "clienttime": {at}, "albumhide": {"0"}, "iscorrection": {"1"},
		"keyword": {keyword}, "nocollect": {"0"}, "page": {strconv.Itoa(page)},
		"pagesize": {strconv.Itoa(size)}, "platform": {"AndroidFilter"},
	}
	q.Set("signature", signature(q))
	return "https://gateway.kugou.com/" + route + "?" + q.Encode(), map[string]string{
		"x-router": "complexsearch.kugou.com", "dfid": "-", "mid": deviceID, "clienttime": at,
		"User-Agent": "Android15-1070-11083-46-0-DiscoveryDRADProtocol-wifi",
	}
}

func signature(q url.Values) string {
	const salt = "OIlwieks28dk2k092lksi2UIkp"
	var signed strings.Builder
	signed.WriteString(salt)
	for _, key := range slices.Sorted(maps.Keys(q)) {
		signed.WriteString(key)
		signed.WriteByte('=')
		signed.WriteString(q.Get(key))
	}
	signed.WriteString(salt)
	sum := md5.Sum([]byte(signed.String()))
	return hex.EncodeToString(sum[:])
}
