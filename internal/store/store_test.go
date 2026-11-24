package store

import (
	"context"
	"database/sql"
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

// An older panel's database: every migration up to the given one.
func openAt(t *testing.T, version int) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file::memory:?_pragma=foreign_keys(1)&_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	if err := migrateTo(context.Background(), db, version); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestJobsAndTheirLogsSurviveBecomingADevicesToo(t *testing.T) {
	ctx := context.Background()
	db := openAt(t, 11)
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}
	count := func(query string, args ...any) int {
		t.Helper()
		var n int
		if err := db.QueryRowContext(ctx, query, args...).Scan(&n); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
		return n
	}
	// What a panel of the previous version holds: two servers with jobs and
	// their logs.
	for _, name := range []string{"ams-1", "fra-2"} {
		exec(`INSERT INTO servers (name, host, host_key, created_at, updated_at) VALUES (?, '192.0.2.1', 'k', 0, 0)`, name)
	}
	exec(`INSERT INTO operations (id, server_id, kind, status, steps, started_at) VALUES (7, 1, 'install', 'succeeded', '[]', 100)`)
	exec(`INSERT INTO operations (id, server_id, kind, status, steps, error, started_at, finished_at)
		VALUES (9, 2, 'apply', 'failed', '[]', '{"code":"check_failed"}', 200, 260)`)
	for i := range 3 {
		exec(`INSERT INTO operation_log (operation_id, at, step, kind, text) VALUES (7, ?, 'xray', 'out', 'line')`, 100+i)
	}
	exec(`INSERT INTO operation_log (operation_id, at, step, kind, text) VALUES (9, 201, 'verify', 'error', 'no answer')`)

	if err := migrate(ctx, db); err != nil {
		t.Fatal(err)
	}

	// Everything is where it was.
	if n := count(`SELECT COUNT(*) FROM operations`); n != 2 {
		t.Fatalf("%d jobs after the migration, want 2", n)
	}
	if n := count(`SELECT COUNT(*) FROM operation_log WHERE operation_id = 7`); n != 3 {
		t.Fatalf("%d log lines of the first job, want 3", n)
	}
	var kind, failure string
	var finished int
	if err := db.QueryRowContext(ctx, `SELECT kind, error, finished_at FROM operations WHERE id = 9 AND server_id = 2`).
		Scan(&kind, &failure, &finished); err != nil || kind != "apply" || failure != `{"code":"check_failed"}` || finished != 260 {
		t.Fatalf("the second job: %q %q %d, %v", kind, failure, finished, err)
	}
	// New jobs are numbered after the old ones, not over them.
	exec(`INSERT INTO operations (server_id, kind, status, steps, started_at) VALUES (1, 'apply', 'failed', '[]', 300)`)
	if n := count(`SELECT MAX(id) FROM operations`); n != 10 {
		t.Fatalf("the next job got id %d, want 10", n)
	}

	// A job of a device, and only of a device.
	exec(`INSERT INTO rule_sets (name, entry_server_id, rules, default_action, created_at, updated_at) VALUES ('Дом', 1, '[]', 'direct', 0, 0)`)
	exec(`INSERT INTO devices (name, host, ssh_port, ssh_user, host_key, rule_set_id, secret, socks_port, http_port, created_at, updated_at)
		VALUES ('Роутер', '192.0.2.9', 22, 'root', 'k', 1, x'00', 10808, 10809, 0, 0)`)
	exec(`INSERT INTO operations (device_id, kind, status, steps, started_at) VALUES (1, 'device_setup', 'failed', '[]', 400)`)
	exec(`INSERT INTO operation_log (operation_id, at, step, kind, text) VALUES (11, 401, 'connect', 'cmd', 'ssh')`)
	for _, wrong := range []string{
		`INSERT INTO operations (kind, status, steps, started_at) VALUES ('apply', 'failed', '[]', 0)`,
		`INSERT INTO operations (server_id, device_id, kind, status, steps, started_at) VALUES (1, 1, 'apply', 'failed', '[]', 0)`,
	} {
		if _, err := db.ExecContext(ctx, wrong); err == nil {
			t.Fatalf("a job of nobody, or of two, was stored: %s", wrong)
		}
	}

	// Deleting whose job it is still takes the job and its log along.
	exec(`DELETE FROM servers WHERE id = 2`)
	if n := count(`SELECT COUNT(*) FROM operations WHERE id = 9`) + count(`SELECT COUNT(*) FROM operation_log WHERE operation_id = 9`); n != 0 {
		t.Fatal("a deleted server's job or its log is still there")
	}
	exec(`DELETE FROM devices WHERE id = 1`)
	if n := count(`SELECT COUNT(*) FROM operations WHERE id = 11`) + count(`SELECT COUNT(*) FROM operation_log WHERE operation_id = 11`); n != 0 {
		t.Fatal("a deleted device's job or its log is still there")
	}
	if n := count(`SELECT COUNT(*) FROM operation_log WHERE operation_id = 7`); n != 3 {
		t.Fatal("deleting one server took another's log with it")
	}
}
