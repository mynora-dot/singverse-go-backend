package downloads

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestAutoQualityFallsBackWhenResolvedAudioIsUnavailable(t *testing.T) {
	for _, code := range []int{http.StatusOK, http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusGone} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			s, root, track := fixture(t)
			defer s.Close()
			batch := createOne(t, s, track)
			id := str(itemFor(t, s, batch), "id")
			runtime := s.runtime.(*testRuntime)
			runtime.resolve = func(_ context.Context, quality string) (string, error) {
				if quality == "128k" {
					row := itemFor(t, s, batch)
					if str(row, "status") != "RESOLVING" || row["resolved_active_key"] != nil || number(row, "received_bytes") != 0 || row["total_bytes"] != nil {
						t.Fatal("previous transfer reservation/progress was not released", row)
					}
				}
				return "https://audio.example/" + quality, nil
			}
			audio := makeWAV()
			s.network.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.URL.Path == "/flac" {
					if code == 200 {
						corrupt := append(makeWAV(), 0)
						return &http.Response{StatusCode: code, Body: io.NopCloser(bytes.NewReader(corrupt)), ContentLength: int64(len(corrupt)), Header: make(http.Header), Request: r}, nil
					}
					return &http.Response{StatusCode: code, Body: http.NoBody, Header: make(http.Header), Request: r}, nil
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(audio)), ContentLength: int64(len(audio)), Header: make(http.Header), Request: r}, nil
			})
			if err := s.download(t.Context(), claim(t, s, id)); err != nil {
				t.Fatal(err)
			}
			row := itemFor(t, s, batch)
			if str(row, "status") != "SUCCESS" || str(row, "resolved_quality") != "128k" || !reflect.DeepEqual(runtime.calls, []string{"flac", "128k"}) {
				t.Fatal("automatic download did not use the working quality", row, runtime.calls)
			}
			data, err := os.ReadFile(filepath.Join(root, str(row, "relative_path")))
			if err != nil || !bytes.Equal(data, audio) {
				t.Fatal("fallback audio not published intact", err)
			}
			var songs int
			if err := s.db.Get(&songs, "SELECT COUNT(*) FROM media_song"); err != nil || songs != 1 {
				t.Fatal("fallback must publish exactly one song", songs, err)
			}
		})
	}
}

func TestManualQualityAndTransientFailuresDoNotDowngrade(t *testing.T) {
	for _, mode := range []string{"manual", "manual-content", "server", "connection", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			s, _, track := fixture(t)
			defer s.Close()
			quality := "auto"
			if mode == "manual" || mode == "manual-content" {
				quality = "flac"
			}
			created, err := s.Create(t.Context(), BatchInput{TrackIDs: []string{track.ID}, Quality: quality})
			if err != nil {
				t.Fatal(err)
			}
			batch := str(created["batch"].(map[string]any), "id")
			id := str(itemFor(t, s, batch), "id")
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			s.network.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if mode == "manual-content" {
					corrupt := append(makeWAV(), 0)
					return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(corrupt)), ContentLength: int64(len(corrupt)), Header: make(http.Header), Request: r}, nil
				}
				if mode == "connection" {
					return nil, errors.New("connection interrupted")
				}
				if mode == "cancel" {
					cancel()
					return nil, context.Canceled
				}
				code := http.StatusServiceUnavailable
				if mode == "manual" {
					code = http.StatusForbidden
				}
				return &http.Response{StatusCode: code, Body: http.NoBody, Header: make(http.Header), Request: r}, nil
			})
			if err := s.download(ctx, claim(t, s, id)); err == nil {
				t.Fatal("unavailable download succeeded")
			}
			if calls := s.runtime.(*testRuntime).calls; !reflect.DeepEqual(calls, []string{"flac"}) {
				t.Fatal("download unexpectedly downgraded", calls)
			}
		})
	}
}
