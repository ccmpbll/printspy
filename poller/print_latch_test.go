package poller

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ccmpbll/printspy/db"
	"github.com/ccmpbll/printspy/models"
	"github.com/ccmpbll/printspy/notify"
	"github.com/ccmpbll/printspy/plugin"
)

// tick is one scripted poll: a state and, if job is true, an active job at pct.
type tick struct {
	state     models.PrinterState
	job       bool
	pct       float64
	file      string // default "a.gcode"
	jobState  string
	noElapsed bool // job reports no elapsed time (default pct*10 s)
	gap       bool // printer was unreachable for an hour before this tick
	elapsed   int  // overrides the default elapsed time
	id        int  // job id (PrusaLink), 0 for none
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
		if t.file == "" {
			t.file = "a.gcode"
		}
		st.Job = &models.JobInfo{FileName: t.file, Progress: t.pct, ElapsedSecs: int(t.pct) * 10, JobState: t.jobState}
		if t.elapsed > 0 {
			st.Job.ElapsedSecs = t.elapsed
		}
		if t.noElapsed {
			st.Job.ElapsedSecs = 0
		}
		st.Job.JobID = t.id
	}
	return st, nil
}

// runTicks polls through ticks after one idle poll, so prev is not nil and
// the restart-seeding path stays out of the way.
func runTicks(t *testing.T, ticks ...tick) (rows, dur int, sent []string) {
	return runFrom(t, append([]tick{{state: models.StateIdle}}, ticks...)...)
}

// runFrom polls through ticks from a fresh poller and returns the number of
// history rows, the newest row's duration and each Pushover title sent.
func runFrom(t *testing.T, ticks ...tick) (rows, dur int, sent []string) {
	rows, dur, _, sent = runResult(t, ticks...)
	return
}

