package lxruntime

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/mynora-dot/singverse-go-backend/internal/platform/querycache"
)

type ipLookup func(context.Context, string) ([]net.IPAddr, error)

var fakeDNSRange = netip.MustParsePrefix("198.18.0.0/15")

// PublicCatalogTransport can recover from TUN fake-IP DNS for known platform
// hosts. Both resolvers' results still pass the public IP and pinned-dial checks.
func PublicCatalogTransport(knownHost func(string) bool) *http.Transport {
	n := newNetwork()
	dns := &publicDNS{client: &http.Client{
		Transport:     &http.Transport{ForceAttemptHTTP2: true, IdleConnTimeout: 30 * time.Second},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
	primary := n.lookup
	n.lookup = func(ctx context.Context, host string) ([]net.IPAddr, error) {
		return catalogLookup(ctx, host, knownHost, primary, dns.lookup)
	}
	transport := n.client.Transport.(*http.Transport)
	transport.ForceAttemptHTTP2 = true
	transport.DisableKeepAlives = false
	transport.MaxIdleConns = 32
	transport.MaxIdleConnsPerHost = 4
	transport.IdleConnTimeout = 30 * time.Second
	return transport
}

func catalogLookup(ctx context.Context, host string, knownHost func(string) bool, system, fallback ipLookup) ([]net.IPAddr, error) {
	addresses, err := system(ctx, host)
	if err != nil || len(addresses) == 0 || len(addresses) > 64 || !knownHost(host) {
		return addresses, err
	}
	for _, address := range addresses {
		ip, ok := netip.AddrFromSlice(address.IP)
		if !ok || address.Zone != "" || !fakeDNSRange.Contains(ip.Unmap()) {
			return addresses, nil
		}
	}
	return fallback(ctx, host)
}

type publicDNS struct {
	client *http.Client
	cache  querycache.Cache
}

func (r *publicDNS) lookup(ctx context.Context, host string) ([]net.IPAddr, error) {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	return querycache.Load(ctx, &r.cache, host, func(ctx context.Context) ([]net.IPAddr, time.Duration, error) {
		ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		addresses, ttl, err := r.query(ctx, host, "A", 1)
		if err == nil && len(addresses) == 0 {
			return r.query(ctx, host, "AAAA", 28)
		}
		return addresses, ttl, err
	})
}

func (r *publicDNS) query(ctx context.Context, host, record string, recordType int) ([]net.IPAddr, time.Duration, error) {
	params := url.Values{"name": {host}, "type": {record}}
	// The resolver is contacted at its public IP with normal TLS certificate verification.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://1.1.1.1/dns-query?"+params.Encode(), nil)
	if err != nil {
		return nil, 0, ErrPublicDNS
	}
	req.Header.Set("Accept", "application/dns-json")
	res, err := r.client.Do(req)
	if err != nil {
		return nil, 0, ErrPublicDNS
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, 0, ErrPublicDNS
	}
	var reply struct {
		Status *int
		TC     bool
		Answer []struct {
			Type int
			TTL  uint32
			Data string
		}
	}
	decoder := json.NewDecoder(io.LimitReader(res.Body, 32*1024))
	var trailing any
	if decoder.Decode(&reply) != nil || decoder.Decode(&trailing) != io.EOF || reply.Status == nil || *reply.Status != 0 || reply.TC || len(reply.Answer) > 64 {
		return nil, 0, ErrPublicDNS
	}
	addresses := []net.IPAddr{}
	ttl := time.Minute
	for _, answer := range reply.Answer {
		ttl = min(ttl, time.Duration(answer.TTL)*time.Second)
		if answer.Type != recordType {
			continue
		}
		ip, err := netip.ParseAddr(answer.Data)
		if err != nil || !publicAddress(ip) || (recordType == 1) != ip.Is4() {
			return nil, 0, ErrPublicDNS
		}
		addresses = append(addresses, net.IPAddr{IP: ip.AsSlice()})
	}
	return addresses, ttl, nil
}
