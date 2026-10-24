package poller

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ccmpbll/printspy/db"
	"github.com/ccmpbll/printspy/models"
	_ "github.com/ccmpbll/printspy/plugin/octoprint" // register the real plugin types for AddPrinter
	_ "github.com/ccmpbll/printspy/plugin/prusalink"
)

// unpoweredRig is a poller with one printer, one assigned plug whose reading the
// test controls, and a plugin that counts how often it is asked for status.
type unpoweredRig struct {
	p      *Poller
	pp     *polledPrinter
	id     int64
	plugOn atomic.Bool
	calls  atomic.Int32
	fail   atomic.Bool
	pl     *fnPlugin
}

func newUnpoweredRig(t *testing.T, prusalink, withPlug bool) *unpoweredRig {
	t.Helper()
	r := &unpoweredRig{}
	var d *db.DB
	r.p, d, r.id = newLifecyclePoller(t)
	r.pl = &fnPlugin{get: func(context.Context) (*models.PrinterStatus, error) {
		r.calls.Add(1)
		if r.fail.Load() {
			return nil, errors.New("no route to host")
		}
		return &models.PrinterStatus{State: models.StateIdle, LastUpdated: time.Now()}, nil
	}}
	r.pp = &polledPrinter{plugin: r.pl, skipWhenUnpowered: prusalink}
	r.p.printers[r.id] = r.pp
	if withPlug {
		if _, err := d.CreateSmartPlug("10.0.0.9", "1", "plug", false, &r.id, ""); err != nil {
			t.Fatal(err)
		}
	}
	orig := directPowerFn
	directPowerFn = func(*Poller, context.Context, int64, []models.SmartPlug) []models.PowerState {
		return []models.PowerState{{ID: "10.0.0.9:1", On: r.plugOn.Load()}}
	}
	t.Cleanup(func() { directPowerFn = orig })
	return r
}

// cache puts a status in the poller as the last poll would have left it.
func (r *unpoweredRig) cache(state models.PrinterState, plugOn bool) {
	r.p.mu.Lock()
	r.p.cache[r.id] = &models.PrinterStatus{State: state, Power: []models.PowerState{{ID: "10.0.0.9:1", On: plugOn}}}
	r.p.mu.Unlock()
	r.pp.lastRequest = time.Now()
}

// setCache is cache without touching lastRequest.
func (r *unpoweredRig) setCache(state models.PrinterState, plugOn bool) {
	r.p.mu.Lock()
	r.p.cache[r.id] = &models.PrinterStatus{State: state, Power: []models.PowerState{{ID: "10.0.0.9:1", On: plugOn}}}
	r.p.mu.Unlock()
}

func (r *unpoweredRig) poll() { r.p.poll(context.Background(), r.id, r.pl) }

func (r *unpoweredRig) state() models.PrinterState { return r.p.GetStatus(r.id).State }

func TestPrusaLinkIsNotAskedWhileOfflineWithItsPlugOff(t *testing.T) {
	r := newUnpoweredRig(t, true, true)
	r.cache(models.StateOffline, false)
	for i := 0; i < 3; i++ {
		r.poll()
	}
	if n := r.calls.Load(); n != 0 {
		t.Errorf("printer was asked for status %d times while offline with its plug off", n)
	}
	if got := r.state(); got != models.StateOffline {
		t.Errorf("state = %s, want offline", got)
	}
}

func TestPollingResumesWhenThePlugComesOn(t *testing.T) {
	r := newUnpoweredRig(t, true, true)
	r.cache(models.StateOffline, false)
	r.plugOn.Store(true)
	// The plug reading is refreshed on every tick even while the printer is
	// skipped: this tick still skips on the old reading but stores the new one,
	// the next tick asks the printer.
	r.poll()
	r.poll()
	if n := r.calls.Load(); n != 1 {
		t.Fatalf("printer asked %d times after the plug came on, want 1", n)
	}
	if got := r.state(); got != models.StateIdle {
		t.Errorf("state = %s, want idle once it answers", got)
	}
	if on := r.p.GetStatus(r.id).Power[0].On; !on {
		t.Errorf("plug reading was not refreshed while the printer was skipped")
	}
}

func TestAPrintingPrinterIsPolledUntilItIsRecordedOffline(t *testing.T) {
	for _, state := range []models.PrinterState{models.StatePrinting, models.StatePaused, models.StateIdle} {
		r := newUnpoweredRig(t, true, true)
		r.cache(state, false) // the plug just went off under it
		r.fail.Store(true)    // and the printer no longer answers
		r.poll()
		if n := r.calls.Load(); n != 1 {
			t.Errorf("%s: printer asked %d times, want 1 (it must be polled until it is recorded offline)", state, n)
		}
		if got := r.state(); got != models.StateOffline {
			t.Errorf("%s: state = %s after the failed poll, want offline", state, got)
		}
		r.poll()
		r.poll()
		if n := r.calls.Load(); n != 1 {
			t.Errorf("%s: asked again after being recorded offline (%d calls)", state, n)
		}
	}
}

