package api

import (
	"context"
	"testing"
	"time"

	"github.com/ccmpbll/printspy/models"
)

// offlinePrinter adds a printer whose polls always fail fast (netguard
// blocks loopback), so its state is offline and never changes.
func offlinePrinter(t *testing.T) (*Handler, int64) {
	t.Helper()
	h, d := newTestHandler(t)
	cfg := models.PrinterConfig{Name: "P", Type: "prusalink", URL: "http://127.0.0.1:1", APIKey: "k", PollInterval: 60, Enabled: true}
	if err := d.CreatePrinter(&cfg); err != nil {
		t.Fatal(err)
	}
	pctx, stop := context.WithCancel(context.Background())
	// Runs before newTestHandler's DB close (cleanups are LIFO).
	t.Cleanup(func() { stop(); h.poller.Wait() })
	h.poller.AddPrinter(pctx, cfg)
	h.poller.Repoll(pctx, cfg.ID)
	if s := h.poller.GetStatus(cfg.ID); s == nil || s.State != models.StateOffline {
		t.Fatalf("setup: status %+v, want offline", s)
	}
	return h, cfg.ID
}

// A control request whose client goes away must stop waiting for the
// printer's state to change instead of repolling until the timeout.
func TestWaitForStateChangeStopsOnCancel(t *testing.T) {
	h, id := offlinePrinter(t)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, changed := h.waitForStateChange(ctx, id, models.StateOffline); changed {
		t.Error("changed = true for a state that never changed")
	}
	if took := time.Since(start); took > 3*time.Second {
		t.Errorf("waited %s after the request ended, want it to stop", took)
	}
}

// No state change before the timeout is reported as changed=false.
func TestWaitForStateChangeTimeout(t *testing.T) {
	h, id := offlinePrinter(t)
	if err := h.db.SetSetting("print_control_timeout_secs", "1"); err != nil {
		t.Fatal(err)
	}
	if _, changed := h.waitForStateChange(context.Background(), id, models.StateOffline); changed {
		t.Error("changed = true after a timeout")
	}
	if _, changed := h.waitForStateChange(context.Background(), id, models.StateIdle); !changed {
		t.Error("changed = false although the state differs")
	}
}
