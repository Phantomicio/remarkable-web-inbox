package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
)

func TestPublicWebIP(t *testing.T) {
	for _, raw := range []string{"127.0.0.1", "10.11.99.1", "192.168.1.1", "172.16.0.1", "169.254.169.254", "100.64.0.1", "0.0.0.1", "192.0.2.1", "198.18.0.1", "224.0.0.1", "255.255.255.255", "::", "::1", "::ffff:127.0.0.1", "fc00::1", "fe80::1", "2001:db8::1", "64:ff9b::a00:1", "2002:a00:1::1", "3fff::1"} {
		if publicWebIP(netip.MustParseAddr(raw)) {
			t.Errorf("accepted %s", raw)
		}
	}
	for _, raw := range []string{"1.1.1.1", "8.8.8.8", "2606:4700:4700::1111", "2001:4860:4860::8888"} {
		if !publicWebIP(netip.MustParseAddr(raw)) {
			t.Errorf("rejected %s", raw)
		}
	}
}

func TestWebDialPinsValidatedIP(t *testing.T) {
	lookups := 0
	lookup := func(_ context.Context, _, host string) ([]netip.Addr, error) {
		lookups++
		if host != "article.test" {
			t.Fatalf("host=%q", host)
		}
		if lookups > 1 {
			return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
		}
		return []netip.Addr{netip.MustParseAddr("1.1.1.1")}, nil
	}
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	dial := func(_ context.Context, _, address string) (net.Conn, error) {
		if address != "1.1.1.1:443" {
			t.Fatalf("unchecked destination: %q", address)
		}
		return a, nil
	}
	conn, err := dialPublicWeb(context.Background(), "tcp", "article.test:443", lookup, dial)
	if err != nil || conn != a || lookups != 1 {
		t.Fatalf("lookups=%d err=%v", lookups, err)
	}
}

func TestWebDialRejectsMixedAndEmptyDNSAnswers(t *testing.T) {
	for _, addresses := range [][]netip.Addr{nil, {netip.MustParseAddr("1.1.1.1"), netip.MustParseAddr("10.0.0.1")}} {
		lookup := func(context.Context, string, string) ([]netip.Addr, error) { return addresses, nil }
		dial := func(context.Context, string, string) (net.Conn, error) {
			t.Fatal("dialed an unsafe answer")
			return nil, nil
		}
		if _, err := dialPublicWeb(context.Background(), "tcp", "article.test:80", lookup, dial); err == nil {
			t.Fatal("unsafe DNS answer accepted")
		}
	}
}

func TestWebRedirectCannotReachPrivateIP(t *testing.T) {
	hits := 0
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		http.Redirect(w, r, "http://private.test/secret", http.StatusFound)
	}))
	defer origin.Close()
	client := newPublicWebClient()
	defer client.CloseIdleConnections()
	transport := client.Transport.(*http.Transport)
	if transport.Proxy != nil {
		t.Fatal("web fetch must not use environment proxies")
	}
	lookup := func(_ context.Context, _, host string) ([]netip.Addr, error) {
		if host == "public.test" {
			return []netip.Addr{netip.MustParseAddr("1.1.1.1")}, nil
		}
		return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
	}
	dial := func(ctx context.Context, network, address string) (net.Conn, error) {
		if address != "1.1.1.1:80" {
			return nil, fmt.Errorf("unchecked address %s", address)
		}
		return (&net.Dialer{}).DialContext(ctx, network, origin.Listener.Addr().String())
	}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		return dialPublicWeb(ctx, network, address, lookup, dial)
	}
	if resp, err := client.Get("http://public.test/"); err == nil {
		resp.Body.Close()
		t.Fatal("private redirect succeeded")
	}
	if hits != 1 {
		t.Fatalf("origin received %d requests", hits)
	}
}

func TestInstallationHealthResponse(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
		ok     bool
	}{{200, "ok\n", true}, {200, "wrong service", false}, {503, "importer unavailable", false}, {401, "wrong key", false}} {
		t.Run(fmt.Sprint(tc.status, tc.body), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("X-Web-Inbox-Key") != "PAIR" {
					t.Error("missing authentication")
				}
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer srv.Close()
			err := checkEndpoint(srv.Client(), srv.URL, "PAIR")
			if (err == nil) != tc.ok {
				t.Fatalf("err=%v", err)
			}
		})
	}
}
