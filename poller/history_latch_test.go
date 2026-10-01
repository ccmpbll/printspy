package poller

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/ccmpbll/printspy/db"
	"github.com/ccmpbll/printspy/models"
	"github.com/ccmpbll/printspy/plugin"
)

// scriptedPlugin replays a fixed sequence of poll results.
type scriptedPlugin struct {
	plugin.PrinterPlugin
	states []models.PrinterState
	i      int
}

func (s *scriptedPlugin) GetStatus(ctx context.Context) (*models.PrinterStatus, error) {
	st := &models.PrinterStatus{State: s.states[s.i], LastUpdated: time.Now()}
	s.i++
	if st.State == models.StatePrinting || st.State == models.StatePaused {
		st.Job = &models.JobInfo{FileName: "a.gcode", Progress: 50, ElapsedSecs: 600}
	}
	return st, nil
}

// historyRows runs the scripted states through poll() and returns how many
// print_history rows resulted. beforeLast, if set, runs before the final poll.
func historyRows(t *testing.T, beforeLast func(pp *polledPrinter), states ...models.PrinterState) int {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	cfg := &models.PrinterConfig{Name: "p", Type: "octoprint", URL: "http://x", Enabled: true}
	if err := database.CreatePrinter(cfg); err != nil {
		t.Fatal(err)
	}

	p := New(database)
	sp := &scriptedPlugin{states: states}
	p.printers[cfg.ID] = &polledPrinter{plugin: sp}
	for i := range states {
		if i == len(states)-1 && beforeLast != nil {
			beforeLast(p.printers[cfg.ID])
		}
		p.poll(context.Background(), cfg.ID, sp)
	}
	rows, _, err := database.ListPrintHistory(cfg.ID, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	return len(rows)
}

func TestHistoryIgnoresTransientOffline(t *testing.T) {
	const P, O, I, D = models.StatePrinting, models.StateOffline, models.StateIdle, models.StateDisconnected
	cases := []struct {
		name       string
		beforeLast func(*polledPrinter)
		states     []models.PrinterState
		want       int
	}{
		{"blip then still printing", nil, []models.PrinterState{P, O, P}, 0},
		{"blip then finished", nil, []models.PrinterState{P, O, I}, 1},
		{"finished once", nil, []models.PrinterState{P, I, I}, 1},
		{"disconnected after print records once", nil, []models.PrinterState{P, D, D}, 1},
		{"long outage not recorded", func(pp *polledPrinter) { pp.offlineSince = time.Now().Add(-time.Hour) }, []models.PrinterState{P, O, I}, 0},
	}
	for _, c := range cases {
		if got := historyRows(t, c.beforeLast, c.states...); got != c.want {
			t.Errorf("%s: %d history rows, want %d", c.name, got, c.want)
		}
	}
}

func TestPollDiscardedAfterPrinterReplaced(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	p := New(database)
	old := &scriptedPlugin{states: []models.PrinterState{models.StateIdle}}
	p.printers[1] = &polledPrinter{plugin: &scriptedPlugin{}} // a newer plugin owns id 1
	p.poll(context.Background(), 1, old)
	if _, ok := p.cache[1]; ok {
		t.Error("stale poll wrote the cache")
	}
}
