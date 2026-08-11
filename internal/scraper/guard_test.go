package scraper_test

import (
	"context"
	"errors"
	"net"
	"testing"

	"github.com/nehsa-net/webscraper-microservice-go-gin/internal/scraper"
)

// stubResolver decides what a hostname resolves to.
//
// This is the seam that makes the guard testable at all: without it, every one
// of these cases would depend on live DNS returning what the test assumed, and
// the metadata-endpoint case could not be written without a cloud instance.
type stubResolver struct {
	addrs map[string][]net.IPAddr
	err   error
}

func (s stubResolver) LookupIPAddr(_ context.Context, host string) ([]net.IPAddr, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.addrs[host], nil
}

func ips(values ...string) []net.IPAddr {
	addrs := make([]net.IPAddr, 0, len(values))
	for _, v := range values {
		addrs = append(addrs, net.IPAddr{IP: net.ParseIP(v)})
	}
	return addrs
}

func guardWith(addrs map[string][]net.IPAddr) *scraper.Guard {
	return &scraper.Guard{Resolver: stubResolver{addrs: addrs}}
}

// The security test that matters most.
//
// A service that fetches an arbitrary caller-supplied URL is an SSRF primitive.
// Each address below is a real, commonly-exploited target, and the original
// service would have fetched every one of them.
func TestGuardRejectsInternalAddresses(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		host string
		ip   string
		why  string
	}{
		{
			name: "cloud metadata endpoint",
			host: "metadata.test",
			ip:   "169.254.169.254",
			why:  "hands out instance credentials on most cloud providers",
		},
		{name: "ipv4 loopback", host: "local.test", ip: "127.0.0.1", why: "reaches services bound to localhost"},
		{name: "ipv6 loopback", host: "local6.test", ip: "::1", why: "reaches services bound to localhost"},
		{name: "private 10/8", host: "ten.test", ip: "10.0.0.5", why: "internal network"},
		{name: "private 172.16/12", host: "onesevtwo.test", ip: "172.16.0.5", why: "internal network"},
		{name: "private 192.168/16", host: "home.test", ip: "192.168.1.1", why: "internal network"},
		{name: "unique local ipv6", host: "ula.test", ip: "fd00::1", why: "internal network"},
		{name: "link-local ipv4", host: "ll.test", ip: "169.254.1.1", why: "link-local"},
		{name: "unspecified", host: "zero.test", ip: "0.0.0.0", why: "resolves to every local interface"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			guard := guardWith(map[string][]net.IPAddr{tc.host: ips(tc.ip)})

			_, err := guard.Check(context.Background(), "http://"+tc.host+"/")

			if !errors.Is(err, scraper.ErrForbiddenHost) {
				t.Fatalf("Check(%s -> %s) error = %v, want ErrForbiddenHost (%s)",
					tc.host, tc.ip, err, tc.why)
			}
		})
	}
}

// A hostname can resolve to several addresses, and an attacker controlling the
// DNS can return one public and one private. Checking only the first is a
// bypass, so this test pins "every address must pass".
func TestGuardRejectsWhenAnyAddressIsInternal(t *testing.T) {
	t.Parallel()

	guard := guardWith(map[string][]net.IPAddr{
		"mixed.test": ips("93.184.216.34", "10.0.0.5"), // one public, one private
	})

	_, err := guard.Check(context.Background(), "https://mixed.test/page")

	if !errors.Is(err, scraper.ErrForbiddenHost) {
		t.Fatalf("Check() error = %v, want ErrForbiddenHost — the private address must veto", err)
	}
}

