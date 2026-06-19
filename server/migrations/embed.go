// Package migrations embeds the SQL migration files so the server can apply
// them at boot without shipping the .sql files separately. Keeping the embed
// directive in the same directory as the files lets Go's embed (which cannot
// reference parent directories) include them with a simple glob.
package migrations

import "embed"

// FS holds every *.sql migration, applied in lexical filename order.
//
//go:embed *.sql
var FS embed.FS
