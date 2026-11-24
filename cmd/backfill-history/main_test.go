package main

import (
	"database/sql"
	"path/filepath"
	"testing"

	appdb "github.com/ccmpbll/printspy/db"
	"github.com/ccmpbll/printspy/models"
)

// Only rows still missing their metadata are candidates. A row the app
// already filled (material set) is not, even though the app never writes
// the legacy path column.
func TestListCandidateRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.db")
	d, err := appdb.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	p := &models.PrinterConfig{Name: "P", Type: "prusalink", URL: "http://10.0.0.1"}
	if err := d.CreatePrinter(p); err != nil {
		t.Fatal(err)
	}
	for _, h := range []*models.PrintHistory{
		{PrinterID: p.ID, FileName: "done.bgcode", Material: "PLA", Thumbnail: []byte{1}, Result: "completed"},
		{PrinterID: p.ID, FileName: "bare.bgcode", Result: "completed"},
		// Before History kept its own thumbnail: material, no path, no
		// thumbnail. Backfill sets path so the startup migration copies the
		// cached thumbnail in.
		{PrinterID: p.ID, FileName: "legacy.bgcode", Material: "PLA", Result: "completed"},
	} {
		if err := d.InsertPrintHistory(h); err != nil {
			t.Fatal(err)
		}
	}
	d.Close()

	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := listCandidateRows(db, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].Filename != "bare.bgcode" || rows[1].Filename != "legacy.bgcode" {
		t.Errorf("candidates = %+v, want bare.bgcode and legacy.bgcode", rows)
	}
}
