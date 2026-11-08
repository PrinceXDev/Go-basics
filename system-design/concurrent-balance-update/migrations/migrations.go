package migrations

import _ "embed"

//go:embed 001_accounts.up.sql
var UpSQL string
