package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ccmpbll/printspy/db"
	"github.com/ccmpbll/printspy/models"
	"github.com/ccmpbll/printspy/plugin"
	"github.com/ccmpbll/printspy/poller"
)

type Handler struct {
	db     *db.DB
	poller *poller.Poller
	ctx    context.Context
	proxy  *http.Client

	errLogMu   sync.Mutex
	errLogLast map[string]time.Time
}

func New(ctx context.Context, database *db.DB, p *poller.Poller) *Handler {
	return &Handler{
		db:         database,
		poller:     p,
		ctx:        ctx,
		proxy:      &http.Client{Timeout: 30 * time.Second},
		errLogLast: make(map[string]time.Time),
	}
}

func (h *Handler) logOnce(key string, interval time.Duration, format string, args ...any) {
	h.errLogMu.Lock()
	defer h.errLogMu.Unlock()
	now := time.Now()
	if last, ok := h.errLogLast[key]; ok && now.Sub(last) < interval {
		return
	}
	if len(h.errLogLast) > 100 {
		for k, t := range h.errLogLast {
			if now.Sub(t) > interval {
				delete(h.errLogLast, k)
			}
		}
	}
	h.errLogLast[key] = now
	log.Printf(format, args...)
}

func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/printers", h.handlePrinters)
	mux.HandleFunc("/api/printers/", h.handlePrinterByID)
	mux.HandleFunc("/api/printers/reorder", h.handleReorder)
	mux.HandleFunc("/api/test", h.handleTestConnection)
	mux.HandleFunc("/api/events", h.handleSSE)
	mux.HandleFunc("/api/settings", h.handleSettings)
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

type printerRequest struct {
	Name         string `json:"name"`
	Type         string `json:"type"`
	URL          string `json:"url"`
	APIKey       string `json:"api_key"`
	Enabled      bool   `json:"enabled"`
	PollInterval int    `json:"poll_interval"`
}

func (h *Handler) addPrinter(w http.ResponseWriter, r *http.Request) {
	var req printerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}

	p := models.PrinterConfig{
		Name:         req.Name,
		Type:         req.Type,
		URL:          req.URL,
		APIKey:       req.APIKey,
		PollInterval: req.PollInterval,
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
	h.poller.BroadcastRefresh()
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
	case http.MethodGet:
		h.getPrinter(w, r, id)
	case http.MethodPut:
		h.updatePrinter(w, r, id)
	case http.MethodDelete:
		h.deletePrinter(w, r, id)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *Handler) getPrinter(w http.ResponseWriter, r *http.Request, id int64) {
	printer, err := h.db.GetPrinter(id)
	if err != nil {
		jsonError(w, "printer not found", http.StatusNotFound)
		return
	}
	jsonResponse(w, struct {
		models.PrinterConfig
		APIKey string `json:"api_key"`
	}{*printer, printer.APIKey})
}

func (h *Handler) updatePrinter(w http.ResponseWriter, r *http.Request, id int64) {
	var req printerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	p := models.PrinterConfig{
		ID:           id,
		Name:         req.Name,
		Type:         req.Type,
		URL:          req.URL,
		APIKey:       req.APIKey,
		Enabled:      req.Enabled,
		PollInterval: req.PollInterval,
	}

	if err := h.db.UpdatePrinter(&p); err != nil {
		jsonError(w, "failed to update printer", http.StatusInternalServerError)
		return
	}

	h.poller.RemovePrinter(id)
	if p.Enabled {
		h.poller.AddPrinter(h.ctx, p)
	}
	h.poller.BroadcastRefresh()
	jsonResponse(w, p)
}

