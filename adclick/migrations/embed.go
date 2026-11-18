// Package migrations embeds the SQL files so binaries are self-contained.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
