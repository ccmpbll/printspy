package db

import (
	"database/sql"
	"testing"
)

func TestOpenUpgradesLegacySchemaAndIsIdempotent(t *testing.T) {
	path := t.TempDir() + "/old.db"
	old, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec(`CREATE TABLE printers (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL, type TEXT NOT NULL DEFAULT 'octoprint', url TEXT NOT NULL, api_key TEXT NOT NULL, enabled INTEGER NOT NULL DEFAULT 1, poll_interval INTEGER NOT NULL DEFAULT 10, created_at DATETIME DEFAULT CURRENT_TIMESTAMP, updated_at DATETIME DEFAULT CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	old.Close()

	for i := 0; i < 2; i++ { // second open: every column already exists
		d, err := Open(path)
		if err != nil {
			t.Fatalf("open #%d: %v", i+1, err)
		}
		if _, err := d.ListPrinters(); err != nil {
			t.Fatalf("ListPrinters after upgrade: %v", err)
		}
		d.Close()
	}
}

func TestAddColumnSurfacesRealFailure(t *testing.T) {
	d := openTest(t)
	d.conn.Close()
	if err := d.addColumn("printers", "zzz", "INTEGER"); err == nil {
		t.Fatal("addColumn on a closed DB returned nil; failures must not be swallowed")
	}
}
