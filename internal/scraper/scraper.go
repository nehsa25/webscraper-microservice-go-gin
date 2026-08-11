package scraper

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
)

// Doer is the seam that makes this package testable. Depending on the one
// method used, rather than on *http.Client, lets a test inject a stub while
// production passes *http.Client unchanged.
type Doer interface {
	Do(req *http.Request) (*http.Response, error)
}

// MaxBodyBytes caps how much of a page is read.
//
// The original read the whole body with no limit, so a single request to a page
// that streams indefinitely would exhaust the container's memory.
const MaxBodyBytes = 5 << 20 // 5 MiB

// Scraper fetches a page and extracts its main content.
type Scraper struct {
	HTTP      Doer
	Guard     *Guard
	Selectors []string
}

// New builds a Scraper. A nil Doer yields a real *http.Client with a timeout —
// the original had none, so a slow page held a connection open indefinitely.
func New(doer Doer, guard *Guard) *Scraper {
	if doer == nil {
		doer = &http.Client{
			Timeout: 15 * time.Second,
			// Do not follow redirects blindly: a redirect to an internal
			// address would bypass the guard, which only checked the URL the
			// caller supplied.
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
	}
	if guard == nil {
		guard = NewGuard(false)
	}
	return &Scraper{HTTP: doer, Guard: guard, Selectors: Selectors}
}

// Scrape fetches raw and returns the first matching content container.
//
// Every failure below used to be a panic() — which, in a Gin handler, is
// recovered into a 500 but takes the whole request down and logs a stack trace
// for what is usually just a bad URL.
func (s *Scraper) Scrape(ctx context.Context, raw string) (Result, error) {
	target, err := s.Guard.Check(ctx, raw)
	if err != nil {
		return Result{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return Result{}, fmt.Errorf("%w: %w", ErrUpstream, err)
	}
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	req.Header.Set("User-Agent", "nehsa-webscraper/1.0")

	resp, err := s.HTTP.Do(req)
	if err != nil {
		return Result{}, fmt.Errorf("%w: %w", ErrUpstream, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return Result{}, fmt.Errorf("%w: status %d", ErrUpstream, resp.StatusCode)
	}

	// Read one byte past the cap so an oversized page can be distinguished
	// from one that exactly fills it.
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxBodyBytes+1))
	if err != nil {
		return Result{}, fmt.Errorf("%w: reading body: %w", ErrUpstream, err)
	}
	if len(body) > MaxBodyBytes {
		return Result{}, fmt.Errorf("%w: over %d bytes", ErrTooLarge, MaxBodyBytes)
	}

	doc, err := goquery.NewDocumentFromReader(strings.NewReader(string(body)))
	if err != nil {
		return Result{}, fmt.Errorf("%w: parsing html: %w", ErrUpstream, err)
	}

	return s.extract(doc)
}

// extract walks the selector list and returns the first non-empty match.
func (s *Scraper) extract(doc *goquery.Document) (Result, error) {
	for _, selector := range s.Selectors {
		selection := doc.Find(selector)
		if selection.Length() == 0 {
			continue
		}

		html, err := selection.Html()
		if err != nil {
			// One bad selector must not abort the whole walk — the next one
			// may well match. The original panicked here.
			continue
		}
		if strings.TrimSpace(html) == "" {
			continue
		}
		return Result{Selector: selector, HTML: html}, nil
	}
	return Result{}, ErrNoContent
}