func (h *Handler) deletePrinter(w http.ResponseWriter, r *http.Request, id int64) {
	h.poller.RemovePrinter(id)
	if err := h.db.DeletePrinter(id); err != nil {
		jsonError(w, "failed to delete printer", http.StatusInternalServerError)
		return
	}
	h.poller.BroadcastRefresh()
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) handleTestConnection(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Type   string `json:"type"`
		URL    string `json:"url"`
		APIKey string `json:"api_key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Type == "" {
		req.Type = "octoprint"
	}

	cfg := models.PrinterConfig{Type: req.Type, URL: req.URL, APIKey: req.APIKey}
	pl, err := plugin.Create(cfg)
	if err != nil {
		jsonError(w, "unsupported printer type", http.StatusBadRequest)
		return
	}

	if err := pl.Connect(r.Context()); err != nil {
		jsonResponse(w, map[string]any{"success": false, "error": err.Error()})
		return
	}

	name := pl.GetPrinterName(r.Context())
	jsonResponse(w, map[string]any{"success": true, "name": name})
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

func (h *Handler) handleReorder(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		IDs []int64 `json:"ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if err := h.db.ReorderPrinters(req.IDs); err != nil {
		jsonError(w, "failed to reorder printers", http.StatusInternalServerError)
		return
	}

	h.poller.BroadcastRefresh()
	w.WriteHeader(http.StatusNoContent)
}

// SSE

func (h *Handler) handleSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	sub := h.poller.Subscribe(r.Context())
	defer h.poller.Unsubscribe(sub)

	// Send initial full state
	printers, err := h.db.ListPrinters()
	if err == nil {
		statuses := h.poller.GetAllStatuses()
		result := make([]models.PrinterWithStatus, len(printers))
		for i, p := range printers {
			result[i] = models.PrinterWithStatus{Config: p, Status: statuses[p.ID]}
		}
		if data, err := json.Marshal(result); err == nil {
			fmt.Fprintf(w, "event: init\ndata: %s\n\n", data)
			flusher.Flush()
		}
	}

	for {
		select {
		case <-r.Context().Done():
			return
		case <-sub.Done():
			return
		case data := <-sub.Chan():
			if strings.Contains(string(data), `"refresh"`) {
				fmt.Fprintf(w, "event: refresh\ndata: %s\n\n", data)
			} else {
				fmt.Fprintf(w, "event: status\ndata: %s\n\n", data)
			}
			flusher.Flush()
		}
	}
}

// Settings

func (h *Handler) handleSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		settings, err := h.db.GetAllSettings()
		if err != nil {
			jsonError(w, "failed to get settings", http.StatusInternalServerError)
			return
		}
		if settings == nil {
			settings = make(map[string]string)
		}
		jsonResponse(w, settings)
	case http.MethodPut:
		var settings map[string]string
		if err := json.NewDecoder(r.Body).Decode(&settings); err != nil {
			jsonError(w, "invalid request body", http.StatusBadRequest)
			return
		}
		for k, v := range settings {
			validated, err := validateSetting(k, v)
			if err != nil {
				jsonError(w, err.Error(), http.StatusBadRequest)
				return
			}
			if err := h.db.SetSetting(k, validated); err != nil {
				jsonError(w, "failed to save settings", http.StatusInternalServerError)
				return
			}
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// Webcam/Thumbnail proxies

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
	streamClient := &http.Client{
		Transport: &http.Transport{
			ResponseHeaderTimeout: 10 * time.Second,
		},
	}
	resp, err := streamClient.Do(req)
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

	resp, err := h.proxy.Do(req)
	if err != nil {
		h.logOnce(fmt.Sprintf("snapshot-err-%d", id), 30*time.Second, "[snapshot:%d] failed to fetch from %s: %v", id, snapshotURL, err)
		http.Error(w, "failed to fetch snapshot", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		h.logOnce(fmt.Sprintf("snapshot-status-%d", id), 30*time.Second, "[snapshot:%d] unexpected status %d from %s", id, resp.StatusCode, snapshotURL)
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

	thumbURL := h.poller.GetThumbnailURL(id)
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

	resp, err := h.proxy.Do(req)
	if err != nil {
		http.Error(w, "failed to fetch thumbnail", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
	w.Header().Set("Cache-Control", "no-cache")
	io.Copy(w, resp.Body)
}

func validateSetting(key, value string) (string, error) {
	switch key {
	case "snapshot_interval":
		n, err := strconv.Atoi(value)
		if err != nil {
			return "", fmt.Errorf("snapshot_interval must be a number")
		}
		if n < 3 {
			n = 3
		} else if n > 300 {
			n = 300
		}
		return strconv.Itoa(n), nil
	default:
		return "", fmt.Errorf("unknown setting: %s", key)
	}
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
