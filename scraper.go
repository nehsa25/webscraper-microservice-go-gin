package main

import (
	"fmt"
	"net/http"

	"github.com/PuerkitoBio/goquery"

	"github.com/gin-gonic/gin"
)

func getScrapedData(url string) ([]byte, error) {
	resp, err := http.Get(url)
	if err != nil {
		panic(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		panic("error: status code should be 200 but got: " + resp.Status)
	}

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		panic(err)
	}

	strings := []string{"#body", "#main", "#main-content", "#content", "#container", "#page-content", ".body", ".main", ".main-content", "", ".wrapper", "html"}
	html := ""
	for _, searchArea := range strings {
		fmt.Println(searchArea)
		selection := doc.Find(searchArea)
		if selection.Length() > 0 {
			html, err = selection.Html()
			if err != nil {
				panic(err)
			}
			if html != "" {
				fmt.Printf("Found something: %s", html)
				break
			}
		} else {
			fmt.Println("Element with id 'body' not found")
		}
	}

	return []byte(html), nil
}

func main() {
	fmt.Println("Starting app!")
	router := gin.Default()

	router.GET("/scraper", func(c *gin.Context) {
		// get url parameter
		url := c.Query("url")

		// makes the GET request to the OpenWeatherMap API
		scapedData, err := getScrapedData(url)
		if err != nil {
			fmt.Printf("router scape: Error getting data from url: %s\nError: %s", url, err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.String(http.StatusOK, string(scapedData))
	})

	fmt.Println("Running service on port 8081")
	router.Run(":8081")
}
