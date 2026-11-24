package api

import (
	"context"
	"testing"
	"time"

	"github.com/ccmpbll/printspy/models"
)

// A control request whose client goes away must stop waiting for the
// printer's state to change instead of repolling until the timeout.
func TestWaitForStateChangeStopsOnCancel(t *testing.T) {
	h, d := newTestHandler(t)
	cfg := models.PrinterConfig{Name: "P", Type: "prusalink", URL: "http://127.0.0.1:1", APIKey: "k", PollInterval: 60, Enabled: true}
	if err := d.CreatePrinter(&cfg); err != nil {
		t.Fatal(err)
	}
	pctx, stop := context.WithCancel(context.Background())
	defer stop()
	h.poller.AddPrinter(pctx, cfg)
	h.poller.Repoll(pctx, cfg.ID)
	// netguard blocks loopback, so the printer stays offline: a state
	// that never changes.
	if s := h.poller.GetStatus(cfg.ID); s == nil || s.State != models.StateOffline {
		t.Fatalf("setup: status %+v, want offline", s)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	h.waitForStateChange(ctx, cfg.ID, models.StateOffline)
	if took := time.Since(start); took > 3*time.Second {
		t.Errorf("waited %s after the request ended, want it to stop", took)
	}
}
