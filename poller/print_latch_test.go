package poller

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ccmpbll/printspy/db"
	"github.com/ccmpbll/printspy/models"
	"github.com/ccmpbll/printspy/notify"
	"github.com/ccmpbll/printspy/plugin"
)

// tick is one scripted poll: a state and, if job is true, an active job at pct.
type tick struct {
	state models.PrinterState
	job   bool
	pct   float64
}

type tickPlugin struct {
	plugin.PrinterPlugin
	ticks []tick
	i     int
}

func (s *tickPlugin) GetStatus(ctx context.Context) (*models.PrinterStatus, error) {
	t := s.ticks[s.i]
	s.i++
	st := &models.PrinterStatus{State: t.state}
	if t.job {
		st.Job = &models.JobInfo{FileName: "a.gcode", Progress: t.pct, ElapsedSecs: int(t.pct) * 10}
	}
	return st, nil
}

// runTicks polls through ticks (after one idle poll, so prev is not nil and
// the restart-seeding path stays out of the way) and returns the number of
// history rows, the newest row's duration and each Pushover title sent.
func runTicks(t *testing.T, ticks ...tick) (rows, dur int, sent []string) {
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
	for k, v := range map[string]string{
		"pushover_app_token": "t", "pushover_user_key": "u",
		"notify_on_start": "1", "notify_on_error": "1",
		"notify_checkpoint1_enabled": "1", "notify_checkpoint1_percent": "10",
		"notify_checkpoint2_enabled": "1", "notify_checkpoint2_percent": "50",
	} {
		database.SetSetting(k, v)
	}
	for _, ev := range []string{"start", "error", "checkpoint1", "checkpoint2", "complete", "failed"} {
		database.SetSetting("notify_"+ev+"_image", "none")
	}

	old := sendPushover
	defer func() { sendPushover = old }()
	sendPushover = func(_, _ string, m notify.Message) error {
		sent = append(sent, m.Title)
		return nil
	}

	p := New(database)
	tp := &tickPlugin{ticks: append([]tick{{state: models.StateIdle}}, ticks...)}
	p.printers[cfg.ID] = &polledPrinter{plugin: tp}
	for range tp.ticks {
		p.poll(context.Background(), cfg.ID, tp)
	}
	h, _, err := database.ListPrintHistory(cfg.ID, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(h) > 0 {
		dur = h[0].DurationSecs
	}
	return len(h), dur, sent
}

func count(sent []string, sub string) int {
	n := 0
	for _, s := range sent {
		if strings.Contains(s, sub) {
			n++
		}
	}
	return n
}

func TestPrintLatchAcrossInterruptions(t *testing.T) {
	const P, A, B, O, I, E = models.StatePrinting, models.StateAttention, models.StateBusy, models.StateOffline, models.StateIdle, models.StateError
	pr := func(pct float64) tick { return tick{P, true, pct} }
	cases := []struct {
		name                    string
		ticks                   []tick
		rows, starts, cps, errs int
	}{
		{"plain print", []tick{pr(5), pr(20), pr(60), {I, false, 0}}, 1, 1, 2, 0},
		{"runout then cancel (item 1)", []tick{pr(20), {A, true, 20}, {I, false, 0}}, 1, 1, 1, 1},
		{"busy then cancel (item 1)", []tick{pr(20), {B, true, 20}, {I, false, 0}}, 1, 1, 1, 0},
		{"runout cleared (items 2, 3)", []tick{pr(20), {A, true, 20}, pr(21), pr(22)}, 0, 1, 1, 1},
		{"busy cleared (items 2, 3)", []tick{pr(20), {B, true, 20}, pr(21)}, 0, 1, 1, 0},
		{"offline blip (item 4)", []tick{pr(20), {O, false, 0}, pr(21)}, 0, 1, 1, 0},
		{"offline then done (item 4)", []tick{pr(20), {O, false, 0}, {I, false, 0}}, 1, 1, 1, 0},
		{"error blip (item 4)", []tick{{E, false, 0}, {O, false, 0}, {E, false, 0}}, 0, 0, 0, 1},
		{"two prints", []tick{pr(20), {I, false, 0}, pr(20), {I, false, 0}}, 2, 2, 2, 0},
	}
	for _, c := range cases {
		rows, _, sent := runTicks(t, c.ticks...)
		if rows != c.rows || count(sent, "Print started") != c.starts ||
			count(sent, "Print checkpoint") != c.cps || count(sent, ": error") != c.errs {
			t.Errorf("%s: rows=%d sent=%q; want rows=%d starts=%d checkpoints=%d errors=%d",
				c.name, rows, sent, c.rows, c.starts, c.cps, c.errs)
		}
	}
}

// The history row uses the last answering tick of the print, including
// attention ticks (elapsed time keeps counting under a runout dialog).
func TestHistoryUsesLatestPrintTick(t *testing.T) {
	_, dur, _ := runTicks(t, tick{models.StatePrinting, true, 20}, tick{models.StateAttention, true, 30}, tick{models.StateIdle, false, 0})
	if dur != 300 {
		t.Errorf("duration %d, want 300 (from the attention tick)", dur)
	}
}
