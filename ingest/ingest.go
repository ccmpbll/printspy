// Package ingest implements a minimal PrusaLink-compatible HTTP surface so
// PrusaSlicer/OrcaSlicer's "Send to printer" (Physical Printer, PrusaLink
// mode) can target PrintSpy directly, relaying onto a target's pinned
// printer automatically - no manual step, matching how OctoPrint's own
// upload works. A slicer's connection test hits GET /ingest/{targetID}/api/version,
// and "Send to printer" hits PUT /ingest/{targetID}/api/v1/files/{storage}/{path}.
package ingest

import (
	"crypto/subtle"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ccmpbll/printspy/db"
	"github.com/ccmpbll/printspy/models"
)

const maxUploadBytes = 200 << 20

type Handler struct {
	db      *db.DB
	dataDir string
	// dispatch is Print-After-Upload's path: power on the pinned printer if
	// needed, wait for it online, relay, and print. Set via SetDispatchFunc
	// once api.Handler exists - main.go wires the two together.
	dispatch func(jobID, printerID int64)
	// relay is plain Upload's path: if the pinned printer is already online,
	// send the file over with no print command and no proactive power-on.
	// If it's not online, this is a no-op - poller.checkIngestOnline picks
	// the job up automatically once the printer's next seen online.
	relay func(jobID, printerID int64)
	// broadcast pings connected dashboards to reload ingest jobs. Without
	// this, a job staged by a slicer upload sits invisible until something
	// else happens to trigger a refresh (a poll-driven state change, or a
	// manual page reload) - the dashboard has no other signal that a new
	// job exists.
	broadcast func()
}

func New(database *db.DB, dataDir string) *Handler {
	return &Handler{db: database, dataDir: dataDir}
}

func (h *Handler) SetDispatchFunc(f func(jobID, printerID int64)) {
	h.dispatch = f
}

func (h *Handler) SetRelayFunc(f func(jobID, printerID int64)) {
	h.relay = f
}

func (h *Handler) SetBroadcastFunc(f func()) {
	h.broadcast = f
}

func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/ingest/", h.route)
}

func (h *Handler) route(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/ingest/")
	parts := strings.SplitN(path, "/", 2)

	var target *models.IngestTarget
	if id, err := strconv.ParseInt(parts[0], 10, 64); err == nil {
		target, err = h.db.GetIngestTarget(id)
		if err != nil {
			http.NotFound(w, r)
			return
		}
	} else {
		target, err = h.db.GetIngestTargetByLabel(parts[0])
		if err != nil {
			http.NotFound(w, r)
			return
		}
	}
	if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Api-Key")), []byte(target.APIKey)) != 1 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	rest := ""
	if len(parts) == 2 {
		rest = parts[1]
	}

	switch {
	case rest == "api/version":
		h.version(w, r)
	case strings.HasPrefix(rest, "api/v1/files/"):
		h.upload(w, r, target, strings.TrimPrefix(rest, "api/v1/files/"))
	default:
		http.NotFound(w, r)
	}
}

func (h *Handler) version(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"api":      "2.0.0",
		"server":   "1.0.0",
		"text":     "PrusaLink",
		"hostname": "printspy",
		"capabilities": map[string]bool{
			"upload-by-put": true,
		},
	})
}

// upload handles PUT api/v1/files/{storage}/{path...}. storage is accepted
// but ignored - the real relay to the target printer always uses "usb"
// (plugin/prusalink's UploadFile already enforces this; PrusaLink's internal
// flash storage is read-only over the network API regardless of auth).
func (h *Handler) upload(w http.ResponseWriter, r *http.Request, target *models.IngestTarget, storagePath string) {
	if r.Method != http.MethodPut {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	segments := strings.SplitN(storagePath, "/", 2)
	if len(segments) != 2 || segments[1] == "" {
		http.Error(w, "missing file path", http.StatusBadRequest)
		return
	}
	filename := filepath.Base(segments[1])

	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes)
	data, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "file too large or unreadable", http.StatusBadRequest)
		return
	}

	printAfter := r.Header.Get("Print-After-Upload") == "?1"

	jobID, err := h.db.CreateIngestJob(target.ID, filename, printAfter, int64(len(data)))
	if err != nil {
		http.Error(w, "failed to stage job", http.StatusInternalServerError)
		return
	}

	jobDir := filepath.Join(h.dataDir, "ingest", strconv.FormatInt(jobID, 10))
	if err := os.MkdirAll(jobDir, 0755); err != nil {
		h.db.DeleteIngestJob(jobID)
		http.Error(w, "failed to stage job", http.StatusInternalServerError)
		return
	}
	filePath := filepath.Join(jobDir, filename)
	if err := os.WriteFile(filePath, data, 0644); err != nil {
		h.db.DeleteIngestJob(jobID)
		os.RemoveAll(jobDir)
		http.Error(w, "failed to stage job", http.StatusInternalServerError)
		return
	}

	if err := h.db.SetIngestJobFilePath(jobID, filePath); err != nil {
		http.Error(w, "failed to stage job", http.StatusInternalServerError)
		return
	}

	if h.broadcast != nil {
		h.broadcast()
	}

	if target.PrinterID != nil {
		if printAfter && h.dispatch != nil {
			h.dispatch(jobID, *target.PrinterID)
		} else if !printAfter && h.relay != nil {
			h.relay(jobID, *target.PrinterID)
		}
	}

	w.WriteHeader(http.StatusCreated)
}
