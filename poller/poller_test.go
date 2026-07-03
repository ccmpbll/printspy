package poller

import (
	"testing"

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

	p.patchPowerState(1, "10.0.0.5:1", false)

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
}
