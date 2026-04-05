package migrations

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

type stubExecutor struct {
	statements []string
	err        error
}

func (s *stubExecutor) Exec(_ context.Context, sql string, _ ...any) (pgconn.CommandTag, error) {
	s.statements = append(s.statements, sql)
	if s.err != nil {
		return pgconn.CommandTag{}, s.err
	}
	return pgconn.CommandTag{}, nil
}

func TestApplyExecutesEmbeddedMigrations(t *testing.T) {
	db := &stubExecutor{}

	if err := Apply(context.Background(), db); err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}

	if len(db.statements) == 0 {
		t.Fatal("expected at least one migration statement to be executed")
	}

	if !strings.Contains(db.statements[0], "CREATE TABLE IF NOT EXISTS telemetry") {
		t.Fatal("expected telemetry migration SQL to be executed")
	}
}

func TestApplyReturnsExecutionError(t *testing.T) {
	db := &stubExecutor{err: errors.New("boom")}

	err := Apply(context.Background(), db)
	if err == nil {
		t.Fatal("expected Apply to fail when migration execution fails")
	}

	if !strings.Contains(err.Error(), "apply embedded migration") {
		t.Fatalf("unexpected error message: %v", err)
	}
}
