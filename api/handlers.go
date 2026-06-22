package api

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/ccmpbll/printspy/db"
	"github.com/ccmpbll/printspy/models"
	"github.com/ccmpbll/printspy/plugin"
	"github.com/ccmpbll/printspy/poller"
)

type Handler struct {
	db     *db.DB
	poller *poller.Poller
	ctx    context.Context
}

func New(ctx context.Context, database *db.DB, p *poller.Poller) *Handler {
	return &Handler{db: database, poller: p, ctx: ctx}
}

func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/printers", h.handlePrinters)
	mux.HandleFunc("/api/printers/", h.handlePrinterByID)
	mux.HandleFunc("/api/webcam/", h.handleWebcamProxy)
	mux.HandleFunc("/api/snapshot/", h.handleSnapshotProxy)
	mux.HandleFunc("/api/thumbnail/", h.handleThumbnailProxy)
}

func (h *Handler) handlePrinters(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.listPrinters(w, r)
	case http.MethodPost:
		h.addPrinter(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *Handler) listPrinters(w http.ResponseWriter, r *http.Request) {
	printers, err := h.db.ListPrinters()
	if err != nil {
		jsonError(w, "failed to list printers", http.StatusInternalServerError)
		return
	}

	statuses := h.poller.GetAllStatuses()
	result := make([]models.PrinterWithStatus, len(printers))
	for i, p := range printers {
		result[i] = models.PrinterWithStatus{
			Config: p,
			Status: statuses[p.ID],
		}
	}
	jsonResponse(w, result)
}

func (h *Handler) addPrinter(w http.ResponseWriter, r *http.Request) {
	var p models.PrinterConfig
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if p.Name == "" || p.URL == "" || p.APIKey == "" {
		jsonError(w, "name, url, and api_key are required", http.StatusBadRequest)
		return
	}
	if p.Type == "" {
		p.Type = "octoprint"
	}
	if p.PollInterval <= 0 {
		p.PollInterval = 10
	}
	p.Enabled = true

	if err := h.db.CreatePrinter(&p); err != nil {
		jsonError(w, "failed to create printer", http.StatusInternalServerError)
		return
	}

	h.poller.AddPrinter(h.ctx, p)
	w.WriteHeader(http.StatusCreated)
	jsonResponse(w, p)
}

func (h *Handler) handlePrinterByID(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/printers/")
	parts := strings.SplitN(path, "/", 2)

	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		jsonError(w, "invalid printer id", http.StatusBadRequest)
		return
	}

	if len(parts) == 2 {
		switch parts[1] {
		case "test":
			h.testPrinter(w, r, id)
		case "history":
			h.getPrintHistory(w, r, id)
		default:
			http.NotFound(w, r)
		}
		return
	}

	switch r.Method {
	case http.MethodPut:
		h.updatePrinter(w, r, id)
	case http.MethodDelete:
		h.deletePrinter(w, r, id)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *Handler) updatePrinter(w http.ResponseWriter, r *http.Request, id int64) {
	var p models.PrinterConfig
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	p.ID = id

	if err := h.db.UpdatePrinter(&p); err != nil {
		jsonError(w, "failed to update printer", http.StatusInternalServerError)
		return
	}

	h.poller.RemovePrinter(id)
	if p.Enabled {
		h.poller.AddPrinter(h.ctx, p)
	}
	jsonResponse(w, p)
}

func (h *Handler) deletePrinter(w http.ResponseWriter, r *http.Request, id int64) {
	h.poller.RemovePrinter(id)
	if err := h.db.DeletePrinter(id); err != nil {
		jsonError(w, "failed to delete printer", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) testPrinter(w http.ResponseWriter, r *http.Request, id int64) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	printer, err := h.db.GetPrinter(id)
	if err != nil {
		jsonError(w, "printer not found", http.StatusNotFound)
		return
	}

	pl, err := plugin.Create(*printer)
	if err != nil {
		jsonError(w, "unsupported printer type", http.StatusBadRequest)
		return
	}

	if err := pl.Connect(r.Context()); err != nil {
		jsonResponse(w, map[string]any{"success": false, "error": err.Error()})
		return
	}
	jsonResponse(w, map[string]any{"success": true})
}

func (h *Handler) getPrintHistory(w http.ResponseWriter, r *http.Request, id int64) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	history, err := h.db.GetPrintHistory(id, 50)
	if err != nil {
		jsonError(w, "failed to get history", http.StatusInternalServerError)
		return
	}
	if history == nil {
		history = []models.PrintHistory{}
	}
	jsonResponse(w, history)
}

func (h *Handler) handleWebcamProxy(w http.ResponseWriter, r *http.Request) {
	idStr := strings.TrimPrefix(r.URL.Path, "/api/webcam/")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid printer id", http.StatusBadRequest)
		return
	}

	webcamURL := h.poller.GetWebcamURL(id)
	if webcamURL == "" {
		http.Error(w, "no webcam configured", http.StatusNotFound)
		return
	}

	printer, err := h.db.GetPrinter(id)
	if err != nil {
		http.Error(w, "printer not found", http.StatusNotFound)
		return
	}

	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, webcamURL, nil)
	if err != nil {
		http.Error(w, "failed to create request", http.StatusInternalServerError)
		return
	}
	req.Header.Set("X-Api-Key", printer.APIKey)

	log.Printf("[webcam:%d] proxying stream from %s", id, webcamURL)
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("[webcam:%d] connection failed: %v", id, err)
		http.Error(w, "failed to connect to webcam", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	log.Printf("[webcam:%d] connected, status=%d content-type=%s", id, resp.StatusCode, resp.Header.Get("Content-Type"))

	for _, header := range []string{"Content-Type", "Cache-Control", "Connection"} {
		if v := resp.Header.Get(header); v != "" {
			w.Header().Set(header, v)
		}
	}
	w.WriteHeader(resp.StatusCode)

	flusher, canFlush := w.(http.Flusher)
	buf := make([]byte, 32*1024)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			w.Write(buf[:n])
			if canFlush {
				flusher.Flush()
			}
		}
		if readErr != nil {
			break
		}
	}
}

