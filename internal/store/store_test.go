package store

import (
	"context"
	"errors"
	"testing"
)

func TestMigrationsApplyToAnEmptyDatabaseAndOnlyOnce(t *testing.T) {
	ctx := context.Background()
	db, err := OpenMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	var applied int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations`).Scan(&applied); err != nil {
		t.Fatal(err)
	}
	files, err := migrationFiles.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	if applied != len(files) || applied == 0 {
		t.Fatalf("%d migrations recorded, %d files", applied, len(files))
	}
	// A second start finds nothing left to do.
	if err := migrate(ctx, db); err != nil {
		t.Fatalf("migrating an up-to-date database: %v", err)
	}
	var again int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations`).Scan(&again); err != nil {
		t.Fatal(err)
	}
	if again != applied {
		t.Fatalf("a second run recorded %d more migrations", again-applied)
	}
}

func TestIsUniqueTellsATakenNameFromOtherRefusals(t *testing.T) {
	ctx := context.Background()
	db, err := OpenMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	insert := `INSERT INTO templates (name, description, created_at) VALUES (?, '', 0)`
	if _, err := db.ExecContext(ctx, insert, "edge"); err != nil {
		t.Fatal(err)
	}
	_, taken := db.ExecContext(ctx, insert, "edge")
	if taken == nil || !IsUnique(taken) {
		t.Fatalf("a second template of the same name: %v", taken)
	}

	// A value that must not be missing is refused too, and is not a taken
	// name — though its message is as full of capitals.
	_, missing := db.ExecContext(ctx, `INSERT INTO templates (name, description, created_at) VALUES (NULL, '', 0)`)
	if missing == nil || IsUnique(missing) {
		t.Fatalf("a missing value was taken for a taken name: %v", missing)
	}
	if IsUnique(nil) || IsUnique(errors.New("UNIQUE constraint failed")) {
		t.Fatal("something that is not the database's refusal was taken for one")
	}
}
