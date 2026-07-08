// Package ingest implements a minimal PrusaLink-compatible HTTP surface so
// PrusaSlicer/OrcaSlicer's "Send to printer" (Physical Printer, PrusaLink
// mode) can target PrintSpy directly. A slicer's connection test hits
// GET /ingest/{targetID}/api/version, and "Send to printer" hits
// PUT /ingest/{targetID}/api/v1/files/{storage}/{path}. Uploaded files are
// staged as an IngestJob for a human to dispatch to a specific printer from
// the dashboard — see api.dispatchIngestJob.
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
	// dispatch triggers the same claim+power-on+relay path a manual
	// dashboard dispatch uses. Set via SetDispatchFunc once api.Handler
	// exists - main.go wires the two together, since ingest has no direct
	// dependency on the poller-backed dispatch logic otherwise.
	dispatch func(jobID, printerID int64)
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

func (h *Handler) SetBroadcastFunc(f func()) {
	h.broadcast = f
}

func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/ingest/", h.route)
}

func (h *Handler) route(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/ingest/")
	parts := strings.SplitN(path, "/", 2)

	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	target, err := h.db.GetIngestTarget(id)
	if err != nil {
		http.NotFound(w, r)
		return
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

	if target.AutoDispatchOnPrintNow && printAfter {
		h.maybeAutoDispatch(jobID, target)
	}

	w.WriteHeader(http.StatusCreated)
}

// maybeAutoDispatch fires the dispatch callback for a PrinterID-pinned
// target unconditionally (no ambiguity to resolve), or for a Model-bucket
// target only when exactly one enabled, non-maintenance printer currently
// matches - with 2+ matches there's no way to auto-pick which physical
// printer to wake, so the job is left staged for a human to resolve via the
// normal dashboard banner.
func (h *Handler) maybeAutoDispatch(jobID int64, target *models.IngestTarget) {
	if h.dispatch == nil {
		return
	}
	if target.PrinterID != nil {
		h.dispatch(jobID, *target.PrinterID)
		return
	}

	printers, err := h.db.ListPrinters()
	if err != nil {
		return
	}
	var match *models.PrinterConfig
	for i, p := range printers {
		if p.Model != target.Model || !p.Enabled || p.Maintenance {
			continue
		}
		if match != nil {
			return // ambiguous - 2+ printers share this model
		}
		match = &printers[i]
	}
	if match == nil {
		return
	}
	h.dispatch(jobID, match.ID)
}
