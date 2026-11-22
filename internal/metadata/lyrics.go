package metadata

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/mynora-dot/singverse-go-backend/internal/platform/querycache"
	"github.com/mynora-dot/singverse-go-backend/internal/platform/store"
)

type lyricsPreview struct {
	Status string         `json:"status"`
	Lyrics map[string]any `json:"lyrics"`
}

func (s *Service) candidateLyricsHTTP(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	preview, err := s.candidateLyrics(c.Request.Context(), c.Param("songId"), c.Param("candidateId"))
	if err != nil {
		s.fail(c, err)
		return
	}
	c.JSON(http.StatusOK, preview)
}

func (s *Service) candidateLyrics(ctx context.Context, songID, candidateID string) (lyricsPreview, error) {
	song, err := s.song(ctx, s.db, songID)
	if err != nil {
		return lyricsPreview{}, err
	}
	fp := fingerprint(song)
	track, err := s.ownedCandidate(ctx, s.db, songID, candidateID, fp)
	if err != nil {
		return lyricsPreview{}, err
	}
	if track.Lyrics != nil {
		return lyricsPreview{Status: "AVAILABLE", Lyrics: track.Lyrics}, nil
	}
	if track.Provider == nil || track.ExternalID == nil {
		return lyricsPreview{Status: "UNSUPPORTED"}, nil
	}
	provider := *track.Provider
	switch provider {
	case "NETEASE", "QQ", "KUGOU", "KUWO", "MIGU":
	default:
		return lyricsPreview{Status: "UNSUPPORTED"}, nil
	}
	settings, err := s.settings(ctx, s.db, true)
	if err != nil {
		return lyricsPreview{}, err
	}
	if !contains(settings.Providers, provider) {
		return lyricsPreview{}, conflict("平台已停用")
	}
	adapter := musicProvider{service: s, name: provider}
	key := querycache.Key("lyrics", provider, *track.ExternalID, track.LyricsPath)
	lyrics, err := querycache.Load(ctx, &s.queryCache, key, func(ctx context.Context) (map[string]any, time.Duration, error) {
		value, err := guarded(ctx, s, provider, func(ctx context.Context) (map[string]any, error) {
			return adapter.lyrics(ctx, track)
		})
		if value == nil {
			return nil, time.Minute, err
		}
		return value, 15 * time.Minute, err
	})
	if err != nil {
		return lyricsPreview{}, err
	}
	if lyrics == nil {
		return lyricsPreview{Status: "MISSING"}, nil
	}
	// Only the first successful preview for this candidate is saved. Apply uses
	// those exact bytes even if the platform changes the lyrics later.
	err = s.transaction(ctx, func(tx store.DBTX) error {
		song, err := s.song(ctx, tx, songID)
		if err != nil {
			return err
		}
		stored, err := s.ownedCandidate(ctx, tx, songID, candidateID, fingerprint(song))
		if err != nil {
			return err
		}
		if stored.Lyrics != nil {
			lyrics = stored.Lyrics
			return nil
		}
		stored.Lyrics = lyrics
		_, err = s.exec(ctx, tx, "meta-saveCandidateLyrics", map[string]any{"id": candidateID, "track": encode(stored)})
		return err
	})
	if err != nil {
		return lyricsPreview{}, err
	}
	return lyricsPreview{Status: "AVAILABLE", Lyrics: lyrics}, nil
}
