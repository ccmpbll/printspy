package poller

import (
	"bytes"
	"context"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
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
	typ string
}

func (f *fnPlugin) Type() string { return f.typ }

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

// runKeepalive runs the keepalive loop for a printer in the given state (empty
// = nothing polled yet) with ping standing in for the ICMP call, until stop
// returns true or two seconds pass.
func runKeepalive(t *testing.T, state models.PrinterState, ping func(string, time.Duration) error, stop func() bool, setup ...func(p *Poller, d *db.DB, id int64)) {
	t.Helper()
	p, d, id := newLifecyclePoller(t)
	p.ping = ping
	if state != "" {
		p.mu.Lock()
		p.cache[id] = &models.PrinterStatus{State: state}
		p.mu.Unlock()
	}
	for _, fn := range setup {
		fn(p, d, id)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { p.keepaliveLoop(ctx, id, "p", "10.0.0.1", 5*time.Millisecond); close(done) }()
	for deadline := time.Now().Add(2 * time.Second); !stop() && time.Now().Before(deadline); {
		time.Sleep(2 * time.Millisecond)
	}
	cancel()
	<-done
}

func TestKeepaliveProbesAnOfflinePrinterOnlyOncePerInterval(t *testing.T) {
	var pings atomic.Int32
	count := func(string, time.Duration) error { pings.Add(1); return nil }

	// Offline: the first tick probes, then nothing for the rest of the (one minute) interval.
	var first time.Time
	runKeepalive(t, models.StateOffline, count, func() bool {
		if pings.Load() >= 1 && first.IsZero() {
			first = time.Now()
		}
		return !first.IsZero() && time.Since(first) >= 100*time.Millisecond // 20 more ticks
	})
	if n := pings.Load(); n != 1 {
		t.Errorf("offline printer was pinged %d times inside one probe interval, want 1", n)
	}

	// Online (and not yet polled): every tick pings.
	for _, state := range []models.PrinterState{models.StateIdle, ""} {
		pings.Store(0)
		runKeepalive(t, state, count, func() bool { return pings.Load() >= 3 })
		if n := pings.Load(); n < 3 {
			t.Errorf("state %q: only %d pings, want a ping on every tick", state, n)
		}
	}
}

func TestKeepaliveLogsAFailureOnceUntilItAnswersAgain(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	var calls atomic.Int32
	flaky := func(string, time.Duration) error {
		n := calls.Add(1)
		if n >= 2 && n <= 6 {
			return errors.New("i/o timeout")
		}
		return nil
	}
	runKeepalive(t, models.StateIdle, flaky, func() bool { return calls.Load() >= 9 })

	out := buf.String()
	if got := strings.Count(out, "failed: i/o timeout"); got != 1 {
		t.Errorf("failure logged %d times for 5 failed pings, want once:\n%s", got, out)
	}
	if got := strings.Count(out, "answered again"); got != 1 {
		t.Errorf("recovery logged %d times, want once:\n%s", got, out)
	}
	if strings.Index(out, "failed:") > strings.Index(out, "answered again") {
		t.Errorf("recovery logged before the failure:\n%s", out)
	}
}

func TestKeepaliveSendsNothingWhileAllAssignedPlugsAreOff(t *testing.T) {
	var pings atomic.Int32
	count := func(string, time.Duration) error { pings.Add(1); return nil }
	plug := func(on bool) func(p *Poller, d *db.DB, id int64) {
		return func(p *Poller, d *db.DB, id int64) {
			if _, err := d.CreateSmartPlug("10.0.0.9", "1", "plug", false, &id, ""); err != nil {
				t.Fatal(err)
			}
			p.mu.Lock()
			p.cache[id].Power = []models.PowerState{{ID: "10.0.0.9:1", On: on}}
			p.mu.Unlock()
		}
	}
	start := time.Now()
	runKeepalive(t, models.StateIdle, count, func() bool { return time.Since(start) >= 100*time.Millisecond }, plug(false))
	if n := pings.Load(); n != 0 {
		t.Errorf("printer with its plug off was pinged %d times", n)
	}
	pings.Store(0)
	runKeepalive(t, models.StateIdle, count, func() bool { return pings.Load() >= 3 }, plug(true))
	if n := pings.Load(); n < 3 {
		t.Errorf("printer with its plug on: only %d pings", n)
	}
}

func TestKeepaliveLogsANewFailureAfterAPlugOffPeriod(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	p, d, id := newLifecyclePoller(t)
	if _, err := d.CreateSmartPlug("10.0.0.9", "1", "plug", false, &id, ""); err != nil {
		t.Fatal(err)
	}
	setPlug := func(on bool) {
		p.mu.Lock()
		p.cache[id] = &models.PrinterStatus{State: models.StateIdle, Power: []models.PowerState{{ID: "10.0.0.9:1", On: on}}}
		p.mu.Unlock()
	}
	var calls atomic.Int32
	p.ping = func(string, time.Duration) error { calls.Add(1); return errors.New("i/o timeout") }

	setPlug(true)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { p.keepaliveLoop(ctx, id, "p", "10.0.0.1", 5*time.Millisecond); close(done) }()
	waitFor := func(cond func() bool) {
		for deadline := time.Now().Add(2 * time.Second); !cond() && time.Now().Before(deadline); {
			time.Sleep(2 * time.Millisecond)
		}
	}
	waitFor(func() bool { return calls.Load() >= 3 }) // failing, logged once
	setPlug(false)                                    // powered down: pings pause
	time.Sleep(30 * time.Millisecond)
	before := calls.Load()
	setPlug(true) // powered up, still not answering
	waitFor(func() bool { return calls.Load() >= before+2 })
	cancel()
	<-done

	if got := strings.Count(buf.String(), "failed: i/o timeout"); got != 2 {
		t.Errorf("failure logged %d times across two episodes, want 2:\n%s", got, buf.String())
	}
}