func TestGuardAllowsPublicAddresses(t *testing.T) {
	t.Parallel()

	guard := guardWith(map[string][]net.IPAddr{
		"example.test": ips("93.184.216.34", "2606:2800:220:1:248:1893:25c8:1946"),
	})

	parsed, err := guard.Check(context.Background(), "https://example.test/some/page?q=1")
	if err != nil {
		t.Fatalf("Check() unexpected error: %v", err)
	}
	if parsed.Host != "example.test" {
		t.Errorf("Host = %q, want example.test", parsed.Host)
	}
	if parsed.Path != "/some/page" {
		t.Errorf("Path = %q, want /some/page", parsed.Path)
	}
}

// Non-http schemes are rejected before the host is even looked at. file://
// would read the container's filesystem; gopher:// and dict:// are classic
// pivots into other protocols.
func TestGuardRejectsNonHTTPSchemes(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{
		"file:///etc/passwd",
		"gopher://example.test/",
		"dict://example.test:11211/",
		"ftp://example.test/",
		"jar:http://example.test!/",
	} {
		t.Run(raw, func(t *testing.T) {
			t.Parallel()

			guard := guardWith(map[string][]net.IPAddr{})

			_, err := guard.Check(context.Background(), raw)

			if !errors.Is(err, scraper.ErrUnsupportedURL) {
				t.Fatalf("Check(%q) error = %v, want ErrUnsupportedURL", raw, err)
			}
		})
	}
}

func TestGuardRejectsMalformedURLs(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{"", "   ", "not a url", "http://", "://example.test"} {
		t.Run(raw, func(t *testing.T) {
			t.Parallel()

			guard := guardWith(map[string][]net.IPAddr{})

			_, err := guard.Check(context.Background(), raw)

			if err == nil {
				t.Fatalf("Check(%q) succeeded, want an error", raw)
			}
			// Either error is acceptable; silently fetching is not.
			if !errors.Is(err, scraper.ErrInvalidURL) && !errors.Is(err, scraper.ErrUnsupportedURL) {
				t.Errorf("Check(%q) error = %v, want ErrInvalidURL or ErrUnsupportedURL", raw, err)
			}
		})
	}
}

func TestGuardRejectsUnresolvableHosts(t *testing.T) {
	t.Parallel()

	guard := &scraper.Guard{Resolver: stubResolver{err: errors.New("no such host")}}

	_, err := guard.Check(context.Background(), "https://nowhere.test/")

	if !errors.Is(err, scraper.ErrInvalidURL) {
		t.Fatalf("Check() error = %v, want ErrInvalidURL", err)
	}
}

func TestGuardRejectsHostsResolvingToNothing(t *testing.T) {
	t.Parallel()

	guard := guardWith(map[string][]net.IPAddr{"empty.test": {}})

	_, err := guard.Check(context.Background(), "https://empty.test/")

	if !errors.Is(err, scraper.ErrInvalidURL) {
		t.Fatalf("Check() error = %v, want ErrInvalidURL", err)
	}
}

// AllowPrivateHosts exists for local development. This test pins that it is
// opt-in, so nobody can argue later that the default was permissive.
func TestGuardAllowPrivateSkipsTheChecks(t *testing.T) {
	t.Parallel()

	guard := &scraper.Guard{
		Resolver:     stubResolver{addrs: map[string][]net.IPAddr{"local.test": ips("127.0.0.1")}},
		AllowPrivate: true,
	}

	if _, err := guard.Check(context.Background(), "http://local.test:8080/"); err != nil {
		t.Fatalf("Check() with AllowPrivate error = %v, want it permitted", err)
	}
}

func TestGuardStillRejectsBadSchemesWhenPrivateIsAllowed(t *testing.T) {
	t.Parallel()

	guard := &scraper.Guard{Resolver: stubResolver{}, AllowPrivate: true}

	// AllowPrivate relaxes the ADDRESS check only. file:// must stay refused
	// even in development, or a dev-mode deployment reads its own filesystem.
	_, err := guard.Check(context.Background(), "file:///etc/passwd")

	if !errors.Is(err, scraper.ErrUnsupportedURL) {
		t.Fatalf("Check() error = %v, want ErrUnsupportedURL", err)
	}
}