func TestOfflinePrinterIsStillProbedNowAndThen(t *testing.T) {
	origEvery := unpoweredProbeEvery
	unpoweredProbeEvery = time.Minute
	t.Cleanup(func() { unpoweredProbeEvery = origEvery })

	// The plug could be a lamp, and the printer could be up: it must not stay
	// "offline" just because the plug reads off.
	r := newUnpoweredRig(t, true, true)
	r.cache(models.StateOffline, false)
	r.pp.lastRequest = time.Now().Add(-2 * time.Minute)
	r.poll()
	if n := r.calls.Load(); n != 1 {
		t.Fatalf("overdue probe: printer asked %d times, want 1", n)
	}
	if got := r.state(); got != models.StateIdle {
		t.Errorf("a reachable printer stayed %s", got)
	}
	// And the probe resets the clock: the next ticks skip again.
	r.setCache(models.StateOffline, false)
	r.poll()
	if n := r.calls.Load(); n != 1 {
		t.Errorf("asked again right after a probe (%d calls)", n)
	}
}

func TestPollingIsUnchangedWithoutAnAssignedPlugOrForOtherPrinterTypes(t *testing.T) {
	// Not PrusaLink (OctoPrint): always asked.
	r := newUnpoweredRig(t, false, true)
	r.cache(models.StateOffline, false)
	r.poll()
	if n := r.calls.Load(); n != 1 {
		t.Errorf("non-PrusaLink printer asked %d times, want 1", n)
	}
	// PrusaLink with no plug assigned: nothing to go on, always asked.
	r = newUnpoweredRig(t, true, false)
	r.cache(models.StateOffline, false)
	r.poll()
	if n := r.calls.Load(); n != 1 {
		t.Errorf("printer with no plug asked %d times, want 1", n)
	}
	// PrusaLink whose plug is on: always asked.
	r = newUnpoweredRig(t, true, true)
	r.cache(models.StateOffline, true)
	r.plugOn.Store(true)
	r.poll()
	if n := r.calls.Load(); n != 1 {
		t.Errorf("printer with its plug on asked %d times, want 1", n)
	}
	// First poll ever (nothing cached yet): asked, even right after an earlier
	// request that left nothing in the cache (a cancelled poll).
	r = newUnpoweredRig(t, true, true)
	r.poll()
	if n := r.calls.Load(); n != 1 {
		t.Errorf("first poll asked %d times, want 1", n)
	}
	r = newUnpoweredRig(t, true, true)
	r.pp.lastRequest = time.Now()
	r.poll()
	if n := r.calls.Load(); n != 1 {
		t.Errorf("poll with nothing cached asked %d times, want 1", n)
	}
}

func TestAStalePollDoesNotTouchTheReplacementPrinter(t *testing.T) {
	r := newUnpoweredRig(t, true, true)
	replacement := &polledPrinter{plugin: &fnPlugin{get: r.pl.get}, skipWhenUnpowered: true}
	old := time.Now().Add(-time.Hour)
	replacement.lastRequest = old
	r.p.mu.Lock()
	r.p.printers[r.id] = replacement // the printer was edited while this poll was in flight
	r.p.mu.Unlock()
	r.poll() // polls with the original plugin
	if !replacement.lastRequest.Equal(old) {
		t.Errorf("a stale poll moved the replacement's lastRequest")
	}
}

// AddPrinter is where "PrusaLink only" is decided.
func TestOnlyPrusaLinkPrintersSkipPollingWhileUnpowered(t *testing.T) {
	for typ, want := range map[string]bool{"prusalink": true, "octoprint": false} {
		p, _, id := newLifecyclePoller(t)
		ctx, cancel := context.WithCancel(context.Background())
		p.AddPrinter(ctx, models.PrinterConfig{ID: id + 100, Name: typ, Type: typ, URL: "http://127.0.0.1:1", APIKey: "k", Enabled: true})
		p.mu.RLock()
		pp := p.printers[id+100]
		p.mu.RUnlock()
		if pp == nil {
			t.Fatalf("%s: printer was not added", typ)
		}
		if pp.skipWhenUnpowered != want {
			t.Errorf("%s: skipWhenUnpowered = %v, want %v", typ, pp.skipWhenUnpowered, want)
		}
		cancel()
		p.RemovePrinter(id + 100)
	}
}
