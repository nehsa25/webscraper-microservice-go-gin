package httpapi_test

import (
	"context"
	"encoding/json"
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

// fakeService drives every status-code branch with no network and no parsing.
type fakeService struct {
	result scraper.Result
	err    error

	calls   int
	lastURL string
}

func (f *fakeService) Scrape(_ context.Context, url string) (scraper.Result, error) {
	f.calls++
	f.lastURL = url
	return f.result, f.err
}

func do(t *testing.T, svc httpapi.Service, target string) *httptest.ResponseRecorder {
	t.Helper()

	router := httpapi.New(svc)
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, target, nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func TestHealthAndReady(t *testing.T) {
	t.Parallel()

	for _, path := range []string{"/health", "/ready"} {
		t.Run(path, func(t *testing.T) {
			t.Parallel()

			svc := &fakeService{}
			rec := do(t, svc, path)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			// A probe that fetches a page would make liveness depend on the
			// whole internet.
			if svc.calls != 0 {
				t.Errorf("probe called the scraper %d times, want 0", svc.calls)
			}
		})
	}
}

func TestScrapeReturnsTheHTMLAndTheMatchedSelector(t *testing.T) {
	t.Parallel()

	svc := &fakeService{result: scraper.Result{Selector: "#main", HTML: "<p>hello</p>"}}

	rec := do(t, svc, "/scraper?url=https://example.test/")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Body.String(); got != "<p>hello</p>" {
		t.Errorf("body = %q, want the extracted html", got)
	}
	// The header lets a caller tell "found the main content" from "fell back to
	// the whole document" — otherwise both look like a successful scrape.
	if got := rec.Header().Get("X-Matched-Selector"); got != "#main" {
		t.Errorf("X-Matched-Selector = %q, want #main", got)
	}
	if svc.lastURL != "https://example.test/" {
		t.Errorf("service received url %q, want the query value", svc.lastURL)
	}
}

func TestStatusCodes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		target     string
		svcErr     error
		wantStatus int
		wantBody   string
	}{
		{
			name:       "missing url is 400",
			target:     "/scraper",
			svcErr:     scraper.ErrInvalidURL,
			wantStatus: http.StatusBadRequest,
			wantBody:   "provide a valid url parameter",
		},
		{
			name:       "a non-http scheme is 400",
			target:     "/scraper?url=file:///etc/passwd",
			svcErr:     scraper.ErrUnsupportedURL,
			wantStatus: http.StatusBadRequest,
			wantBody:   "only http and https urls are supported",
		},
		{
			name:       "an internal address is 403",
			target:     "/scraper?url=http://169.254.169.254/",
			svcErr:     scraper.ErrForbiddenHost,
			wantStatus: http.StatusForbidden,
			wantBody:   "that url is not allowed",
		},
		{
			name:       "nothing extractable is 404",
			target:     "/scraper?url=https://example.test/",
			svcErr:     scraper.ErrNoContent,
			wantStatus: http.StatusNotFound,
			wantBody:   "no content could be extracted from that page",
		},
		{
			name:       "an oversized page is 413",
			target:     "/scraper?url=https://example.test/",
			svcErr:     scraper.ErrTooLarge,
			wantStatus: http.StatusRequestEntityTooLarge,
			wantBody:   "that page is too large to scrape",
		},
		{
			name:       "an unreachable page is 502",
			target:     "/scraper?url=https://example.test/",
			svcErr:     scraper.ErrUpstream,
			wantStatus: http.StatusBadGateway,
			wantBody:   "could not fetch that page",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			rec := do(t, &fakeService{err: tc.svcErr}, tc.target)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body: %s)", rec.Code, tc.wantStatus, rec.Body)
			}

			var body struct {
				Error string `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decoding %q: %v", rec.Body, err)
			}
			if body.Error != tc.wantBody {
				t.Errorf("error = %q, want %q", body.Error, tc.wantBody)
			}
		})
	}
}

// The 403 body must not say WHY the host was refused. "that resolved to a
// private address" turns the endpoint into a network-mapping oracle: an
// attacker learns which internal names exist by reading the error.
func TestForbiddenResponseIsNotANetworkOracle(t *testing.T) {
	t.Parallel()

	err := errWithDetail{`scraper: that host is not allowed: "internal-db.corp" resolves to 10.0.4.17`}

	rec := do(t, &fakeService{err: err}, "/scraper?url=http://internal-db.corp/")

	body := rec.Body.String()
	for _, forbidden := range []string{"10.0.4.17", "internal-db.corp", "resolves"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("response leaked %q: %s", forbidden, body)
		}
	}
}

// A regression test for the original behaviour: the handler returned
// err.Error() verbatim, so any internal failure was echoed to the caller.
func TestErrorsDoNotLeakInternalDetail(t *testing.T) {
	t.Parallel()

	leaky := errWithDetail{`Get "http://10.0.0.9:5432/": dial tcp 10.0.0.9:5432: connect: connection refused`}

	rec := do(t, &fakeService{err: leaky}, "/scraper?url=https://example.test/")

	body := rec.Body.String()
	for _, forbidden := range []string{"10.0.0.9", "dial tcp", "5432"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("response leaked %q: %s", forbidden, body)
		}
	}
}

type errWithDetail struct{ msg string }

func (e errWithDetail) Error() string { return e.msg }

// The 403 must be a distinct status from the 400s: a caller (and a WAF, and a
// log-based alert) needs to tell "you sent nonsense" from "you asked for
// something you are not allowed to have".
func TestForbiddenIsDistinctFromBadRequest(t *testing.T) {
	t.Parallel()

	forbidden := do(t, &fakeService{err: scraper.ErrForbiddenHost}, "/scraper?url=http://x/")
	bad := do(t, &fakeService{err: scraper.ErrInvalidURL}, "/scraper?url=nonsense")

	if forbidden.Code == bad.Code {
		t.Errorf("both returned %d; the two cases must be distinguishable", forbidden.Code)
	}
}
