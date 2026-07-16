package db

import (
	"testing"

	"github.com/ccmpbll/printspy/models"
)

// Regression: History's thumbnail is its own copy on the row, looked up by
// id alone - must survive independent of file_meta_cache (which is keyed by
// path/timestamp and gets pruned/overwritten whenever the printer's live
// file listing changes).
func TestPrintHistoryThumbnailIsOwnCopy(t *testing.T) {
	database, err := Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	p := &models.PrinterConfig{Name: "Test", Type: "prusalink", URL: "http://10.0.0.1", APIKey: "key"}
	if err := database.CreatePrinter(p); err != nil {
		t.Fatal(err)
	}

	h := &models.PrintHistory{
		PrinterID:            p.ID,
		FileName:             "test.bgcode",
		StartedAt:            "2026-07-16T00:00:00Z",
		CompletedAt:          "2026-07-16T01:00:00Z",
		Result:               "completed",
		Thumbnail:            []byte{0xFF, 0xD8, 0xFF, 0xE0},
		ThumbnailContentType: "image/jpeg",
	}
	if err := database.InsertPrintHistory(h); err != nil {
		t.Fatal(err)
	}

	entries, _, err := database.ListPrintHistory(p.ID, 10, 0)
	if err != nil || len(entries) != 1 {
		t.Fatalf("ListPrintHistory: %v, %d entries", err, len(entries))
	}
	if !entries[0].HasThumbnail {
		t.Error("HasThumbnail = false, want true")
	}

	thumb, contentType, err := database.GetPrintHistoryThumbnail(entries[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if string(thumb) != string(h.Thumbnail) || contentType != h.ThumbnailContentType {
		t.Errorf("GetPrintHistoryThumbnail = %v, %q; want %v, %q", thumb, contentType, h.Thumbnail, h.ThumbnailContentType)
	}

	// A file's file_meta_cache row can vanish entirely (deleted from the
	// printer, pruned by backfill) without touching History's own copy.
	if err := database.DeleteFileMetaCache(p.ID, "anything"); err != nil {
		t.Fatal(err)
	}
	thumb2, _, err := database.GetPrintHistoryThumbnail(entries[0].ID)
	if err != nil || string(thumb2) != string(h.Thumbnail) {
		t.Error("History thumbnail should be unaffected by file_meta_cache deletion")
	}
}

// Regression: an existing print_history row from before the thumbnail
// columns existed - written with the old path/uploaded_at cross-reference -
// must get backfilled from whatever's still in file_meta_cache the next
// time migrate() runs (i.e. on the next process start), not left stranded.
func TestPrintHistoryThumbnailBackfillFromFileMetaCache(t *testing.T) {
	database, err := Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	p := &models.PrinterConfig{Name: "Test", Type: "prusalink", URL: "http://10.0.0.1", APIKey: "key"}
	if err := database.CreatePrinter(p); err != nil {
		t.Fatal(err)
	}

	// Simulate a pre-migration row: real thumbnail in file_meta_cache, but
	// print_history was inserted with the old path-only cross-reference
	// and its own thumbnail column still NULL.
	if err := database.SetFileMetaCache(p.ID, "OLD~1.BGC", 111, nil, []byte{1, 2, 3}, "image/png"); err != nil {
		t.Fatal(err)
	}
	old := &models.PrintHistory{
		PrinterID: p.ID, FileName: "old.bgcode",
		StartedAt: "2026-07-15T00:00:00Z", CompletedAt: "2026-07-15T01:00:00Z", Result: "completed",
	}
	if err := database.InsertPrintHistory(old); err != nil {
		t.Fatal(err)
	}
	preBackfill, _, err := database.ListPrintHistory(p.ID, 10, 0)
	if err != nil || len(preBackfill) != 1 {
		t.Fatalf("seeding row: %v, %d entries", err, len(preBackfill))
	}
	// Retrofit the old-style path/uploaded_at cross-reference this row
	// would've had from the pre-decoupling code - uploaded_at deliberately
	// mismatched from file_meta_cache's, exactly the drift that broke it.
	if _, err := database.conn.Exec(`UPDATE print_history SET path = ?, uploaded_at = ? WHERE id = ?`,
		"OLD~1.BGC", 999, preBackfill[0].ID); err != nil {
		t.Fatal(err)
	}

	if err := database.migrate(); err != nil {
		t.Fatal(err)
	}

	entries, _, err := database.ListPrintHistory(p.ID, 10, 0)
	if err != nil || len(entries) != 1 {
		t.Fatalf("ListPrintHistory: %v, %d entries", err, len(entries))
	}
	if !entries[0].HasThumbnail {
		t.Fatal("backfill did not populate thumbnail from file_meta_cache")
	}
	thumb, contentType, err := database.GetPrintHistoryThumbnail(entries[0].ID)
	if err != nil || string(thumb) != "\x01\x02\x03" || contentType != "image/png" {
		t.Errorf("GetPrintHistoryThumbnail after backfill = %v, %q, %v", thumb, contentType, err)
	}
}

func TestPrintHistoryWithoutThumbnail(t *testing.T) {
	database, err := Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	p := &models.PrinterConfig{Name: "Test", Type: "prusalink", URL: "http://10.0.0.1", APIKey: "key"}
	if err := database.CreatePrinter(p); err != nil {
		t.Fatal(err)
	}

	h := &models.PrintHistory{PrinterID: p.ID, FileName: "no-thumb.gcode", Result: "completed"}
	if err := database.InsertPrintHistory(h); err != nil {
		t.Fatal(err)
	}

	entries, _, err := database.ListPrintHistory(p.ID, 10, 0)
	if err != nil || len(entries) != 1 {
		t.Fatalf("ListPrintHistory: %v, %d entries", err, len(entries))
	}
	if entries[0].HasThumbnail {
		t.Error("HasThumbnail = true, want false")
	}
	thumb, _, err := database.GetPrintHistoryThumbnail(entries[0].ID)
	if err != nil || len(thumb) != 0 {
		t.Errorf("GetPrintHistoryThumbnail = %v, %v; want empty, nil", thumb, err)
	}
}
