package library

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

type analysisTransport func(*http.Request) (*http.Response, error)

func (f analysisTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type unreadAnalysisBody struct{ reads, closes int }

func (b *unreadAnalysisBody) Read([]byte) (int, error) { b.reads++; return 0, io.EOF }
func (b *unreadAnalysisBody) Close() error             { b.closes++; return nil }

func TestAnalysisRejectsIgnoredRangeBeforeReadingResponseBody(t *testing.T) {
	for _, test := range []struct{ upstream, expected int }{{200, 422}, {401, 401}, {403, 401}, {404, 410}, {412, 409}, {429, 429}} {
		t.Run(http.StatusText(test.upstream), func(t *testing.T) {
			body := &unreadAnalysisBody{}
			s := &Service{client: &http.Client{Transport: analysisTransport(func(r *http.Request) (*http.Response, error) {
				if r.Header.Get("Range") != "bytes=0-7" {
					t.Fatal(r.Header)
				}
				return &http.Response{StatusCode: test.upstream, Header: make(http.Header), ContentLength: 1 << 30, Body: body}, nil
			})}}
			r := httptest.NewRequest("GET", "/input", nil)
			r.Header.Set("Range", "bytes=0-7")
			w := httptest.NewRecorder()
			err := s.remoteOriginal(WithAnalysisRead(t.Context()), w, r, Song{RelativePath: "song.mp4"}, Source{Location: "https://example.test/media/"}, nil)
			var p *problem
			if !errors.As(err, &p) || p.status != test.expected {
				t.Fatal(err)
			}
			if body.reads != 0 || body.closes != 1 || w.Body.Len() != 0 {
				t.Fatal("analysis consumed an unbounded/failing response", body, w.Body.Len())
			}
		})
	}
}
