package main

import (
	"fmt"
	"sync"
	"time"
)

func download(file string) {
	fmt.Println("Downloading", file, "...")
	time.Sleep(time.Second)
	fmt.Println("Downloaded", file)
}

func main() {
	var wg sync.WaitGroup

	files := []string{
		"a.pdf",
		"b.pdf",
		"c.pdf",
		"d.pdf",
		"e.pdf",
	}

	wg.Add(len(files))

	for _, file := range files {
		go func(file string) {
			defer wg.Done()

			download(file)
		}(file)
	}

	wg.Wait()

	fmt.Println("All files downloaded")
}
