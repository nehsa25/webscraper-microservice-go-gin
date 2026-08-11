package scraper_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nehsa-net/webscraper-microservice-go-gin/internal/scraper"
)

// permissiveGuard resolves every host to a public address, so these tests can
// use httptest servers (which listen on 127.0.0.1) without disabling the guard
// under test elsewhere.
func permissiveGuard() *scraper.Guard {
	return &scraper.Guard{
		Resolver:     alwaysPublic{},
		AllowPrivate: true,
	}
}

type alwaysPublic struct{}

func (alwaysPublic) LookupIPAddr(context.Context, string) ([]net.IPAddr, error) {
	return []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}, nil
}

func newScraper(t *testing.T, handler http.HandlerFunc) (*scraper.Scraper, string) {
	t.Helper()

	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	return scraper.New(srv.Client(), permissiveGuard()), srv.URL
}

func serveHTML(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, body)
	}
}

// The selector list is the heart of this service, and it was previously a local
// variable inside the fetch function — unreachable from any test. As a package
// variable it becomes a table.
func TestScrapeSelectorPriority(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		html         string
		wantSelector string
		wantContains string
	}{
		{
			name:         "id body wins over everything",
			html:         `<html><body><div id="body">FIRST</div><div id="main">SECOND</div></body></html>`,
			wantSelector: "#body",
			wantContains: "FIRST",
		},
		{
			name:         "id main when there is no id body",
			html:         `<html><body><div id="main">MAIN</div><div id="content">CONTENT</div></body></html>`,
			wantSelector: "#main",
			wantContains: "MAIN",
		},
		{
			name:         "id main-content before id content",
			html:         `<html><body><div id="content">C</div><div id="main-content">MC</div></body></html>`,
			wantSelector: "#main-content",
			wantContains: "MC",
		},
		{
			name:         "class main when no id matches",
			html:         `<html><body><div class="main">CLASSMAIN</div></body></html>`,
			wantSelector: ".main",
			wantContains: "CLASSMAIN",
		},
		{
			name:         "class wrapper is near the end",
			html:         `<html><body><div class="wrapper">WRAP</div></body></html>`,
			wantSelector: ".wrapper",
			wantContains: "WRAP",
		},
		{
			name:         "falls back to the whole document",
			html:         `<html><head><title>T</title></head><body><p>PLAIN</p></body></html>`,
			wantSelector: "html",
			wantContains: "PLAIN",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			s, url := newScraper(t, serveHTML(tc.html))

			got, err := s.Scrape(context.Background(), url)
			if err != nil {
				t.Fatalf("Scrape() unexpected error: %v", err)
			}

			if got.Selector != tc.wantSelector {
				t.Errorf("Selector = %q, want %q", got.Selector, tc.wantSelector)
			}
			if !strings.Contains(got.HTML, tc.wantContains) {
				t.Errorf("HTML = %q, want it to contain %q", got.HTML, tc.wantContains)
			}
		})
	}
}

// A container that exists but is empty must be skipped, not returned. The
// original checked for this; the check is preserved here with a test.
func TestScrapeSkipsEmptyContainers(t *testing.T) {
	t.Parallel()

	s, url := newScraper(t, serveHTML(
		`<html><body><div id="body"></div><div id="main">REAL CONTENT</div></body></html>`))

	got, err := s.Scrape(context.Background(), url)
	if err != nil {
		t.Fatalf("Scrape() unexpected error: %v", err)
	}

	if got.Selector != "#main" {
		t.Errorf("Selector = %q, want #main — the empty #body must be skipped", got.Selector)
	}
}

func TestScrapeSkipsWhitespaceOnlyContainers(t *testing.T) {
	t.Parallel()

	s, url := newScraper(t, serveHTML(
		"<html><body><div id=\"body\">   \n\t </div><div id=\"main\">REAL</div></body></html>"))

	got, err := s.Scrape(context.Background(), url)
	if err != nil {
		t.Fatalf("Scrape() unexpected error: %v", err)
	}
	if got.Selector != "#main" {
		t.Errorf("Selector = %q, want #main", got.Selector)
	}
}

// The empty string used to sit in the middle of the selector list. doc.Find("")
// matches nothing, so it was a no-op that looked like a bug. This pins its
// removal: a list containing "" would make this test's expectations ambiguous.
func TestSelectorListHasNoEmptyEntries(t *testing.T) {
	t.Parallel()

	for i, selector := range scraper.Selectors {
		if strings.TrimSpace(selector) == "" {
			t.Errorf("Selectors[%d] is empty; doc.Find(\"\") matches nothing", i)
		}
	}
	if len(scraper.Selectors) == 0 {
		t.Fatal("Selectors is empty")
	}
	if last := scraper.Selectors[len(scraper.Selectors)-1]; last != "html" {
		t.Errorf("the last selector is %q, want html — the whole-document fallback", last)
	}
}

