package migrations

import (
	"context"
	"errors"
	"io/fs"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

type stubExecutor struct {
	statements []string
	err        error
}

type fakeDirEntry struct {
	name string
	dir  bool
}

const unexpectedErrFmt = "unexpected error message: %v"

func (f fakeDirEntry) Name() string               { return f.name }
func (f fakeDirEntry) IsDir() bool                { return f.dir }
func (f fakeDirEntry) Type() fs.FileMode          { return 0 }
func (f fakeDirEntry) Info() (fs.FileInfo, error) { return nil, nil }

func (s *stubExecutor) Exec(_ context.Context, sql string, _ ...any) (pgconn.CommandTag, error) {
	s.statements = append(s.statements, sql)
	if s.err != nil {
		return pgconn.CommandTag{}, s.err
	}
	return pgconn.CommandTag{}, nil
}

func patchEmbeddedReaders(t *testing.T) {
	t.Helper()
	prevReadEmbeddedDir := readEmbeddedDir
	prevReadEmbeddedFile := readEmbeddedFile
	t.Cleanup(func() {
		readEmbeddedDir = prevReadEmbeddedDir
		readEmbeddedFile = prevReadEmbeddedFile
	})
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
		t.Fatalf(unexpectedErrFmt, err)
	}
}

func TestApplyReturnsListError(t *testing.T) {
	patchEmbeddedReaders(t)
	readEmbeddedDir = func() ([]fs.DirEntry, error) {
		return nil, errors.New("list failure")
	}

	err := Apply(context.Background(), &stubExecutor{})
	if err == nil {
		t.Fatal("expected Apply to fail when listing embedded migrations fails")
	}
	if !strings.Contains(err.Error(), "list embedded migrations") {
		t.Fatalf(unexpectedErrFmt, err)
	}
}

func TestApplyReturnsReadError(t *testing.T) {
	patchEmbeddedReaders(t)
	readEmbeddedDir = func() ([]fs.DirEntry, error) {
		return []fs.DirEntry{fakeDirEntry{name: "001.sql"}}, nil
	}
	readEmbeddedFile = func(string) ([]byte, error) {
		return nil, errors.New("read failure")
	}

	err := Apply(context.Background(), &stubExecutor{})
	if err == nil {
		t.Fatal("expected Apply to fail when reading migration file fails")
	}
	if !strings.Contains(err.Error(), "read embedded migration") {
		t.Fatalf(unexpectedErrFmt, err)
	}
}

func TestApplyFiltersAndSortsEntries(t *testing.T) {
	patchEmbeddedReaders(t)
	readEmbeddedDir = func() ([]fs.DirEntry, error) {
		return []fs.DirEntry{
			fakeDirEntry{name: "zzz.sql"},
			fakeDirEntry{name: "skip.txt"},
			fakeDirEntry{name: "folder", dir: true},
			fakeDirEntry{name: "aaa.sql"},
		}, nil
	}
	readEmbeddedFile = func(name string) ([]byte, error) {
		sql := map[string]string{
			"aaa.sql": "--aaa",
			"zzz.sql": "--zzz",
		}
		return []byte(sql[name]), nil
	}

	db := &stubExecutor{}
	err := Apply(context.Background(), db)
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if len(db.statements) != 2 {
		t.Fatalf("expected 2 sql statements to be executed, got %d", len(db.statements))
	}
	if db.statements[0] != "--aaa" || db.statements[1] != "--zzz" {
		t.Fatalf("expected sorted execution order, got %#v", db.statements)
	}
}
