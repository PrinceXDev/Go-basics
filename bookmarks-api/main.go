package main

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

type Bookmark struct {
	Id    int    `json:"id"`
	Title string `json:"title" binding:"required"`
	// "required,url" — both rules must be inside ONE tag string, comma-
	// separated. Your old version had `binding:"required",url` : that closed
	// the tag after "required" and left ,url as dead text outside it, so the
	// url format was never actually checked.
	URL string `json:"url" binding:"required,url"`
}

// In-memory "database" — just a slice in RAM. It resets every time the
// server restarts, and (unlike a real DB) two requests writing to it at the
// same time would race. Fine for learning; a *sync.Mutex or a real DB is the
// production fix.
var bookmarks = []Bookmark{}

var nextID = 1

// findBookmarkIndex returns the slice index of the bookmark with this id,
// or -1 if none exists. Centralized here so GET/PUT/DELETE by id all agree
// on what "not found" means.
func findBookmarkIndex(id int) int {
	for i, b := range bookmarks {
		if b.Id == id {
			return i
		}
	}
	return -1
}

func main() {
	// Create a Gin router with default middleware (logger and recovery)
	router := gin.Default()

	// gin.Context allow us to respond to a request
	router.GET("/", func(c *gin.Context) {
		// gin.H => building a json object to send back
		c.JSON(200, gin.H{"message": "hello from Gin!"})
	})

	router.GET("/hello/:name", func(c *gin.Context) {
		// Path Parameter
		name := c.Param("name")

		// Query Strings (https://codynn.com/Codynn?username=Prince)
		loud := c.DefaultQuery("loud", "false")

		greeting := "hello" + "" + name

		if loud == "true" {
			greeting = strings.ToUpper(greeting)
		}

		c.JSON(200, gin.H{"greeting": greeting})
	})

	router.POST("/bookmarks", func(c *gin.Context) {
		var newBookmark Bookmark

		// JSON Binding

		if err := c.ShouldBindJSON(&newBookmark); err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}

		newBookmark.Id = nextID
		nextID++

		bookmarks = append(bookmarks, newBookmark)
		c.JSON(http.StatusCreated, newBookmark)
	})

	// List all bookmarks.
	router.GET("/bookmarks", func(c *gin.Context) {
		c.JSON(http.StatusOK, bookmarks)
	})

	// Get one bookmark by id.
	router.GET("/bookmarks/:id", func(c *gin.Context) {
		id, err := strconv.Atoi(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "id must be a number"})
			return
		}

		i := findBookmarkIndex(id)
		if i == -1 {
			c.JSON(http.StatusNotFound, gin.H{"error": "bookmark not found"})
			return
		}

		c.JSON(http.StatusOK, bookmarks[i])
	})

	// Replace a bookmark's title/url. Same binding rules as create, so a
	// bad update is rejected before it ever touches the slice.
	router.PUT("/bookmarks/:id", func(c *gin.Context) {
		id, err := strconv.Atoi(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "id must be a number"})
			return
		}

		i := findBookmarkIndex(id)
		if i == -1 {
			c.JSON(http.StatusNotFound, gin.H{"error": "bookmark not found"})
			return
		}

		var updated Bookmark
		if err := c.ShouldBindJSON(&updated); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		// Keep the existing id — the client sent title/url, not an id to
		// reassign.
		updated.Id = id
		bookmarks[i] = updated
		c.JSON(http.StatusOK, updated)
	})

	// Delete a bookmark by id.
	router.DELETE("/bookmarks/:id", func(c *gin.Context) {
		id, err := strconv.Atoi(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "id must be a number"})
			return
		}

		i := findBookmarkIndex(id)
		if i == -1 {
			c.JSON(http.StatusNotFound, gin.H{"error": "bookmark not found"})
			return
		}

		// append(a[:i], a[i+1:]...) drops the element at i by overwriting it
		// with everything after it — Go has no built-in "remove" for slices.
		bookmarks = append(bookmarks[:i], bookmarks[i+1:]...)
		c.Status(http.StatusNoContent)
	})

	err := router.Run(":8080")

	if err != nil {
		println("The router encountered an error while running\n", err)
	}
}