// runResult is runFrom plus the newest history row's result.
func runResult(t *testing.T, ticks ...tick) (rows, dur int, result string, sent []string) {
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
		if err := database.SetSetting(k, v); err != nil {
			t.Fatal(err)
		}
	}
	for _, ev := range []string{"start", "error", "checkpoint1", "checkpoint2", "complete", "failed"} {
		if err := database.SetSetting("notify_"+ev+"_image", "none"); err != nil {
			t.Fatal(err)
		}
	}

	old := sendPushover
	defer func() { sendPushover = old }()
	sendPushover = func(_, _ string, m notify.Message) error {
		sent = append(sent, m.Title)
		return nil
	}

	p := New(database)
	tp := &tickPlugin{ticks: ticks}
	p.printers[cfg.ID] = &polledPrinter{plugin: tp}
	for _, tk := range tp.ticks {
		if tk.gap {
			p.printers[cfg.ID].offlineSince = time.Now().Add(-time.Hour)
		}
		p.poll(context.Background(), cfg.ID, tp)
	}
	h, _, err := database.ListPrintHistory(cfg.ID, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(h) > 0 {
		dur, result = h[0].DurationSecs, h[0].Result
	}
	return len(h), dur, result, sent
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
	pr := func(pct float64) tick { return tick{state: P, job: true, pct: pct} }
	cases := []struct {
		name                    string
		ticks                   []tick
		rows, starts, cps, errs int
	}{
		{"plain print", []tick{pr(5), pr(20), pr(60), {state: I}}, 1, 1, 2, 0},
		{"runout then cancel (item 1)", []tick{pr(20), {state: A, job: true, pct: 20}, {state: I}}, 1, 1, 1, 1},
		{"busy then cancel (item 1)", []tick{pr(20), {state: B, job: true, pct: 20}, {state: I}}, 1, 1, 1, 0},
		{"runout cleared (items 2, 3)", []tick{pr(20), {state: A, job: true, pct: 20}, pr(21), pr(22)}, 0, 1, 1, 1},
		{"busy cleared (items 2, 3)", []tick{pr(20), {state: B, job: true, pct: 20}, pr(21)}, 0, 1, 1, 0},
		{"offline blip (item 4)", []tick{pr(20), {state: O}, pr(21)}, 0, 1, 1, 0},
		{"offline then done (item 4)", []tick{pr(20), {state: O}, {state: I}}, 1, 1, 1, 0},
		{"error blip (item 4)", []tick{{state: E}, {state: O}, {state: E}}, 0, 0, 0, 1},
		{"two prints", []tick{pr(20), {state: I}, pr(20), {state: I}}, 2, 2, 2, 0},
		// Power loss mid-print, new print started before an idle tick was seen.
		{"new file after outage", []tick{pr(60), {state: O}, {state: P, job: true, pct: 70, file: "b.gcode"}}, 0, 2, 4, 0},
		// Review findings: elapsed missing on a dialog tick is not a new print,
		// a new file first seen busy still sends Print started, and an error
		// after a long outage is a new error.
		{"dialog tick without elapsed", []tick{pr(60), {state: A, job: true, pct: 60, noElapsed: true}, pr(61)}, 0, 1, 2, 1},
		{"printing tick without elapsed", []tick{pr(60), {state: P, job: true, pct: 61, noElapsed: true}, pr(62)}, 0, 1, 2, 0},
		{"busy with job before start", []tick{{state: B, job: true}, pr(1)}, 0, 1, 0, 0},
		{"new file first seen busy", []tick{pr(60), {state: O}, {state: B, job: true, pct: 1, file: "b.gcode"}, {state: P, job: true, pct: 2, file: "b.gcode"}}, 0, 2, 2, 0},
		{"error after long outage", []tick{{state: E}, {state: O}, {state: E, gap: true}}, 0, 0, 0, 2},
		{"same file restarted after outage", []tick{pr(60), {state: O}, pr(1), pr(20)}, 0, 2, 3, 0},
		// Second review: a restart whose first tick has no elapsed time is
		// still caught, a print first seen paused is recorded, a new file
		// under a dialog drops the old print, and the job id wins over
		// elapsed time.
		{"restart first seen without elapsed", []tick{pr(60), {state: O}, {state: P, job: true, pct: 1, noElapsed: true}, pr(20)}, 0, 2, 3, 0},
		{"first seen paused then cancelled", []tick{{state: models.StatePaused, job: true, pct: 5}, {state: I}}, 1, 0, 0, 0},
		{"new file under dialog then cancelled", []tick{pr(60), {state: O}, {state: A, job: true, pct: 1, file: "b.gcode"}, {state: I}}, 0, 1, 2, 1},
		{"same job id, elapsed reset", []tick{{state: P, job: true, pct: 60, id: 7}, {state: O}, {state: P, job: true, pct: 61, id: 7, elapsed: 5}}, 0, 1, 2, 0},
		{"new job id, same file", []tick{{state: P, job: true, pct: 60, id: 7}, {state: O}, {state: P, job: true, pct: 70, id: 8}}, 0, 2, 4, 0},
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
	_, dur, _ := runTicks(t, tick{state: models.StatePrinting, job: true, pct: 20}, tick{state: models.StateAttention, job: true, pct: 30}, tick{state: models.StateIdle})
	if dur != 300 {
		t.Errorf("duration %d, want 300 (from the attention tick)", dur)
	}
}

// A dialog tick without elapsed time keeps the print's duration.
func TestHistoryKeepsElapsedOverDialog(t *testing.T) {
	_, dur, _ := runTicks(t, tick{state: models.StatePrinting, job: true, pct: 60}, tick{state: models.StateAttention, job: true, pct: 60, noElapsed: true}, tick{state: models.StateIdle})
	if dur != 600 {
		t.Errorf("duration %d, want 600", dur)
	}
}

// A print started after a restart from a busy state, with or without a
// job on the busy tick, still sends Print started.
func TestRestartThenStart(t *testing.T) {
	B, P := models.StateBusy, models.StatePrinting
	for name, ticks := range map[string][]tick{
		"busy without job":      {{state: B}, {state: P, job: true, pct: 1}},
		"busy with job":         {{state: B, job: true}, {state: P, job: true, pct: 1}},
		"attention without job": {{state: models.StateAttention}, {state: P, job: true, pct: 1}},
	} {
		_, _, sent := runFrom(t, ticks...)
		if count(sent, "Print started") != 1 {
			t.Errorf("%s: sent=%q; want 1 Print started", name, sent)
		}
	}
}

// Power lost mid-print and back within the gap: the print failed.
func TestPowerCutIsFailed(t *testing.T) {
	_, _, result, _ := runResult(t, tick{state: models.StateIdle},
		tick{state: models.StatePrinting, job: true, pct: 40, jobState: "PRINTING"},
		tick{state: models.StateOffline}, tick{state: models.StateIdle})
	if result != "failed" {
		t.Errorf("result %q, want failed", result)
	}
}

// A restart mid-print seeds the passed checkpoints: no start, no repeat
// checkpoint, and the print is still recorded when it ends.
func TestRestartMidPrint(t *testing.T) {
	P := models.StatePrinting
	rows, _, sent := runFrom(t, tick{state: P, job: true, pct: 60}, tick{state: P, job: true, pct: 61}, tick{state: models.StateIdle})
	if rows != 1 || len(sent) != 0 {
		t.Errorf("rows=%d sent=%q; want 1 row, no alerts", rows, sent)
	}
}

// A print first seen paused or under a dialog (restart, printer just
// added) is still the print in progress: cancelling records it, resuming
// sends no Print started and does not re-fire passed checkpoints. Same when
// the first poll after a restart fails or comes back without job data.
func TestRestartNotPrinting(t *testing.T) {
	P, Pa, O, I := models.StatePrinting, models.StatePaused, models.StateOffline, models.StateIdle
	cases := []struct {
		name  string
		ticks []tick
		rows  int
	}{
		{"paused then cancelled", []tick{{state: Pa, job: true, pct: 60}, {state: I}}, 1},
		{"paused then resumed", []tick{{state: Pa, job: true, pct: 60}, {state: P, job: true, pct: 61}}, 0},
		{"first poll offline", []tick{{state: O}, {state: P, job: true, pct: 60}, {state: P, job: true, pct: 61}, {state: I}}, 1},
		{"first poll without job", []tick{{state: P}, {state: P, job: true, pct: 60}, {state: I}}, 1},
	}
	for _, c := range cases {
		rows, _, sent := runFrom(t, c.ticks...)
		if rows != c.rows || len(sent) != 0 {
			t.Errorf("%s: rows=%d sent=%q; want %d rows, no alerts", c.name, rows, sent, c.rows)
		}
	}
}

// PrusaLink: runout (job PAUSED) then cancel, job gone on the idle tick.
func TestRunoutCancelIsCancelled(t *testing.T) {
	_, _, result, _ := runResult(t, tick{state: models.StateIdle},
		tick{state: models.StatePrinting, job: true, pct: 40, jobState: "PRINTING"},
		tick{state: models.StateAttention, job: true, pct: 40, jobState: "PAUSED"},
		tick{state: models.StateIdle})
	if result != "cancelled" {
		t.Errorf("result %q, want cancelled", result)
	}
}

// OctoPrint keeps showing the last job while idle: seeing that on the first
// poll must not mark the next print's checkpoints as already sent.
func TestFirstPollIdleWithOldJob(t *testing.T) {
	P, I := models.StatePrinting, models.StateIdle
	_, _, sent := runFrom(t, tick{state: I, job: true, pct: 100}, tick{state: P, job: true, pct: 5},
		tick{state: P, job: true, pct: 20}, tick{state: P, job: true, pct: 60})
	if count(sent, "Print checkpoint") != 2 || count(sent, "Print started") != 1 {
		t.Errorf("sent=%q; want 1 start, 2 checkpoints", sent)
	}
}
