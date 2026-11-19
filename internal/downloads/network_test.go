package downloads

import (
	"bytes"
	"context"
	"errors"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestDownloadNetworkCancellationTruncationAndDiskGuard(t *testing.T) {
	for _, mode := range []string{"cancel", "truncated", "invalid", "success"} {
		t.Run(mode, func(t *testing.T) {
			network := NewNetwork()
			defer network.Close()
			content := makeWAV()
			declared := int64(len(content))
			if mode == "truncated" {
				declared++
			}
			if mode == "invalid" {
				content = bytes.Repeat([]byte("<html>not audio</html>"), 5)
				declared = int64(len(content))
			}
			network.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(content)), ContentLength: declared, Header: http.Header{}, Request: r}, nil
			})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			path := filepath.Join(t.TempDir(), "audio.part")
			result, err := network.Download(ctx, "https://audio.example/file", path, func(int64, *int64) error {
				if mode == "cancel" {
					cancel()
					return ctx.Err()
				}
				return nil
			})
			if mode == "success" {
				if err != nil || result.Extension != "wav" || result.Size != int64(len(content)) || len(result.Hash) != 64 {
					t.Fatal(result, err)
				}
				if got, readErr := os.ReadFile(path); readErr != nil || !bytes.Equal(got, content) {
					t.Fatal("completed download differs", readErr)
				}
			} else if err == nil {
				t.Fatal("incomplete or cancelled transfer reported success")
			} else if mode == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
		})
	}
	if err := freeSpace(t.TempDir(), math.MaxInt64); statusError(err) != 409 {
		t.Fatal("insufficient-space guard did not reject allocation", err)
	}
}

func TestDownloadNetworkRejectsPrivateRedirectBeforeRequest(t *testing.T) {
	network := NewNetwork()
	defer network.Close()
	calls := 0
	network.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: http.StatusFound, Body: http.NoBody, Header: http.Header{"Location": {"http://127.0.0.1/secret"}}, Request: r}, nil
	})
	if _, err := network.Request(context.Background(), "https://audio.example/file", "GET", nil, nil, 1000, false); err == nil || calls != 1 {
		t.Fatal("private redirect was requested", calls, err)
	}
}

func TestDownloadReportsHeadersBeforeReadingAudio(t *testing.T) {
	network := NewNetwork()
	defer network.Close()
	audio := makeWAV()
	var received []int64
	network.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: &progressCheckingBody{Reader: bytes.NewReader(audio), beforeRead: func() {
			if len(received) == 0 || received[0] != 0 {
				t.Fatal("no transfer progress before body read", received)
			}
		}}, ContentLength: int64(len(audio)), Header: make(http.Header), Request: r}, nil
	})
	_, err := network.Download(t.Context(), "https://audio.example/file", filepath.Join(t.TempDir(), "audio.part"), func(n int64, total *int64) error {
		if total == nil || *total != int64(len(audio)) {
			t.Fatal("missing transfer size", total)
		}
		received = append(received, n)
		return nil
	})
	if err != nil || received[len(received)-1] != int64(len(audio)) {
		t.Fatal(received, err)
	}
}

type progressCheckingBody struct {
	*bytes.Reader
	beforeRead func()
}

func TestNetworkRetriesOnlyReadConnectionFailures(t *testing.T) {
	for _, method := range []string{"GET", "HEAD", "POST"} {
		t.Run(method, func(t *testing.T) {
			n := NewNetwork()
			defer n.Close()
			calls := 0
			n.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				if calls == 1 {
					return nil, errors.New("transient connection failure")
				}
				return &http.Response{StatusCode: 200, Body: http.NoBody, Header: make(http.Header), Request: r}, nil
			})
			_, err := n.Request(t.Context(), "https://audio.example/file", method, nil, nil, 100, false)
			if method == "POST" {
				if calls != 1 || err == nil {
					t.Fatal("write request retried", calls, err)
				}
			} else if calls != 2 || err != nil {
				t.Fatal("read connection was not retried once", calls, err)
			}
		})
	}
}

func TestNetworkDoesNotRetryCancellationOrHTTPFailure(t *testing.T) {
	for _, mode := range []string{"cancel", "403", "503", "connection"} {
		t.Run(mode, func(t *testing.T) {
			n := NewNetwork()
			defer n.Close()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			calls := 0
			n.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				if mode == "cancel" {
					cancel()
					return nil, context.Canceled
				}
				if mode == "connection" {
					return nil, errors.New("connection refused")
				}
				code := 403
				if mode == "503" {
					code = 503
				}
				return &http.Response{StatusCode: code, Body: http.NoBody, Header: make(http.Header), Request: r}, nil
			})
			_, err := n.Request(ctx, "https://audio.example/file", "GET", nil, nil, 100, false)
			want := 1
			if mode == "connection" {
				want = 2
			}
			if calls != want {
				t.Fatal("unexpected retry count", calls, want)
			}
			if mode == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
		})
	}
}

func (r *progressCheckingBody) Read(p []byte) (int, error) { r.beforeRead(); return r.Reader.Read(p) }
func (*progressCheckingBody) Close() error                 { return nil }
