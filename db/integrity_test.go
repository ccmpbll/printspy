package db

import (
	"errors"
	"testing"

	"github.com/ccmpbll/printspy/models"
)

func openTest(t *testing.T) *DB {
	t.Helper()
	d, err := Open(t.TempDir() + "/t.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func TestUpdateAndDeleteMissingRowsReturnErrNotFound(t *testing.T) {
	d := openTest(t)
	checks := map[string]error{
		"SetMaintenance":     d.SetMaintenance(99, true),
		"UpdateSmartPlug":    d.UpdateSmartPlug(99, "1.2.3.4", "1", "x", false, nil, ""),
		"DeleteSmartPlug":    d.DeleteSmartPlug(99),
		"UpdateCamera":       d.UpdateCamera(99, "http://x", "x", nil),
		"DeleteCamera":       d.DeleteCamera(99),
		"UpdatePrinter":      d.UpdatePrinter(&models.PrinterConfig{ID: 99, Name: "x", URL: "u", APIKey: "k"}),
		"DeletePrinter":      d.DeletePrinter(99),
		"UpdateIngestTarget": d.UpdateIngestTarget(99, "m", nil, "l"),
		"DeleteIngestTarget": d.DeleteIngestTarget(99),
	}
	for name, err := range checks {
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("%s: err = %v, want ErrNotFound", name, err)
		}
	}
}

func TestDeletePrinterRemovesFileMetaCache(t *testing.T) {
	d := openTest(t)
	p := models.PrinterConfig{Name: "p", URL: "u", APIKey: "k"}
	if err := d.CreatePrinter(&p); err != nil {
		t.Fatal(err)
	}
	if err := d.SetFileMetaCache(p.ID, "A.GCO", 1, nil, []byte("thumb"), "image/png"); err != nil {
		t.Fatal(err)
	}
	if err := d.DeletePrinter(p.ID); err != nil {
		t.Fatal(err)
	}
	if _, hit, _ := d.GetFileMetaCache(p.ID, "A.GCO"); hit {
		t.Error("file_meta_cache row survived printer delete")
	}
}

func TestPruneOrphanedFileMeta(t *testing.T) {
	d := openTest(t)
	p := models.PrinterConfig{Name: "p", URL: "u", APIKey: "k"}
	d.CreatePrinter(&p)
	d.SetFileMetaCache(p.ID, "keep", 1, nil, nil, "")
	d.SetFileMetaCache(999, "orphan", 1, nil, nil, "") // no such printer
	if n, err := d.PruneOrphanedFileMeta(); err != nil || n != 1 {
		t.Fatalf("pruned %d (%v), want 1", n, err)
	}
	if _, hit, _ := d.GetFileMetaCache(p.ID, "keep"); !hit {
		t.Error("pruned a live printer's row")
	}
}

func TestRecoverInterruptedIngestJobs(t *testing.T) {
	d := openTest(t)
	p := models.PrinterConfig{Name: "p", URL: "u", APIKey: "k"}
	d.CreatePrinter(&p)
	tid, err := d.CreateIngestTarget("m", &p.ID, "lbl", "key")
	if err != nil {
		t.Fatal(err)
	}
	stuck, _ := d.CreateIngestJob(tid, "a.gcode", false, 1)
	pending, _ := d.CreateIngestJob(tid, "b.gcode", false, 1)
	if ok, err := d.ClaimIngestJobForDispatch(stuck, p.ID); !ok || err != nil {
		t.Fatalf("claim: %v %v", ok, err)
	}
	if n, err := d.RecoverInterruptedIngestJobs(); err != nil || n != 1 {
		t.Fatalf("recovered %d (%v), want 1", n, err)
	}
	if j, _ := d.GetIngestJob(stuck); j.Status != "failed" {
		t.Errorf("stuck job status = %q, want failed", j.Status)
	}
	if j, _ := d.GetIngestJob(pending); j.Status != "pending" {
		t.Errorf("pending job status = %q, want pending", j.Status)
	}
	if ok, _ := d.ClaimIngestJobForDispatch(stuck, p.ID); !ok {
		t.Error("recovered job can't be retried")
	}
}
