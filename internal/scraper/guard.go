package scraper

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strings"
)

// Resolver looks up the addresses a hostname points at.
//
// It is an interface so a test can decide what a name resolves to, which is the
// only way to test the guard below without depending on live DNS.
type Resolver interface {
	LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error)
}

// Guard decides whether a URL may be fetched.
//
// A service that fetches an arbitrary caller-supplied URL is a
// server-side request forgery (SSRF) primitive: without this, anyone who can
// reach the endpoint can use it to read the cloud metadata service at
// 169.254.169.254 — which on most providers hands out credentials — or to probe
// any host inside the network that the caller cannot reach directly.
//
// The original service had no validation of any kind.
type Guard struct {
	Resolver Resolver
	// AllowPrivate disables the address checks. It exists for local
	// development and for the tests, and must stay false in production.
	AllowPrivate bool
}

// NewGuard returns a Guard backed by the system resolver.
func NewGuard(allowPrivate bool) *Guard {
	return &Guard{Resolver: net.DefaultResolver, AllowPrivate: allowPrivate}
}

// Check parses raw and reports whether it is safe to fetch.
func (g *Guard) Check(ctx context.Context, raw string) (*url.URL, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, ErrInvalidURL
	}

	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidURL, err)
	}

	// Reject anything that is not http(s) BEFORE looking at the host.
	// file:// would read the container's filesystem; gopher:// and dict:// are
	// classic SSRF pivots into other protocols.
	switch parsed.Scheme {
	case "http", "https":
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnsupportedURL, parsed.Scheme)
	}

	host := parsed.Hostname()
	if host == "" {
		return nil, ErrInvalidURL
	}

	if g.AllowPrivate {
		return parsed, nil
	}

	addrs, err := g.Resolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("%w: resolving %q: %w", ErrInvalidURL, host, err)
	}
	if len(addrs) == 0 {
		return nil, fmt.Errorf("%w: %q resolves to nothing", ErrInvalidURL, host)
	}

	// EVERY address must be allowed, not just the first.
	//
	// A hostname can resolve to several addresses, and an attacker controlling
	// the DNS can return one public and one private. Checking only the first
	// is a bypass. (This still leaves a DNS-rebinding window between the check
	// and the connection; closing that needs a custom dialer that re-checks the
	// address it actually connects to.)
	for _, addr := range addrs {
		if !isPublic(addr.IP) {
			return nil, fmt.Errorf("%w: %q resolves to %s", ErrForbiddenHost, host, addr.IP)
		}
	}
	return parsed, nil
}

// isPublic reports whether ip is a routable public address.
func isPublic(ip net.IP) bool {
	if ip == nil || ip.IsUnspecified() {
		return false
	}
	// Covers 127.0.0.0/8 and ::1
	if ip.IsLoopback() {
		return false
	}
	// Covers 169.254.0.0/16 — including 169.254.169.254, the cloud metadata
	// endpoint, which is the single most valuable SSRF target there is.
	if ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return false
	}
	// Covers 10/8, 172.16/12, 192.168/16, and fc00::/7
	if ip.IsPrivate() {
		return false
	}
	if ip.IsMulticast() || ip.IsInterfaceLocalMulticast() {
		return false
	}
	return true
}
