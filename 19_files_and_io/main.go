package main

// ============================================================================
// CONCEPT: Files and I/O — reading and writing files with `os` and `bufio`.
//
// JS/TS comparison: this replaces Node's `fs` module.
//   fs.writeFileSync(path, data)  -> os.WriteFile(path, data, perm)
//   fs.readFileSync(path)         -> os.ReadFile(path)
// Go's file APIs return (result, error) — the same error-handling idiom
// from lesson 08 applies to EVERY file operation, since disks fail often
// (missing file, no permission, disk full, etc.).
// ============================================================================

import (
	"bufio"
	"fmt"
	"os"
)

func main() {
	filePath := "example.txt"

	// ---------- WRITING a file (simple, whole-file write) ----------
	content := "Hello, Go file I/O!\nThis is line two.\nThis is line three."

	// os.WriteFile takes a filename, the data as []byte, and a permission
	// mode (0644 = owner can read/write, others can read — standard Unix
	// permission bits; on Windows Go handles this compatibly).
	err := os.WriteFile(filePath, []byte(content), 0644)
	if err != nil {
		fmt.Println("write error:", err)
		return
	}
	fmt.Println("Wrote file:", filePath)

	// ---------- READING a whole file at once ----------
	data, err := os.ReadFile(filePath)
	if err != nil {
		fmt.Println("read error:", err)
		return
	}
	// data is []byte — convert to string for printing/text use.
	fmt.Println("Whole file contents:")
	fmt.Println(string(data))

	// ---------- READING line-by-line (for larger files) ----------
	// Reading a whole file at once is fine for small files, but for large
	// files you want to stream it line by line instead of loading
	// everything into memory. `bufio.Scanner` does this.
	file, err := os.Open(filePath)
	if err != nil {
		fmt.Println("open error:", err)
		return
	}
	// `defer` schedules file.Close() to run when main() returns — this
	// guarantees the file handle is released even if something below
	// panics. Very common Go idiom: open a resource, immediately defer
	// its cleanup.
	// Note: on Windows a file must be closed before it can be removed, so
	// this example closes each handle explicitly with Close() right after
	// use instead of relying on `defer` (which would only run when main()
	// itself returns, keeping the file locked until then).
	fmt.Println("-- reading line by line --")
	scanner := bufio.NewScanner(file)
	lineNum := 1
	for scanner.Scan() { // advances to the next line, returns false when done
		line := scanner.Text() // the current line, without the newline
		fmt.Printf("Line %d: %s\n", lineNum, line)
		lineNum++
	}
	if err := scanner.Err(); err != nil {
		fmt.Println("scan error:", err)
	}
	file.Close()

	// ---------- APPENDING to a file ----------
	// os.OpenFile gives fine-grained control over HOW the file is opened:
	// os.O_APPEND (add to end, don't overwrite), os.O_CREATE (make it if
	// missing), os.O_WRONLY (write-only).
	appendFile, err := os.OpenFile(filePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		fmt.Println("open for append error:", err)
		return
	}
	_, err = appendFile.WriteString("\nThis line was appended.")
	if err != nil {
		fmt.Println("append error:", err)
		return
	}
	appendFile.Close()
	fmt.Println("Appended a line.")

	// ---------- CHECKING if a file exists ----------
	// Go has no `fs.existsSync` — the idiom is to try Stat() and inspect
	// the error using os.IsNotExist.
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		fmt.Println("File does not exist")
	} else {
		fmt.Println("File exists")
	}

	// ---------- CLEANUP ----------
	// Removing the file we created, so re-running this example doesn't
	// leave stray artifacts behind — os.Remove is Go's fs.unlink.
	if err := os.Remove(filePath); err != nil {
		fmt.Println("remove error:", err)
	} else {
		fmt.Println("Removed:", filePath)
	}
}
