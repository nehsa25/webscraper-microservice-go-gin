// Package scraper fetches a page and extracts its main content area.
package scraper

import "errors"

// Sentinel errors. Callers compare with errors.Is rather than by string, so
// wrapping with %w further up stays safe.
var (
	ErrInvalidURL     = errors.New("scraper: url is missing or malformed")
	ErrForbiddenHost  = errors.New("scraper: that host is not allowed")
	ErrUnsupportedURL = errors.New("scraper: only http and https are supported")
	ErrUpstream       = errors.New("scraper: fetching the page failed")
	ErrNoContent      = errors.New("scraper: no content container matched")
	ErrTooLarge       = errors.New("scraper: the page is too large")
)

// Selectors is the ordered list of content containers to try. The first one
// that matches and yields non-empty HTML wins.
//
// Two changes from the original list, which was a local variable inside the
// fetch function and so could not be tested or reused:
//
//   - the empty string "" has been removed. doc.Find("") matches nothing, so
//     it was a no-op sitting in the middle of the list looking like a bug.
//   - "html" remains last, deliberately: a page with none of these containers
//     still returns something rather than a 404.
var Selectors = []string{
	"#body",
	"#main",
	"#main-content",
	"#content",
	"#container",
	"#page-content",
	".body",
	".main",
	".main-content",
	".wrapper",
	"html",
}

// Result is what a successful scrape produces.
type Result struct {
	// Selector is the one that matched, so a caller can tell whether it got
	// the main content or the whole-document fallback.
	Selector string
	HTML     string
}
