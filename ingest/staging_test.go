package ingest

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/ccmpbll/printspy/db"
)

func TestRemoveStagedOnlyTouchesJobDirs(t *testing.T) {
	root := t.TempDir()
	job := filepath.Join(root, "ingest", "7")
	os.MkdirAll(job, 0755)
	os.WriteFile(filepath.Join(job, "a.gcode"), []byte("x"), 0644)
	other := filepath.Join(root, "keep")
	os.MkdirAll(other, 0755)
	os.WriteFile(filepath.Join(other, "f"), []byte("x"), 0644)

	RemoveStaged("")                        // empty path must not become RemoveAll(".")
	RemoveStaged(filepath.Join(other, "f")) // not under ingest/<id>
	if _, err := os.Stat(filepath.Join(other, "f")); err != nil {
		t.Fatal("removed a dir that isn't a staged job")
	}
	RemoveStaged(filepath.Join(job, "a.gcode"))
	if _, err := os.Stat(job); !os.IsNotExist(err) {
		t.Error("staged job dir not removed")
	}
}

func TestSweepOrphansKeepsLiveJobs(t *testing.T) {
	dataDir := t.TempDir()
	d, err := db.Open(filepath.Join(dataDir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	tid, err := d.CreateIngestTarget("m", nil, "l", "k")
	if err != nil {
		t.Fatal(err)
	}
	live, _ := d.CreateIngestJob(tid, "a.gcode", false, 1)

	mk := func(name string) string {
		p := filepath.Join(dataDir, "ingest", name)
		os.MkdirAll(p, 0755)
		return p
	}
	liveDir := mk(strconv.FormatInt(live, 10))
	orphanDir := mk("9999")
	strayDir := mk("not-a-number")

	SweepOrphans(dataDir, d)

	if _, err := os.Stat(liveDir); err != nil {
		t.Error("swept a dir belonging to a live job")
	}
	if _, err := os.Stat(strayDir); err != nil {
		t.Error("swept a non-numeric dir")
	}
	if _, err := os.Stat(orphanDir); !os.IsNotExist(err) {
		t.Error("orphan dir not removed")
	}
}
