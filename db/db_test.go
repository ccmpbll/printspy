package db

import (
	"strings"
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

func TestListPrintHistoryFilteredByResultAndName(t *testing.T) {
	database, err := Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	p := &models.PrinterConfig{Name: "P", Type: "prusalink", URL: "http://10.0.0.1", APIKey: "k", PollInterval: 10, Enabled: true}
	if err := database.CreatePrinter(p); err != nil {
		t.Fatal(err)
	}
	for _, e := range []struct{ name, result string }{
		{"benchy.bgcode", "completed"},
		{"Gear_Set.bgcode", "failed"},
		{"gear100%.bgcode", "completed"},
		{"vase.bgcode", "cancelled"},
	} {
		if err := database.InsertPrintHistory(&models.PrintHistory{PrinterID: p.ID, FileName: e.name, Result: e.result}); err != nil {
			t.Fatal(err)
		}
	}
	names := func(result, q string) string {
		rows, _, err := database.ListPrintHistoryFiltered(p.ID, 10, 0, result, q)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, r := range rows {
			out = append(out, r.FileName)
		}
		return strings.Join(out, ",")
	}
	if got := names("", ""); got != "vase.bgcode,gear100%.bgcode,Gear_Set.bgcode,benchy.bgcode" {
		t.Errorf("no filter = %q", got)
	}
	if got := names("failed", ""); got != "Gear_Set.bgcode" {
		t.Errorf("failed = %q", got)
	}
	if got := names("", "GEAR"); got != "gear100%.bgcode,Gear_Set.bgcode" {
		t.Errorf("case-insensitive search = %q", got)
	}
	if got := names("completed", "gear"); got != "gear100%.bgcode" {
		t.Errorf("result and search = %q", got)
	}
	// % and _ typed by the user are literal, not wildcards.
	if got := names("", "100%"); got != "gear100%.bgcode" {
		t.Errorf("literal %% = %q", got)
	}
	if got := names("", "_"); got != "Gear_Set.bgcode" {
		t.Errorf("literal _ = %q", got)
	}
	if got := names("", "%"); got != "gear100%.bgcode" {
		t.Errorf("lone %% = %q", got)
	}
}