// Every one of these used to be a panic(), which Gin recovers into a 500 with a
// stack trace in the log — for what is usually just a bad URL.
func TestScrapeErrorsInsteadOfPanicking(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		handler http.HandlerFunc
		wantErr error
	}{
		{
			name: "a 404 from the page",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNotFound)
			},
			wantErr: scraper.ErrUpstream,
		},
		{
			name: "a 500 from the page",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
			},
			wantErr: scraper.ErrUpstream,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			s, url := newScraper(t, tc.handler)

			// If the code under test panics, the test binary fails loudly —
			// which is the point: these inputs used to do exactly that.
			_, err := s.Scrape(context.Background(), url)

			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Scrape() error = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

type stubDoer struct{ err error }

func (s stubDoer) Do(*http.Request) (*http.Response, error) { return nil, s.err }

// An empty page still returns something, because goquery synthesises
// <html><head></head><body></body></html> when it parses one — so the `html`
// fallback always matches and this endpoint essentially never 404s.
//
// That is worth pinning: it is surprising, and somebody will otherwise write a
// test expecting a 404 and conclude the code is broken.
func TestScrapeOnAnEmptyPageReturnsTheSynthesisedDocument(t *testing.T) {
	t.Parallel()

	s, url := newScraper(t, serveHTML(""))

	got, err := s.Scrape(context.Background(), url)
	if err != nil {
		t.Fatalf("Scrape() unexpected error: %v", err)
	}
	if got.Selector != "html" {
		t.Errorf("Selector = %q, want html", got.Selector)
	}
}

// ErrNoContent is therefore only reachable with a narrowed selector list. This
// test covers the branch, and documents how to configure the service so that a
// page without a real content container is reported rather than dumped whole.
func TestScrapeReturnsErrNoContentWhenNoSelectorMatches(t *testing.T) {
	t.Parallel()

	s, url := newScraper(t, serveHTML(`<html><body><p>plain</p></body></html>`))
	s.Selectors = []string{"#body", "#main"} // no html fallback

	_, err := s.Scrape(context.Background(), url)

	if !errors.Is(err, scraper.ErrNoContent) {
		t.Fatalf("Scrape() error = %v, want ErrNoContent", err)
	}
}

func TestScrapeTransportError(t *testing.T) {
	t.Parallel()

	s := scraper.New(stubDoer{err: errors.New("dial tcp: connection refused")}, permissiveGuard())

	_, err := s.Scrape(context.Background(), "https://example.test/")

	if !errors.Is(err, scraper.ErrUpstream) {
		t.Fatalf("Scrape() error = %v, want ErrUpstream", err)
	}
	if !strings.Contains(err.Error(), "connection refused") {
		t.Errorf("error %q lost the underlying cause", err)
	}
}

// The original read the whole body with no limit, so one request to a page that
// streams indefinitely would exhaust the container's memory.
func TestScrapeRejectsOversizedPages(t *testing.T) {
	t.Parallel()

	s, url := newScraper(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		// One byte over the cap.
		fmt.Fprint(w, `<html><body><div id="main">`)
		_, _ = w.Write([]byte(strings.Repeat("x", scraper.MaxBodyBytes)))
		fmt.Fprint(w, `</div></body></html>`)
	})

	_, err := s.Scrape(context.Background(), url)

	if !errors.Is(err, scraper.ErrTooLarge) {
		t.Fatalf("Scrape() error = %v, want ErrTooLarge", err)
	}
}

func TestScrapeAcceptsAPageAtTheLimit(t *testing.T) {
	t.Parallel()

	// A page comfortably under the cap must still work — a limit that rejects
	// ordinary pages is worse than no limit.
	filler := strings.Repeat("y", 1024)
	s, url := newScraper(t, serveHTML(`<html><body><div id="main">`+filler+`</div></body></html>`))

	got, err := s.Scrape(context.Background(), url)
	if err != nil {
		t.Fatalf("Scrape() unexpected error: %v", err)
	}
	if !strings.Contains(got.HTML, filler) {
		t.Error("the page content was truncated")
	}
}

func TestScrapeHonoursContextDeadline(t *testing.T) {
	t.Parallel()

	blocked := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		<-blocked
	}))
	t.Cleanup(func() {
		close(blocked)
		srv.Close()
	})

	s := scraper.New(srv.Client(), permissiveGuard())

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := s.Scrape(ctx, srv.URL)

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want it to wrap context.DeadlineExceeded", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("took %v; the deadline was not honoured", elapsed)
	}
}

func TestScrapeSendsAUserAgent(t *testing.T) {
	t.Parallel()

	var gotAgent, gotAccept string
	s, url := newScraper(t, func(w http.ResponseWriter, r *http.Request) {
		gotAgent = r.Header.Get("User-Agent")
		gotAccept = r.Header.Get("Accept")
		serveHTML(`<html><body><div id="main">X</div></body></html>`)(w, r)
	})

	if _, err := s.Scrape(context.Background(), url); err != nil {
		t.Fatalf("Scrape() unexpected error: %v", err)
	}

	// A scraper with no User-Agent gets blocked by a lot of sites, and is
	// impolite besides — the operator of the page cannot tell who is calling.
	if !strings.Contains(gotAgent, "nehsa-webscraper") {
		t.Errorf("User-Agent = %q, want it to identify the service", gotAgent)
	}
	if !strings.Contains(gotAccept, "text/html") {
		t.Errorf("Accept = %q, want text/html", gotAccept)
	}
}