func (h *Handler) handleSnapshotProxy(w http.ResponseWriter, r *http.Request) {
	idStr := strings.TrimPrefix(r.URL.Path, "/api/snapshot/")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid printer id", http.StatusBadRequest)
		return
	}

	snapshotURL := h.poller.GetSnapshotURL(id)
	if snapshotURL == "" {
		http.Error(w, "no webcam configured", http.StatusNotFound)
		return
	}

	printer, err := h.db.GetPrinter(id)
	if err != nil {
		http.Error(w, "printer not found", http.StatusNotFound)
		return
	}

	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, snapshotURL, nil)
	if err != nil {
		http.Error(w, "failed to create request", http.StatusInternalServerError)
		return
	}
	req.Header.Set("X-Api-Key", printer.APIKey)

	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		log.Printf("[snapshot:%d] failed to fetch from %s: %v", id, snapshotURL, err)
		http.Error(w, "failed to fetch snapshot", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Printf("[snapshot:%d] unexpected status %d from %s", id, resp.StatusCode, snapshotURL)
	}

	w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
	w.Header().Set("Cache-Control", "no-cache, no-store")
	io.Copy(w, resp.Body)
}

func (h *Handler) handleThumbnailProxy(w http.ResponseWriter, r *http.Request) {
	idStr := strings.TrimPrefix(r.URL.Path, "/api/thumbnail/")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid printer id", http.StatusBadRequest)
		return
	}

	thumbURL := h.poller.GetThumbnailURL(r.Context(), id)
	if thumbURL == "" {
		http.Error(w, "no thumbnail available", http.StatusNotFound)
		return
	}

	printer, err := h.db.GetPrinter(id)
	if err != nil {
		http.Error(w, "printer not found", http.StatusNotFound)
		return
	}

	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, thumbURL, nil)
	if err != nil {
		http.Error(w, "failed to create request", http.StatusInternalServerError)
		return
	}
	req.Header.Set("X-Api-Key", printer.APIKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		http.Error(w, "failed to fetch thumbnail", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
	w.Header().Set("Cache-Control", "no-cache")
	io.Copy(w, resp.Body)
}

func jsonResponse(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(data); err != nil {
		log.Printf("json encode error: %v", err)
	}
}

func jsonError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
