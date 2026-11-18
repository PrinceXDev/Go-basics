// devpg runs a throw-away PostgreSQL without Docker or a local install.
// Usage: go run ./cmd/devpg [-port 5433]
// Port 5433 is the default so it won't clash with a Postgres already on 5432.
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
	dir := flag.String("dir", filepath.Join(os.TempDir(), "cbpay-pg"), "working directory")
	port := flag.Uint("port", 5433, "port")
	flag.Parse()

	pg := embeddedpostgres.NewDatabase(embeddedpostgres.DefaultConfig().
		Port(uint32(*port)).
		DataPath(filepath.Join(*dir, "data")).
		RuntimePath(filepath.Join(*dir, "run")).
		BinariesPath(filepath.Join(*dir, "bin")).
		Username("cbpay").Password("cbpay").Database("cbpay").
		Locale("C").Encoding("UTF8")) // default on Windows is WIN1252
	if err := pg.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "start:", err)
		os.Exit(1)
	}
	fmt.Printf("postgres ready: postgres://cbpay:cbpay@localhost:%d/cbpay?sslmode=disable  (Ctrl+C to stop)\n", *port)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	<-sig
	_ = pg.Stop()
}
