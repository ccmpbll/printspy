package poller

import (
	"context"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ccmpbll/printspy/db"
	"github.com/ccmpbll/printspy/models"
	"github.com/ccmpbll/printspy/plugin"
)

type fnPlugin struct {
	plugin.PrinterPlugin
	get func(ctx context.Context) (*models.PrinterStatus, error)
}

func (f *fnPlugin) GetStatus(ctx context.Context) (*models.PrinterStatus, error) { return f.get(ctx) }
func (f *fnPlugin) Connect(ctx context.Context) error                            { return nil }

func newLifecyclePoller(t *testing.T) (*Poller, *db.DB, int64) {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	cfg := &models.PrinterConfig{Name: "p", Type: "octoprint", URL: "http://x", APIKey: "k", Enabled: true}
	if err := d.CreatePrinter(cfg); err != nil {
		t.Fatal(err)
	}
	return New(d), d, cfg.ID
}

func TestCancelledPollDoesNotPoisonCache(t *testing.T) {
	p, _, id := newLifecyclePoller(t)
	healthy := &fnPlugin{get: func(ctx context.Context) (*models.PrinterStatus, error) {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return &models.PrinterStatus{State: models.StateIdle, LastUpdated: time.Now()}, nil
	}}
	p.printers[id] = &polledPrinter{plugin: healthy}
	p.poll(context.Background(), id, healthy)
	if st := p.GetStatus(id); st == nil || st.State != models.StateIdle {
		t.Fatalf("setup: %+v", st)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // e.g. browser tab closed during the print-control settle loop
	p.poll(ctx, id, healthy)
	if st := p.GetStatus(id); st.State != models.StateIdle {
		t.Errorf("cancelled poll overwrote state with %s", st.State)
	}

	// A genuine failure with a live context must still mark it offline.
	broken := &fnPlugin{get: func(context.Context) (*models.PrinterStatus, error) { return nil, errors.New("conn refused") }}
	p.printers[id].plugin = broken
	p.poll(context.Background(), id, broken)
	if st := p.GetStatus(id); st.State != models.StateOffline {
		t.Errorf("real failure state = %s, want offline", st.State)
	}
}

func TestPollIntervalChangeAppliesWithoutRestart(t *testing.T) {
	p, d, id := newLifecyclePoller(t)
	var polls atomic.Int32
	pl := &fnPlugin{get: func(context.Context) (*models.PrinterStatus, error) {
		polls.Add(1)
		return &models.PrinterStatus{State: models.StateIdle, LastUpdated: time.Now()}, nil
	}}
	p.printers[id] = &polledPrinter{plugin: pl}
	d.SetSetting("poll_interval", "1")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.pollLoop(ctx, id, "p", pl, 10)

	time.Sleep(300 * time.Millisecond) // first immediate poll done
	d.SetSetting("poll_interval", "3600")
	time.Sleep(3 * time.Second) // tick at 1s fires, sees the new value, stops ticking
	if n := polls.Load(); n != 2 {
		t.Errorf("polls = %d, want 2 (immediate + one tick, then 3600s interval)", n)
	}
}

func TestGoTrackedWaitsAndDropsAfterClose(t *testing.T) {
	p, _, _ := newLifecyclePoller(t)
	var done atomic.Bool
	p.Go(func() { time.Sleep(200 * time.Millisecond); done.Store(true) })
	p.Wait()
	if !done.Load() {
		t.Error("Wait returned before tracked goroutine finished")
	}
	var ran atomic.Bool
	p.Go(func() { ran.Store(true) })
	time.Sleep(50 * time.Millisecond)
	if ran.Load() {
		t.Error("goroutine started after shutdown began")
	}
}

func TestSkipKeepaliveWhilePrinterOffline(t *testing.T) {
	p, _, id := newLifecyclePoller(t)
	if p.skipKeepalive(id) {
		t.Errorf("no status cached yet: want keep pinging")
	}
	for state, want := range map[models.PrinterState]bool{
		models.StateOffline:  true,
		models.StateIdle:     false,
		models.StatePrinting: false,
	} {
		p.mu.Lock()
		p.cache[id] = &models.PrinterStatus{State: state}
		p.mu.Unlock()
		if got := p.skipKeepalive(id); got != want {
			t.Errorf("state %s: skipKeepalive = %v, want %v", state, got, want)
		}
	}
	// The poll loop is what notices the printer coming back: once it caches
	// a reachable state the pings resume.
	p.mu.Lock()
	p.cache[id] = &models.PrinterStatus{State: models.StateOffline}
	p.mu.Unlock()
	if !p.skipKeepalive(id) {
		t.Fatal("setup: offline should skip")
	}
	p.mu.Lock()
	p.cache[id] = &models.PrinterStatus{State: models.StateIdle}
	p.mu.Unlock()
	if p.skipKeepalive(id) {
		t.Errorf("printer back online: want pings to resume")
	}
}

func TestKeepaliveLoopSendsNoPingsToAnOfflinePrinter(t *testing.T) {
	var pings atomic.Int32
	orig := pingFn
	pingFn = func(string, time.Duration) error { pings.Add(1); return nil }
	t.Cleanup(func() { pingFn = orig })

	run := func(state models.PrinterState) int32 {
		p, _, id := newLifecyclePoller(t)
		p.mu.Lock()
		p.cache[id] = &models.PrinterStatus{State: state}
		p.mu.Unlock()
		pings.Store(0)
		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
		defer cancel()
		p.keepaliveLoop(ctx, id, "p", "10.0.0.1", 10*time.Millisecond)
		return pings.Load()
	}
	if n := run(models.StateOffline); n != 0 {
		t.Errorf("offline printer was pinged %d times", n)
	}
	if n := run(models.StateIdle); n == 0 {
		t.Errorf("idle printer was never pinged")
	}
}
