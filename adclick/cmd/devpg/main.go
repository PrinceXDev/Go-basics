// devpg runs a throw-away Postgres without Docker, for local demos.
// Usage: go run ./cmd/devpg [-dir DIR] [-port 5432]
package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
)

func main() {
	dir := flag.String("dir", filepath.Join(os.TempDir(), "adclick-devpg"), "working directory")
	port := flag.Uint("port", 5432, "port")
	flag.Parse()

	pg := embeddedpostgres.NewDatabase(embeddedpostgres.DefaultConfig().
		Port(uint32(*port)).
		DataPath(filepath.Join(*dir, "data")).
		RuntimePath(filepath.Join(*dir, "run")).
		BinariesPath(filepath.Join(*dir, "bin")).
		Username("adclick").Password("adclick").Database("adclick"))
	if err := pg.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "start:", err)
		os.Exit(1)
	}
	fmt.Printf("postgres ready: postgres://adclick:adclick@localhost:%d/adclick?sslmode=disable\n", *port)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	<-sig
	_ = pg.Stop()
}
