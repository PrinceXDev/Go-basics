package main

// ============================================================================
// CONCEPT: A small CLI tool — tying together flags, files, and error
// handling into one realistic program.
//
// This is a word-count tool (like Unix `wc`): pass it a file path and
// optional flags, and it reports line/word/char counts. It brings
// together: os.Args / the `flag` package, file I/O (lesson 19),
// bufio.Scanner, and error handling (lesson 08) — the first "real"
// program in this course rather than an isolated concept demo.
//
// JS/TS comparison: `flag` is Go's built-in equivalent of a lightweight
// `yargs`/`commander` — no external package needed for basic CLI parsing.
// ============================================================================

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"strings"
)

func main() {
	// flag.String/Bool/Int register a named flag: name, default value,
	// help text. flag.Parse() reads os.Args and populates them.
	// Usage: go run ./26_cli_tool -file=sample.txt -lines
	filePath := flag.String("file", "", "path to the file to analyze (required)")
	showLines := flag.Bool("lines", true, "show line count")
	showWords := flag.Bool("words", true, "show word count")
	showChars := flag.Bool("chars", true, "show character count")
	flag.Parse()

	if *filePath == "" {
		// Writing usage/errors to Stderr (not Stdout) is the Unix
		// convention: Stdout is for real output, Stderr is for
		// diagnostics — so piping/redirecting output doesn't capture
		// error noise.
		fmt.Fprintln(os.Stderr, "Error: -file flag is required")
		flag.Usage()
		os.Exit(1) // non-zero exit code signals failure to the shell/CI
	}

	stats, err := analyzeFile(*filePath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}

	if *showLines {
		fmt.Println("Lines:", stats.lines)
	}
	if *showWords {
		fmt.Println("Words:", stats.words)
	}
	if *showChars {
		fmt.Println("Characters:", stats.chars)
	}
}

type fileStats struct {
	lines, words, chars int
}

func analyzeFile(path string) (fileStats, error) {
	file, err := os.Open(path)
	if err != nil {
		// fmt.Errorf + %w wraps the underlying error (lesson 08) so the
		// caller still sees the original os.Open failure reason.
		return fileStats{}, fmt.Errorf("could not open file: %w", err)
	}
	defer file.Close()

	var stats fileStats
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		stats.lines++
		stats.words += len(strings.Fields(line)) // Fields splits on whitespace
		stats.chars += len(line)
	}
	if err := scanner.Err(); err != nil {
		return fileStats{}, fmt.Errorf("error reading file: %w", err)
	}

	return stats, nil
}
