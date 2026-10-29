package migrations

import "embed"

// Files contains the ordered SQLite schema migrations used by initialization and offline upgrades.
//
//go:embed *.sql
var Files embed.FS
