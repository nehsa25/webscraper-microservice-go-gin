// Package httpapi wires the HTTP surface. It knows about status codes and
// query strings; it knows nothing about how a page is fetched or parsed.
package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/nehsa-net/webscraper-microservice-go-gin/internal/scraper"
)

// Service is the narrow interface the handler needs. Being an interface is
// what lets a router test drive every status-code branch with no network.
type Service interface {
	Scrape(ctx context.Context, url string) (scraper.Result, error)
}

// New builds the router.
//
// Returning *gin.Engine rather than starting a server is what lets tests call
// router.ServeHTTP against an httptest recorder. The original defined its
// handler as a closure inside main(), where no test could reach it.
func New(svc Service) *gin.Engine {
	router := gin.New()
	router.Use(gin.Recovery())

	router.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})
	router.GET("/ready", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ready"})
	})

	router.GET("/scraper", func(c *gin.Context) {
		result, err := svc.Scrape(c.Request.Context(), c.Query("url"))
		if err != nil {
			writeError(c, err)
			return
		}

		// Tell the caller which container matched, so they can distinguish
		// "found the main content" from "fell back to the whole document".
		c.Header("X-Matched-Selector", result.Selector)
		c.String(http.StatusOK, result.HTML)
	})

	return router
}

// writeError maps domain errors onto status codes in one place.
//
// The original returned err.Error() to the caller, which echoed the internal
// failure verbatim. The cause goes to the log now; the caller gets a status
// code and a flat sentence.
func writeError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, scraper.ErrInvalidURL):
		c.JSON(http.StatusBadRequest, gin.H{"error": "provide a valid url parameter"})
	case errors.Is(err, scraper.ErrUnsupportedURL):
		c.JSON(http.StatusBadRequest, gin.H{"error": "only http and https urls are supported"})
	case errors.Is(err, scraper.ErrForbiddenHost):
		// 403, not 400: the request was well-formed and is being refused.
		// The message deliberately does not say WHY, because "that resolved to
		// a private address" is a network-mapping oracle.
		c.JSON(http.StatusForbidden, gin.H{"error": "that url is not allowed"})
	case errors.Is(err, scraper.ErrNoContent):
		c.JSON(http.StatusNotFound, gin.H{"error": "no content could be extracted from that page"})
	case errors.Is(err, scraper.ErrTooLarge):
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "that page is too large to scrape"})
	default:
		_ = c.Error(err) // recorded for the log, not rendered to the caller
		c.JSON(http.StatusBadGateway, gin.H{"error": "could not fetch that page"})
	}
}
