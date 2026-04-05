package migrations

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
)

// SQLExecutor is the minimal DB contract required to run SQL migrations.
type SQLExecutor interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
}

//go:embed *.sql
var embeddedSQL embed.FS

// Apply executes embedded .sql migrations in lexical order.
// SQL scripts are expected to be idempotent so startup can safely re-run them.
func Apply(ctx context.Context, db SQLExecutor) error {
	entries, err := fs.ReadDir(embeddedSQL, ".")
	if err != nil {
		return fmt.Errorf("list embedded migrations: %w", err)
	}

	files := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		files = append(files, entry.Name())
	}

	sort.Strings(files)

	for _, name := range files {
		statement, err := embeddedSQL.ReadFile(name)
		if err != nil {
			return fmt.Errorf("read embedded migration %s: %w", name, err)
		}

		if _, err := db.Exec(ctx, string(statement)); err != nil {
			return fmt.Errorf("apply embedded migration %s: %w", name, err)
		}
	}

	return nil
}
