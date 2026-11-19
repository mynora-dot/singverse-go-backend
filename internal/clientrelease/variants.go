package clientrelease

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

var platformABIs = map[string][]string{
	"mobile": {"armeabi-v7a", "arm64-v8a", "x86_64"},
	"tv":     {"armeabi-v7a", "arm64-v8a", "x86", "x86_64"},
}

type Variant struct {
	ABI      string `json:"abi"`
	FileName string `json:"fileName"`
	Size     int64  `json:"size"`
	SHA256   string `json:"sha256"`
}

type DownloadVariant struct {
	ABI    string `json:"abi"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
	URL    string `json:"url"`
}

func knownABI(platform, abi string) bool {
	for _, supported := range platformABIs[platform] {
		if abi == supported {
			return true
		}
	}
	return false
}

func validManifest(m Manifest, platform string) bool {
	expectedSDK := 24
	if platform == "tv" {
		expectedSDK = 21
	}
	if m.SchemaVersion != 1 || m.Platform != platform || m.VersionCode < 1 || m.VersionCode > 2100000000 ||
		strings.TrimSpace(m.VersionName) == "" || len(m.VersionName) > 80 || m.PackageName != "com.mynora.singverse."+platform ||
		m.MinSDK != expectedSDK || len(m.ABIs) == 0 || m.Size < 1 || m.Size > MaxAPKSize ||
		!digestPattern.MatchString(m.SHA256) || !digestPattern.MatchString(m.CertificateSHA256) || len(m.Notes) > 16000 {
		return false
	}
	seen := map[string]bool{}
	for _, abi := range m.ABIs {
		if seen[abi] || (abi != "universal" && !knownABI(platform, abi)) || (abi == "universal" && len(m.ABIs) != 1) {
			return false
		}
		seen[abi] = true
	}
	return true
}

func selectManifest(raw []byte, platform, abi string) (Manifest, string, []DownloadVariant, error) {
	var index struct {
		Manifest
		Variants []Variant `json:"variants"`
	}
	if err := json.Unmarshal(raw, &index); err != nil {
		return Manifest{}, "", nil, err
	}
	invalid := errors.New("invalid release metadata")
	if index.SchemaVersion == 1 {
		if !validManifest(index.Manifest, platform) {
			return Manifest{}, "", nil, invalid
		}
		if abi != "" {
			compatible := false
			for _, supported := range index.ABIs {
				compatible = compatible || supported == abi || supported == "universal"
			}
			if !compatible {
				return Manifest{}, "", nil, nil
			}
		}
		return index.Manifest, "singverse-" + platform + ".apk", nil, nil
	}
	if index.SchemaVersion != 2 || len(index.Variants) != len(platformABIs[platform]) {
		return Manifest{}, "", nil, invalid
	}
	// Older clients have no ABI header. Keep a useful default and expose every
	// architecture to the download page so older installations can migrate.
	if abi == "" {
		abi = "arm64-v8a"
		if platform == "tv" {
			abi = "armeabi-v7a"
		}
	}
	seen := map[string]bool{}
	var selected Manifest
	var filename string
	var downloads []DownloadVariant
	for _, variant := range index.Variants {
		name := fmt.Sprintf("singverse-%s-%s.apk", platform, variant.ABI)
		if !knownABI(platform, variant.ABI) || seen[variant.ABI] || variant.FileName != name {
			return Manifest{}, "", nil, invalid
		}
		seen[variant.ABI] = true
		m := index.Manifest
		m.SchemaVersion = 1
		m.ABIs = []string{variant.ABI}
		m.Size, m.SHA256 = variant.Size, variant.SHA256
		if !validManifest(m, platform) {
			return Manifest{}, "", nil, invalid
		}
		downloads = append(downloads, DownloadVariant{variant.ABI, variant.Size, variant.SHA256,
			"/api/v1/client-releases/" + platform + "/apk?abi=" + variant.ABI})
		if variant.ABI == abi {
			selected, filename = m, name
		}
	}
	return selected, filename, downloads, nil
}
