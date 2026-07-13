package migrations

import "embed"

// FS contains the versioned SQLite migrations consumed by internal/store.
//
//go:embed *.sql
var FS embed.FS
