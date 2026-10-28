package migrations

import "embed"

// Files is the unchanged business schema shared by offline initialization and import.
//
//go:embed *.sql
var Files embed.FS
