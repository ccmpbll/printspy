package poller

import (
	"testing"

	"github.com/ccmpbll/printspy/db"
	"github.com/ccmpbll/printspy/models"
)

func TestPatchPowerStateUpdatesAllMatchingIDs(t *testing.T) {
	// Same physical plug can appear twice: once auto-detected by a plugin
	// (e.g. OctoPrint's own Tasmota integration) and once as a separately
	// assigned direct smart plug pointing at the same device.
	p := &Poller{
		printers: make(map[int64]*polledPrinter),
		cache: map[int64]*models.PrinterStatus{
			1: {
				Power: []models.PowerState{
					{ID: "10.0.0.5:1", Label: "Printer", On: true, Source: "tasmota"},
					{ID: "10.0.0.5:1", Label: "CoreOne", On: true, Source: "tasmota-direct"},
					{ID: "10.0.0.6:1", Label: "Lights", On: true, Source: "tasmota-direct"},
				},
			},
		},
		subscribers: make(map[*subscriber]struct{}),
	}

	p.patchPowerState(1, "10.0.0.5:1", "", false)

	got := p.cache[1].Power
	if got[0].On {
		t.Errorf("entry 0 (matching ID) still On, want patched to false")
	}
	if got[1].On {
		t.Errorf("entry 1 (duplicate matching ID) still On, want patched to false")
	}
	if !got[2].On {
		t.Errorf("entry 2 (different ID) was patched, want untouched")
	}
	if got[0].Source != "tasmota" {
		t.Errorf("entry 0 Source = %q, want unchanged (empty source arg means leave as-is)", got[0].Source)
	}
}

func TestPatchPowerStateOverridesSourceWhenGiven(t *testing.T) {
	p := &Poller{
		printers: make(map[int64]*polledPrinter),
		cache: map[int64]*models.PrinterStatus{
			1: {Power: []models.PowerState{{ID: "10.0.0.5:1", On: true, Source: "tasmota-direct"}}},
		},
		subscribers: make(map[*subscriber]struct{}),
	}

	p.patchPowerState(1, "10.0.0.5:1", "auto-idle", false)

	got := p.cache[1].Power[0]
	if got.On {
		t.Errorf("On = true, want false")
	}
	if got.Source != "auto-idle" {
		t.Errorf("Source = %q, want %q", got.Source, "auto-idle")
	}
}

// seedPrintingState is what stops a restart mid-print from firing a
// spurious "Print Started" (handled separately in poll()) and from
// re-firing a checkpoint whose threshold already passed before printspy
// came back up - it should mark checkpoints already crossed as notified,
// but leave ones still ahead alone so they fire normally when reached.
func TestSeedPrintingStateMarksOnlyPassedCheckpoints(t *testing.T) {
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()
	database.SetSetting("notify_checkpoint1_percent", "5")
	database.SetSetting("notify_checkpoint2_percent", "50")

	p := &Poller{db: database, printers: map[int64]*polledPrinter{1: {}}}
	p.seedPrintingState(1, &models.PrinterStatus{Job: &models.JobInfo{Progress: 20}})

	pp := p.printers[1]
	if !pp.notifiedCheckpoint1 {
		t.Errorf("checkpoint1 (5%%) should be marked notified at 20%% progress")
	}
	if pp.notifiedCheckpoint2 {
		t.Errorf("checkpoint2 (50%%) should NOT be marked notified at 20%% progress - hasn't happened yet")
	}
}

func TestSeedPrintingStateNoJobIsNoop(t *testing.T) {
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()

	p := &Poller{db: database, printers: map[int64]*polledPrinter{1: {}}}
	p.seedPrintingState(1, &models.PrinterStatus{Job: nil})

	pp := p.printers[1]
	if pp.notifiedCheckpoint1 || pp.notifiedCheckpoint2 {
		t.Errorf("no Job on status should leave both checkpoints unmarked")
	}
}
