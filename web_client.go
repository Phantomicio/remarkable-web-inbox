package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"time"
)

// Reject non-public and special-purpose destinations, including IPv4-mapped
// IPv6. Do not allow proxies: a proxy could resolve a host differently from us.
var blockedWebRanges = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("224.0.0.0/4"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("2001::/23"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("3fff::/20"),
}

func publicWebIP(ip netip.Addr) bool {
	if !ip.IsValid() || ip.Zone() != "" {
		return false
	}
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() || ip.IsPrivate() {
		return false
	}
	// Only current global IPv6 unicast space; excludes local NAT64 prefixes.
	if ip.Is6() && !netip.MustParsePrefix("2000::/3").Contains(ip) {
		return false
	}
	for _, prefix := range blockedWebRanges {
		if prefix.Contains(ip) {
			return false
		}
	}
	return true
}

type lookupWebIP func(context.Context, string, string) ([]netip.Addr, error)
type dialWebIP func(context.Context, string, string) (net.Conn, error)

func dialPublicWeb(ctx context.Context, network, address string, lookup lookupWebIP, dial dialWebIP) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	addresses, err := lookup(ctx, "ip", host)
	if err != nil || len(addresses) == 0 {
		return nil, fmt.Errorf("could not resolve website host")
	}
	// Validate the entire answer before making any connection.
	for _, ip := range addresses {
		if !publicWebIP(ip) {
			return nil, fmt.Errorf("local, private and special-use network URLs are not allowed")
		}
	}
	var lastErr error
	for _, ip := range addresses {
		// Dial the checked IP, never the hostname (no second DNS lookup).
		conn, err := dial(ctx, network, net.JoinHostPort(ip.Unmap().String(), port))
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

func newPublicWebClient() *http.Client {
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	transport := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			return dialPublicWeb(ctx, network, address, net.DefaultResolver.LookupNetIP, dialer.DialContext)
		},
		TLSHandshakeTimeout:    10 * time.Second,
		ResponseHeaderTimeout:  15 * time.Second,
		MaxResponseHeaderBytes: 32 << 10,
	}
	return &http.Client{
		Transport: transport, Timeout: 30 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 6 {
				return fmt.Errorf("too many redirects")
			}
			if (req.URL.Scheme != "http" && req.URL.Scheme != "https") || req.URL.User != nil {
				return fmt.Errorf("invalid redirect URL")
			}
			// Each new connection (including redirects) uses the checked dialer.
			return nil
		},
	}
}
