package prusalink

import (
	"testing"

	"github.com/ccmpbll/printspy/models"
)

// Every documented PrusaLink state maps as before; an unknown one is busy,
// not idle, so it neither starts the auto-off clock nor ends a print.
func TestMapState(t *testing.T) {
	for in, want := range map[string]models.PrinterState{
		"PRINTING": models.StatePrinting, "PAUSED": models.StatePaused,
		"FINISHED": models.StateIdle, "STOPPED": models.StateIdle, "IDLE": models.StateIdle, "READY": models.StateIdle,
		"ATTENTION": models.StateAttention, "ERROR": models.StateError, "BUSY": models.StateBusy,
		"idle": models.StateIdle, "SOMETHING_NEW": models.StateBusy, "": models.StateBusy,
	} {
		if got := mapState(in); got != want {
			t.Errorf("mapState(%q) = %s, want %s", in, got, want)
		}
	}
}
