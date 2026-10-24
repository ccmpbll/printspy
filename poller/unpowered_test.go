package poller

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ccmpbll/printspy/db"
	"github.com/ccmpbll/printspy/models"
)

// unpoweredRig is a poller with one printer, optionally one assigned plug whose
// reading the test controls, and a plugin that counts how often it is asked.
type unpoweredRig struct {
	p      *Poller
	pp     *polledPrinter
	id     int64
	plugOn atomic.Bool
	calls  atomic.Int32
	fail   atomic.Bool
	pl     *fnPlugin
}

// newUnpoweredRig's printer was asked just now, so the 5 minute probe is not due.
func newUnpoweredRig(t *testing.T, typ string, withPlug bool) *unpoweredRig {
	t.Helper()
	r := &unpoweredRig{}
	var d *db.DB
	r.p, d, r.id = newLifecyclePoller(t)
	r.pl = &fnPlugin{typ: typ, get: func(context.Context) (*models.PrinterStatus, error) {
		r.calls.Add(1)
		if r.fail.Load() {
			return nil, errors.New("no route to host")
		}
		return &models.PrinterStatus{State: models.StateIdle, LastUpdated: time.Now()}, nil
	}}
	r.pp = &polledPrinter{plugin: r.pl, lastRequest: time.Now()}
	r.p.printers[r.id] = r.pp
	if withPlug {
		if _, err := d.CreateSmartPlug("10.0.0.9", "1", "plug", false, &r.id, ""); err != nil {
			t.Fatal(err)
		}
	}
	r.p.fetchPower = func(context.Context, int64, []models.SmartPlug) []models.PowerState {
		return []models.PowerState{{ID: "10.0.0.9:1", On: r.plugOn.Load()}}
	}
	return r
}

// cache puts a status in the poller as the last poll would have left it.
func (r *unpoweredRig) cache(state models.PrinterState, plugOn bool) {
	r.p.mu.Lock()
	r.p.cache[r.id] = &models.PrinterStatus{State: state, Power: []models.PowerState{{ID: "10.0.0.9:1", On: plugOn}}}
	r.p.mu.Unlock()
}

func (r *unpoweredRig) poll()                      { r.p.poll(context.Background(), r.id, r.pl) }
func (r *unpoweredRig) state() models.PrinterState { return r.p.GetStatus(r.id).State }

func TestPrusaLinkIsNotAskedWhileOfflineWithItsPlugOff(t *testing.T) {
	r := newUnpoweredRig(t, "prusalink", true)
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
	r := newUnpoweredRig(t, "prusalink", true)
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
	if !r.p.GetStatus(r.id).Power[0].On {
		t.Errorf("plug reading was not refreshed while the printer was skipped")
	}
}

func TestAPrintingPrinterIsPolledUntilItIsRecordedOffline(t *testing.T) {
	for _, state := range []models.PrinterState{models.StatePrinting, models.StatePaused, models.StateIdle} {
		r := newUnpoweredRig(t, "prusalink", true)
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
	// The plug could be a lamp, and the printer could be up: it must not stay
	// "offline" just because the plug reads off.
	r := newUnpoweredRig(t, "prusalink", true)
	r.cache(models.StateOffline, false)
	r.pp.lastRequest = time.Now().Add(-10 * time.Minute)
	r.poll()
	if n := r.calls.Load(); n != 1 {
		t.Fatalf("overdue probe: printer asked %d times, want 1", n)
	}
	if got := r.state(); got != models.StateIdle {
		t.Errorf("a reachable printer stayed %s", got)
	}
	// The probe resets the clock: the next ticks skip again.
	r.cache(models.StateOffline, false)
	r.poll()
	if n := r.calls.Load(); n != 1 {
		t.Errorf("asked again right after a probe (%d calls)", n)
	}
}

func TestPollingIsUnchangedOutsideTheSkipCase(t *testing.T) {
	for _, tc := range []struct {
		name      string
		typ       string
		withPlug  bool
		cached    bool // a status is cached (offline, plug reading below)
		plugOn    bool
		neverAsk  bool // lastRequest zero, as before the first poll
		wantCalls int32
	}{
		{"octoprint, plug off", "octoprint", true, true, false, false, 1},
		{"prusalink, no plug assigned", "prusalink", false, true, false, false, 1},
		{"prusalink, plug on", "prusalink", true, true, true, false, 1},
		{"prusalink, nothing cached yet", "prusalink", true, false, false, false, 1},
		{"prusalink, never asked", "prusalink", true, true, false, true, 1},
		{"prusalink, offline, plug off (the skip)", "prusalink", true, true, false, false, 0},
	} {
		r := newUnpoweredRig(t, tc.typ, tc.withPlug)
		if tc.cached {
			r.cache(models.StateOffline, tc.plugOn)
		}
		r.plugOn.Store(tc.plugOn)
		if tc.neverAsk {
			r.pp.lastRequest = time.Time{}
		}
		r.poll()
		if n := r.calls.Load(); n != tc.wantCalls {
			t.Errorf("%s: printer asked %d times, want %d", tc.name, n, tc.wantCalls)
		}
	}
}

func TestASkippedPollKeepsWhatThePrinterLastReported(t *testing.T) {
	for _, state := range []models.PrinterState{models.StateOffline, models.StateDisconnected} {
		r := newUnpoweredRig(t, "prusalink", true)
		r.p.mu.Lock()
		r.p.cache[r.id] = &models.PrinterStatus{State: state, StateMessage: "Unable to reach PrusaLink", Power: []models.PowerState{{ID: "10.0.0.9:1"}}}
		r.p.mu.Unlock()
		r.poll()
		r.poll()
		if n := r.calls.Load(); n != 0 {
			t.Fatalf("%s: skip case was asked %d times", state, n)
		}
		st := r.p.GetStatus(r.id)
		if st.State != state || st.StateMessage != "Unable to reach PrusaLink" {
			t.Errorf("%s: skipped poll left %s / %q, want the cached state and reason kept", state, st.State, st.StateMessage)
		}
	}
}
