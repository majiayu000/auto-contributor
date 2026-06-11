package db

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"
)

func TestMigratePropagatesPostgresColumnMigrationFailure(t *testing.T) {
	stub := &migrationExecStub{
		failOn:  "ALTER TABLE pull_requests ADD COLUMN IF NOT EXISTS feedback_round",
		failErr: fmt.Errorf("permission denied"),
	}
	registerMigrationExecStub(t, stub)

	sqlDB, err := sql.Open(migrationExecStubDriverName, "")
	if err != nil {
		t.Fatalf("open migration exec stub db: %v", err)
	}
	t.Cleanup(func() {
		_ = sqlDB.Close()
	})

	db := &DB{DB: sqlDB, dbType: DBTypePostgres}
	err = db.Migrate()
	if err == nil {
		t.Fatal("Migrate() error = nil, want column migration failure")
	}
	if !strings.Contains(err.Error(), "feedback_round: permission denied") {
		t.Fatalf("Migrate() error = %q", err)
	}
}

func TestRunMigrationsIgnoresSQLiteDuplicateColumnError(t *testing.T) {
	stub := &migrationExecStub{
		failOn:  "ALTER TABLE issues ADD COLUMN retry_count",
		failErr: fmt.Errorf("duplicate column name: retry_count"),
	}
	registerMigrationExecStub(t, stub)

	sqlDB, err := sql.Open(migrationExecStubDriverName, "")
	if err != nil {
		t.Fatalf("open migration exec stub db: %v", err)
	}
	t.Cleanup(func() {
		_ = sqlDB.Close()
	})

	db := &DB{DB: sqlDB, dbType: DBTypeSQLite}
	if err := db.runMigrations(); err != nil {
		t.Fatalf("runMigrations() error = %v, want nil duplicate-column error", err)
	}
}

func TestRunMigrationsPropagatesSQLiteNonDuplicateError(t *testing.T) {
	stub := &migrationExecStub{
		failOn:  "ALTER TABLE pull_requests ADD COLUMN merged_at",
		failErr: fmt.Errorf("database is locked"),
	}
	registerMigrationExecStub(t, stub)

	sqlDB, err := sql.Open(migrationExecStubDriverName, "")
	if err != nil {
		t.Fatalf("open migration exec stub db: %v", err)
	}
	t.Cleanup(func() {
		_ = sqlDB.Close()
	})

	db := &DB{DB: sqlDB, dbType: DBTypeSQLite}
	err = db.runMigrations()
	if err == nil {
		t.Fatal("runMigrations() error = nil, want locked database error")
	}
	if !strings.Contains(err.Error(), "pull_requests.merged_at: database is locked") {
		t.Fatalf("runMigrations() error = %q", err)
	}
}
