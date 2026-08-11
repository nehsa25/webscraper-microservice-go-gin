//go:build integration

// Package integration_test exercises the seams: real router, real HTTP client,
// real TCP listener, real HTML parsing — with only DNS replaced, so the tests
// can point at a loopback server without switching the SSRF guard off.
package integration_test

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/nehsa-net/webscraper-microservice-go-gin/internal/httpapi"
	"github.com/nehsa-net/webscraper-microservice-go-gin/internal/scraper"
)

func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	m.Run()
}

const samplePage = `<!doctype html>
<html>
  <head><title>Sample</title></head>
  <body>
    <header>ignore me</header>
    <div id="main"><h1>Main content</h1><p>The interesting part.</p></div>
    <footer>ignore me too</footer>
  </body>
</html>`

// mapResolver resolves whatever the test says, so the guard runs for real
// against a controlled answer.
type mapResolver map[string]string

func (m mapResolver) LookupIPAddr(_ context.Context, host string) ([]net.IPAddr, error) {
	ip, ok := m[host]
	if !ok {
		return nil, fmt.Errorf("no such host: %s", host)
	}
	return []net.IPAddr{{IP: net.ParseIP(ip)}}, nil
}

// newStack wires the real scraper, guard and router together on a real port —
// the assembly main() performs, which unit tests cannot check.
func newStack(t *testing.T, resolver scraper.Resolver) (*httptest.Server, *httptest.Server) {
	t.Helper()

	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		switch r.URL.Path {
		case "/gone":
			w.WriteHeader(http.StatusNotFound)
		case "/plain":
			fmt.Fprint(w, `<html><body><p>no container here</p></body></html>`)
		default:
			fmt.Fprint(w, samplePage)
		}
	}))
	t.Cleanup(page.Close)

	// A client whose dialer always connects to the page server, whatever the
	// hostname says.
	//
	// This is what lets the SSRF guard run FOR REAL in a test: the guard
	// resolves "public.test" through the stub resolver and decides, while the
	// connection still lands on the loopback listener. Pointing the URL
	// straight at 127.0.0.1 instead would force the guard off, and a guard
	// that is disabled in every test is a guard nobody is testing.
	pageAddr := strings.TrimPrefix(page.URL, "http://")
	dialer := &net.Dialer{}
	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return dialer.DialContext(ctx, network, pageAddr)
			},
		},
	}

	guard := &scraper.Guard{Resolver: resolver}
	svc := scraper.New(client, guard)
	app := httptest.NewServer(httpapi.New(svc))
	t.Cleanup(app.Close)

	return app, page
}

func get(t *testing.T, url string) (int, string, http.Header) {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	return resp.StatusCode, string(body), resp.Header
}

// pageHost rewrites the page server's URL to use a name the resolver knows,
// so the request goes through the real guard rather than around it.
func pageHost(t *testing.T, page *httptest.Server, name string) string {
	t.Helper()

	_, port, err := net.SplitHostPort(strings.TrimPrefix(page.URL, "http://"))
	if err != nil {
		t.Fatalf("splitting %q: %v", page.URL, err)
	}
	return fmt.Sprintf("http://%s:%s", name, port)
}

func TestScrapeThroughTheWholeStack(t *testing.T) {
	// The name resolves to a public address, so the guard permits it; the
	// connection still lands on the loopback listener because that is where
	// the port is. This is what lets the guard run for real in a test.
	app, page := newStack(t, mapResolver{"public.test": "93.184.216.34"})

	status, body, header := get(t, app.URL+"/scraper?url="+pageHost(t, page, "public.test")+"/")

	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", status, body)
	}
	if !strings.Contains(body, "The interesting part.") {
		t.Errorf("body did not contain the main content: %s", body)
	}
	// The header and footer sit outside #main and must not come through.
	if strings.Contains(body, "ignore me") {
		t.Errorf("body leaked content from outside #main: %s", body)
	}
	if got := header.Get("X-Matched-Selector"); got != "#main" {
		t.Errorf("X-Matched-Selector = %q, want #main", got)
	}
}

// The security test, end to end. Every one of these would have been fetched by
// the original service.
func TestInternalAddressesAreRefusedThroughTheStack(t *testing.T) {
	app, _ := newStack(t, mapResolver{
		"metadata.test": "169.254.169.254",
		"internal.test": "10.0.0.5",
		"loopback.test": "127.0.0.1",
	})

	for _, host := range []string{"metadata.test", "internal.test", "loopback.test"} {
		t.Run(host, func(t *testing.T) {
			status, body, _ := get(t, app.URL+"/scraper?url=http://"+host+"/")

			if status != http.StatusForbidden {
				t.Fatalf("status = %d, want 403 (body: %s)", status, body)
			}
			// The refusal must not confirm what the name resolved to.
			for _, forbidden := range []string{"169.254", "10.0.0", "127.0.0.1", "resolves"} {
				if strings.Contains(body, forbidden) {
					t.Errorf("403 body leaked %q: %s", forbidden, body)
				}
			}
		})
	}
}

func TestNonHTTPSchemesAreRefusedThroughTheStack(t *testing.T) {
	app, _ := newStack(t, mapResolver{})

	for _, raw := range []string{"file:///etc/passwd", "gopher://x/", "ftp://x/"} {
		t.Run(raw, func(t *testing.T) {
			status, body, _ := get(t, app.URL+"/scraper?url="+raw)

			if status != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body: %s)", status, body)
			}
		})
	}
}

func TestMissingURLIsA400ThroughTheStack(t *testing.T) {
	app, _ := newStack(t, mapResolver{})

	status, body, _ := get(t, app.URL+"/scraper")

	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body: %s)", status, body)
	}
}

func TestUpstream404BecomesA502ThroughTheStack(t *testing.T) {
	app, page := newStack(t, mapResolver{"public.test": "93.184.216.34"})

	status, body, _ := get(t, app.URL+"/scraper?url="+pageHost(t, page, "public.test")+"/gone")

	// The page said 404; that is an upstream failure from this service's point
	// of view, not a missing resource on this service.
	if status != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 (body: %s)", status, body)
	}
}

// The whole-document fallback, verified end to end: a page with none of the
// content containers still returns something, tagged as the html fallback.
func TestFallsBackToTheWholeDocument(t *testing.T) {
	app, page := newStack(t, mapResolver{"public.test": "93.184.216.34"})

	status, body, header := get(t, app.URL+"/scraper?url="+pageHost(t, page, "public.test")+"/plain")

	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", status, body)
	}
	if got := header.Get("X-Matched-Selector"); got != "html" {
		t.Errorf("X-Matched-Selector = %q, want html", got)
	}
	if !strings.Contains(body, "no container here") {
		t.Errorf("body = %q, want the page content", body)
	}
}

func TestProbesAnswerWithoutFetchingAnything(t *testing.T) {
	app, _ := newStack(t, mapResolver{})

	for _, path := range []string{"/health", "/ready"} {
		t.Run(path, func(t *testing.T) {
			if status, body, _ := get(t, app.URL+path); status != http.StatusOK {
				t.Errorf("status = %d, want 200 (body: %s)", status, body)
			}
		})
	}
}
