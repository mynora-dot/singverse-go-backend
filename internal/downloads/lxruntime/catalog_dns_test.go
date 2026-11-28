package lxruntime

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
)

func TestCatalogDNSFallbackPinsOnlyPublicAddresses(t *testing.T) {
	for _, tc := range []struct {
		name       string
		host       string
		primary    []string
		fallback   []string
		wantLookup bool
		wantDial   string
	}{
		{"direct", "music.163.com", []string{"8.8.8.8"}, nil, false, "8.8.8.8:443"},
		{"fake DNS", "music.163.com", []string{"198.18.0.12"}, []string{"8.8.8.8"}, true, "8.8.8.8:443"},
		{"external host", "other.example", []string{"198.18.0.12"}, nil, false, ""},
		{"mixed DNS", "music.163.com", []string{"198.18.0.12", "127.0.0.1"}, nil, false, ""},
		{"invalid fallback", "music.163.com", []string{"198.18.0.12"}, []string{"8.8.8.8", "127.0.0.1"}, true, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ips := func(raw []string) []net.IPAddr {
				out := make([]net.IPAddr, 0, len(raw))
				for _, address := range raw {
					out = append(out, net.IPAddr{IP: net.ParseIP(address)})
				}
				return out
			}
			n := newNetwork()
			lookup := false
			n.lookup = func(ctx context.Context, host string) ([]net.IPAddr, error) {
				return catalogLookup(ctx, host, func(host string) bool { return host == "music.163.com" },
					func(context.Context, string) ([]net.IPAddr, error) { return ips(tc.primary), nil },
					func(context.Context, string) ([]net.IPAddr, error) {
						lookup = true
						return ips(tc.fallback), nil
					})
			}
			var dialed string
			n.dial = func(_ context.Context, _, address string) (net.Conn, error) {
				dialed = address
				a, b := net.Pipe()
				b.Close()
				return a, nil
			}
			conn, err := n.dialPublic(t.Context(), "tcp", tc.host+":443")
			if conn != nil {
				conn.Close()
			}
			if lookup != tc.wantLookup || dialed != tc.wantDial || (tc.wantDial == "") != errors.Is(err, ErrPublicDNS) {
				t.Fatalf("fallback=%t dialed=%q error=%v", lookup, dialed, err)
			}
		})
	}
}

type dnsRoundTrip func(*http.Request) (*http.Response, error)

func (f dnsRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestCatalogDNSResolverRejectsNonPublicAnswers(t *testing.T) {
	dns := &publicDNS{client: &http.Client{Transport: dnsRoundTrip(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host != "1.1.1.1" || req.URL.Query().Get("name") != "music.163.com" || req.Header.Get("Accept") != "application/dns-json" {
			t.Errorf("unexpected DNS request %s", req.URL)
		}
		body := `{"Status":0,"Answer":[{"type":1,"TTL":30,"data":"8.8.8.8"},{"type":1,"TTL":30,"data":"198.18.1.1"}]}`
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}}
	if addresses, err := dns.lookup(t.Context(), "music.163.com"); !errors.Is(err, ErrPublicDNS) || len(addresses) != 0 {
		t.Fatalf("addresses=%v error=%v", addresses, err)
	}
}
