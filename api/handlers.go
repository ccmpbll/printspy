package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ccmpbll/printspy/db"
	"github.com/ccmpbll/printspy/models"
	"github.com/ccmpbll/printspy/netguard"
	"github.com/ccmpbll/printspy/notify"
	"github.com/ccmpbll/printspy/plugin"
	"github.com/ccmpbll/printspy/poller"
	"gopkg.in/yaml.v3"
)

type Handler struct {
	db     *db.DB
	poller *poller.Poller
	ctx    context.Context
	proxy  *http.Client

	errLogMu   sync.Mutex
	errLogLast map[string]time.Time

	loginMu    sync.Mutex
	loginFails map[string][]time.Time

	setupMu sync.Mutex
}

func New(ctx context.Context, database *db.DB, p *poller.Poller) *Handler {
	return &Handler{
		db:         database,
		poller:     p,
		ctx:        ctx,
		proxy:      &http.Client{Timeout: 30 * time.Second, Transport: netguard.Transport()},
		errLogLast: make(map[string]time.Time),
		loginFails: make(map[string][]time.Time),
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
	mux.HandleFunc("/api/printers/power", h.handleBulkPower)
	mux.HandleFunc("/api/test", h.handleTestConnection)
	mux.HandleFunc("/api/events", h.handleSSE)
	mux.HandleFunc("/api/settings", h.handleSettings)
	mux.HandleFunc("/api/notify-test", h.handleNotifyTest)
	mux.HandleFunc("/api/config/export", h.handleConfigExport)
	mux.HandleFunc("/api/config/import", h.handleConfigImport)
	mux.HandleFunc("/api/webcam/", h.handleWebcamProxy)
	mux.HandleFunc("/api/snapshot/", h.handleSnapshotProxy)
	mux.HandleFunc("/api/thumbnail/", h.handleThumbnailProxy)
	mux.HandleFunc("/api/file-thumbnail/", h.handleFileThumbnailProxy)
	mux.HandleFunc("/setup", h.handleSetup)
	mux.HandleFunc("/login", h.handleLogin)
	mux.HandleFunc("/logout", h.handleLogout)
	mux.HandleFunc("/api/users", h.handleUsers)
	mux.HandleFunc("/api/users/", h.handleUserByID)
	mux.HandleFunc("/api/account/password", h.handleChangePassword)
	mux.HandleFunc("/api/smartplugs", h.handleSmartPlugs)
	mux.HandleFunc("/api/smartplugs/", h.handleSmartPlugByID)
	mux.HandleFunc("/api/cameras", h.handleCameras)
	mux.HandleFunc("/api/cameras/", h.handleCameraByID)
	mux.HandleFunc("/api/ingest-keys", h.handleIngestKeys)
	mux.HandleFunc("/api/ingest-keys/", h.handleIngestKeyByID)
	mux.HandleFunc("/api/ingest-jobs", h.handleIngestJobs)
	mux.HandleFunc("/api/ingest-jobs/", h.handleIngestJobByID)
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
		result[i] = h.buildPrinterWithStatus(p, statuses)
	}
	jsonResponse(w, result)
}

func (h *Handler) buildPrinterWithStatus(p models.PrinterConfig, statuses map[int64]*models.PrinterStatus) models.PrinterWithStatus {
	return models.PrinterWithStatus{
		Config:      p,
		Status:      statuses[p.ID],
		HasCamera:   h.hasCameraAssigned(p.ID),
		DisplayName: h.poller.GetDisplayName(p.ID),
		HasWebcam:   h.poller.GetWebcamURL(p.ID) != "",
	}
}

type printerRequest struct {
	Name               string  `json:"name"`
	Type               string  `json:"type"`
	Model              string  `json:"model"`
	HideModel          bool    `json:"hide_model"`
	URL                string  `json:"url"`
	APIKey             string  `json:"api_key"`
	Username           string  `json:"username"`
	Enabled            bool    `json:"enabled"`
	PollInterval       int     `json:"poll_interval"`
	IdleTimeoutMinutes int     `json:"idle_timeout_minutes"`
	MaxBedTemp         float64 `json:"max_bed_temp"`
	MaxExtruderTemp    float64 `json:"max_extruder_temp"`
}

func (h *Handler) addPrinter(w http.ResponseWriter, r *http.Request) {
	var req printerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}

	p := models.PrinterConfig{
		Name:               req.Name,
		Type:               req.Type,
		Model:              req.Model,
		HideModel:          req.HideModel,
		URL:                req.URL,
		APIKey:             req.APIKey,
		Username:           req.Username,
		PollInterval:       req.PollInterval,
		IdleTimeoutMinutes: req.IdleTimeoutMinutes,
		MaxBedTemp:         req.MaxBedTemp,
		MaxExtruderTemp:    req.MaxExtruderTemp,
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
		case "history/list":
			h.getPrintHistoryList(w, r, id)
		case "power":
			h.handlePower(w, r, id)
		case "recent":
			h.getRecentPrints(w, r, id)
		case "print":
			h.startPrint(w, r, id)
		case "upload":
			h.uploadFile(w, r, id)
		case "control":
			h.controlPrint(w, r, id)
		case "maintenance":
			h.handleMaintenance(w, r, id)
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
		APIKey   string `json:"api_key"`
		Username string `json:"username"`
	}{*printer, printer.APIKey, printer.Username})
}

func (h *Handler) updatePrinter(w http.ResponseWriter, r *http.Request, id int64) {
	var req printerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	p := models.PrinterConfig{
		ID:                 id,
		Name:               req.Name,
		Type:               req.Type,
		Model:              req.Model,
		HideModel:          req.HideModel,
		URL:                req.URL,
		APIKey:             req.APIKey,
		Username:           req.Username,
		Enabled:            req.Enabled,
		PollInterval:       req.PollInterval,
		IdleTimeoutMinutes: req.IdleTimeoutMinutes,
		MaxBedTemp:         req.MaxBedTemp,
		MaxExtruderTemp:    req.MaxExtruderTemp,
	}

	if err := h.db.UpdatePrinter(&p); err != nil {
		jsonError(w, "failed to update printer", http.StatusInternalServerError)
		return
	}

	// UpdatePrinter doesn't touch the maintenance column - re-fetch it so
	// saving unrelated edits (name, URL, ...) doesn't silently resume
	// polling a printer that's deliberately paused.
	current, _ := h.db.GetPrinter(id)
	h.poller.RemovePrinter(id)
	if p.Enabled && (current == nil || !current.Maintenance) {
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
		Type     string `json:"type"`
		URL      string `json:"url"`
		APIKey   string `json:"api_key"`
		Username string `json:"username"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Type == "" {
		req.Type = "octoprint"
	}

	cfg := models.PrinterConfig{Type: req.Type, URL: req.URL, APIKey: req.APIKey, Username: req.Username}
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
	summary, err := h.db.GetPrintHistorySummary(id)
	if err != nil {
		jsonError(w, "failed to get history", http.StatusInternalServerError)
		return
	}
	jsonResponse(w, summary)
}

func (h *Handler) getPrintHistoryList(w http.ResponseWriter, r *http.Request, id int64) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	limit := 20
	if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v > 0 {
		limit = v
	}
	offset := 0
	if v, err := strconv.Atoi(r.URL.Query().Get("offset")); err == nil && v >= 0 {
		offset = v
	}

	entries, hasMore, err := h.db.ListPrintHistory(id, limit, offset)
	if err != nil {
		jsonError(w, "failed to get history", http.StatusInternalServerError)
		return
	}
	if entries == nil {
		entries = []models.PrintHistory{}
	}
	jsonResponse(w, map[string]any{"entries": entries, "has_more": hasMore})
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

	log.Printf("[sse] client connected: %s", r.RemoteAddr)
	defer log.Printf("[sse] client disconnected: %s", r.RemoteAddr)

	// Send initial full state
	printers, err := h.db.ListPrinters()
	if err == nil {
		statuses := h.poller.GetAllStatuses()
		result := make([]models.PrinterWithStatus, len(printers))
		for i, p := range printers {
			result[i] = h.buildPrinterWithStatus(p, statuses)
		}
		if data, err := json.Marshal(result); err == nil {
			fmt.Fprintf(w, "event: init\ndata: %s\n\n", data)
			flusher.Flush()
		}
	}

	// ponytail: keeps proxies (Traefik, nginx, etc.) from reaping the connection as idle
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-sub.Done():
			return
		case <-heartbeat.C:
			if _, err := fmt.Fprint(w, ": heartbeat\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case msg := <-sub.Chan():
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", msg.Event, msg.Data)
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
		if _, ok := settings["auto_off_idle_minutes"]; ok {
			h.poller.ResetAllIdleClocks()
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleNotifyTest sends a text-only Pushover notification using whatever
// credentials are currently saved, so the user can confirm delivery without
// waiting for a real print event. Credentials are read from settings, never
// accepted in the request body - this only ever tests what's already saved.
func (h *Handler) handleNotifyTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	token, _ := h.db.GetSetting("pushover_app_token")
	userKey, _ := h.db.GetSetting("pushover_user_key")
	if token == "" || userKey == "" {
		jsonResponse(w, map[string]any{"success": false, "error": "Pushover user key and app token must be saved first"})
		return
	}

	msg := notify.Message{Title: "PrintSpy test", Text: "This is a test notification from PrintSpy."}
	if err := notify.Send(token, userKey, msg); err != nil {
		jsonResponse(w, map[string]any{"success": false, "error": err.Error()})
		return
	}
	jsonResponse(w, map[string]any{"success": true})
}

// Power control

func (h *Handler) handlePower(w http.ResponseWriter, r *http.Request, id int64) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Action string `json:"action"`  // "on" or "off"
		PlugID string `json:"plug_id"` // which plug to control
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.PlugID == "" {
		jsonError(w, "plug_id is required", http.StatusBadRequest)
		return
	}

	turnOn := req.Action == "on"

	if !turnOn && req.Action != "off" {
		jsonError(w, "action must be on or off", http.StatusBadRequest)
		return
	}

	if !turnOn {
		status := h.poller.GetStatus(id)
		if status != nil && (status.State == models.StatePrinting || status.State == models.StatePaused) {
			singlePlug := len(status.Power) <= 1
			isPrinterPlug := singlePlug
			if !singlePlug {
				for _, ps := range status.Power {
					if ps.ID == req.PlugID && strings.Contains(strings.ToLower(ps.Label), "printer") {
						isPrinterPlug = true
						break
					}
				}
			}
			if isPrinterPlug {
				jsonError(w, "cannot turn off printer while printing", http.StatusConflict)
				return
			}
		}
	}

	if err := h.poller.SetPowerState(r.Context(), id, req.PlugID, turnOn); err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	jsonResponse(w, map[string]any{"success": true, "power_on": turnOn})
}

// handleMaintenance toggles a printer out of/into the poll loop on explicit
// user intent - unlike offline/error/attention, which are inferred from
// connectivity, this is "I know it's down, stop polling and stop alarming."
func (h *Handler) handleMaintenance(w http.ResponseWriter, r *http.Request, id int64) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Maintenance bool `json:"maintenance"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if err := h.db.SetMaintenance(id, req.Maintenance); err != nil {
		jsonError(w, "failed to update maintenance state", http.StatusInternalServerError)
		return
	}

	if req.Maintenance {
		h.poller.RemovePrinter(id)
	} else if printer, err := h.db.GetPrinter(id); err == nil && printer.Enabled {
		h.poller.AddPrinter(h.ctx, *printer)
	}

	h.poller.BroadcastRefresh()
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) handleBulkPower(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Action string `json:"action"` // "on" or "off"
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Action != "on" && req.Action != "off" {
		jsonError(w, "action must be on or off", http.StatusBadRequest)
		return
	}

	turnOn := req.Action == "on"

	printers, err := h.db.ListPrinters()
	if err != nil {
		jsonError(w, "failed to list printers", http.StatusInternalServerError)
		return
	}

	results := make([]map[string]any, 0)
	for _, p := range printers {
		if !p.Enabled {
			continue
		}
		status := h.poller.GetStatus(p.ID)
		if status == nil || len(status.Power) == 0 {
			continue
		}
		if !turnOn && (status.State == models.StatePrinting || status.State == models.StatePaused) {
			results = append(results, map[string]any{"id": p.ID, "name": p.Name, "success": false, "error": "printing"})
			continue
		}
		for _, ps := range status.Power {
			err := h.poller.SetPowerState(r.Context(), p.ID, ps.ID, turnOn)
			if err != nil {
				results = append(results, map[string]any{"id": p.ID, "name": p.Name, "plug": ps.Label, "success": false, "error": err.Error()})
			} else {
				results = append(results, map[string]any{"id": p.ID, "name": p.Name, "plug": ps.Label, "success": true})
			}
		}
	}

	jsonResponse(w, map[string]any{"results": results})
}

// Smart plugs — managed independently of printers, optionally assigned to one.

func (h *Handler) handleSmartPlugs(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		plugs, err := h.db.ListAllSmartPlugs()
		if err != nil {
			jsonError(w, "failed to list smart plugs", http.StatusInternalServerError)
			return
		}
		jsonResponse(w, plugs)

	case http.MethodPost:
		var req smartPlugRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			jsonError(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if req.IP == "" {
			jsonError(w, "ip is required", http.StatusBadRequest)
			return
		}
		id, err := h.db.CreateSmartPlug(req.IP, req.Idx, req.Label, req.HideLabel, req.PrinterID)
		if err != nil {
			jsonError(w, "failed to create smart plug", http.StatusInternalServerError)
			return
		}
		if req.PrinterID != nil {
			go h.poller.Repoll(h.ctx, *req.PrinterID)
		}
		jsonResponse(w, map[string]int64{"id": id})

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

type smartPlugRequest struct {
	IP        string `json:"ip"`
	Idx       string `json:"idx"`
	Label     string `json:"label"`
	HideLabel bool   `json:"hide_label"`
	PrinterID *int64 `json:"printer_id"`
}

func (h *Handler) handleSmartPlugByID(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(strings.TrimPrefix(r.URL.Path, "/api/smartplugs/"), 10, 64)
	if err != nil {
		jsonError(w, "invalid smart plug id", http.StatusBadRequest)
		return
	}

	switch r.Method {
	case http.MethodPut:
		var req smartPlugRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			jsonError(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if req.IP == "" {
			jsonError(w, "ip is required", http.StatusBadRequest)
			return
		}
		existing, _ := h.db.GetSmartPlug(id)
		if err := h.db.UpdateSmartPlug(id, req.IP, req.Idx, req.Label, req.HideLabel, req.PrinterID); err != nil {
			jsonError(w, "failed to update smart plug", http.StatusInternalServerError)
			return
		}
		if existing != nil && existing.PrinterID != nil {
			go h.poller.Repoll(h.ctx, *existing.PrinterID)
		}
		if req.PrinterID != nil && (existing == nil || existing.PrinterID == nil || *req.PrinterID != *existing.PrinterID) {
			go h.poller.Repoll(h.ctx, *req.PrinterID)
		}
		w.WriteHeader(http.StatusNoContent)

	case http.MethodDelete:
		existing, _ := h.db.GetSmartPlug(id)
		if err := h.db.DeleteSmartPlug(id); err != nil {
			jsonError(w, "failed to delete smart plug", http.StatusInternalServerError)
			return
		}
		if existing != nil && existing.PrinterID != nil {
			go h.poller.Repoll(h.ctx, *existing.PrinterID)
		}
		w.WriteHeader(http.StatusNoContent)

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (h *Handler) hasCameraAssigned(printerID int64) bool {
	_, err := h.db.GetCameraForPrinter(printerID)
	return err == nil
}

// Cameras — printspy-cam devices, managed independently of printers, optionally assigned to one.

func (h *Handler) handleCameras(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		cams, err := h.db.ListAllCameras()
		if err != nil {
			jsonError(w, "failed to list cameras", http.StatusInternalServerError)
			return
		}
		jsonResponse(w, cams)

	case http.MethodPost:
		var req cameraRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			jsonError(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if req.URL == "" {
			jsonError(w, "url is required", http.StatusBadRequest)
			return
		}
		id, err := h.db.CreateCamera(req.URL, req.Name, req.PrinterID)
		if err != nil {
			jsonError(w, "failed to create camera", http.StatusInternalServerError)
			return
		}
		if req.PrinterID != nil {
			go h.poller.Repoll(h.ctx, *req.PrinterID)
		}
		h.poller.BroadcastRefresh()
		jsonResponse(w, map[string]int64{"id": id})

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

type cameraRequest struct {
	URL       string `json:"url"`
	Name      string `json:"name"`
	PrinterID *int64 `json:"printer_id"`
}

func (h *Handler) handleCameraByID(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/cameras/")
	if idStr, ok := strings.CutSuffix(path, "/settings"); ok {
		id, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil {
			jsonError(w, "invalid camera id", http.StatusBadRequest)
			return
		}
		h.handleCameraSettings(w, r, id)
		return
	}

	id, err := strconv.ParseInt(path, 10, 64)
	if err != nil {
		jsonError(w, "invalid camera id", http.StatusBadRequest)
		return
	}

	switch r.Method {
	case http.MethodPut:
		var req cameraRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			jsonError(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if req.URL == "" {
			jsonError(w, "url is required", http.StatusBadRequest)
			return
		}
		existing, _ := h.db.GetCamera(id)
		if err := h.db.UpdateCamera(id, req.URL, req.Name, req.PrinterID); err != nil {
			jsonError(w, "failed to update camera", http.StatusInternalServerError)
			return
		}
		if existing != nil && existing.PrinterID != nil {
			go h.poller.Repoll(h.ctx, *existing.PrinterID)
		}
		if req.PrinterID != nil && (existing == nil || existing.PrinterID == nil || *req.PrinterID != *existing.PrinterID) {
			go h.poller.Repoll(h.ctx, *req.PrinterID)
		}
		h.poller.BroadcastRefresh()
		w.WriteHeader(http.StatusNoContent)

	case http.MethodDelete:
		existing, _ := h.db.GetCamera(id)
		if err := h.db.DeleteCamera(id); err != nil {
			jsonError(w, "failed to delete camera", http.StatusInternalServerError)
			return
		}
		if existing != nil && existing.PrinterID != nil {
			go h.poller.Repoll(h.ctx, *existing.PrinterID)
		}
		h.poller.BroadcastRefresh()
		w.WriteHeader(http.StatusNoContent)

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// handleCameraSettings proxies image-orientation settings straight through to
// the printspy-cam device itself - no auth, no digest logic, it has none.
func (h *Handler) handleCameraSettings(w http.ResponseWriter, r *http.Request, id int64) {
	cam, err := h.db.GetCamera(id)
	if err != nil {
		jsonError(w, "camera not found", http.StatusNotFound)
		return
	}
	base := strings.TrimRight(cam.URL, "/") + "/api/settings"

	switch r.Method {
	case http.MethodGet:
		req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, base, nil)
		if err != nil {
			jsonError(w, "failed to build request", http.StatusInternalServerError)
			return
		}
		resp, err := h.proxy.Do(req)
		if err != nil {
			jsonError(w, "failed to reach camera", http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		w.Header().Set("Content-Type", "application/json")
		io.Copy(w, resp.Body)

	case http.MethodPut:
		// Pure passthrough - printspy-cam's own firmware already validates
		// and ignores anything it doesn't recognize, so there's nothing for
		// this proxy to decode or re-encode.
		req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, base, r.Body)
		if err != nil {
			jsonError(w, "failed to build request", http.StatusInternalServerError)
			return
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := h.proxy.Do(req)
		if err != nil {
			jsonError(w, "failed to reach camera", http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		io.Copy(io.Discard, resp.Body)
		w.WriteHeader(http.StatusNoContent)

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// Print history and control

func (h *Handler) getRecentPrints(w http.ResponseWriter, r *http.Request, id int64) {
	switch r.Method {
	case http.MethodGet:
	case http.MethodDelete:
		h.deleteRecentFile(w, r, id)
		return
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	limit := 5
	if r.URL.Query().Get("all") == "1" {
		limit = 0
	} else if v, err := h.db.GetSetting("recent_files_count"); err == nil && v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	files, err := h.poller.GetRecentFiles(r.Context(), id, limit)
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if files == nil {
		files = []models.RecentFile{}
	}
	jsonResponse(w, files)
}

func (h *Handler) deleteRecentFile(w http.ResponseWriter, r *http.Request, id int64) {
	origin := r.URL.Query().Get("origin")
	path := r.URL.Query().Get("path")
	if origin == "" || path == "" {
		jsonError(w, "origin and path are required", http.StatusBadRequest)
		return
	}
	if strings.Contains(path, "..") {
		jsonError(w, "invalid path", http.StatusBadRequest)
		return
	}

	if err := h.poller.DeleteFile(r.Context(), id, origin, path); err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	jsonResponse(w, map[string]any{"success": true})
}

func (h *Handler) startPrint(w http.ResponseWriter, r *http.Request, id int64) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Origin string `json:"origin"`
		Path   string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Path == "" {
		jsonError(w, "path is required", http.StatusBadRequest)
		return
	}
	if req.Origin == "" {
		req.Origin = "local"
	}

	status := h.poller.GetStatus(id)
	if status != nil && (status.State == models.StatePrinting || status.State == models.StatePaused) {
		jsonError(w, "printer is busy", http.StatusConflict)
		return
	}

	if err := h.poller.StartPrint(r.Context(), id, req.Origin, req.Path); err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	jsonResponse(w, map[string]any{"success": true, "status": h.waitForStateChange(r.Context(), id, currentState(status))})
}

// currentState returns status.State, or StateIdle if status is nil (printer
// never successfully polled yet).
func currentState(status *models.PrinterStatus) models.PrinterState {
	if status == nil {
		return models.StateIdle
	}
	return status.State
}

// printControlTimeout is how long waitForStateChange waits for a real state
// transition before giving up - Settings → General, default 15s. A resume
// that needs to reheat before motion resumes can take longer than a plain
// cancel/pause; past this bound, the frontend just shows whatever's current
// and the next natural poll tick catches up, same as it always has.
func (h *Handler) printControlTimeout() time.Duration {
	secs := 15
	if v, err := h.db.GetSetting("print_control_timeout_secs"); err == nil && v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			secs = n
		}
	}
	return time.Duration(secs) * time.Second
}

// waitForStateChange repolls id every second until its state differs from
// prevState or timeout elapses, then returns whatever status that leaves.
// A single immediate repoll right after issuing a print command routinely
// catches the printer still mid-transition (accepting "cancel" happens
// instantly; the printer physically stopping and reporting idle does not) -
// returning that stale-in-effect status let a confirm button clear well
// before the card had anything new to show. This gives the real printer a
// bounded window to settle before the response (and so the button/card
// update) goes out, without risking hanging the request indefinitely.
func (h *Handler) waitForStateChange(ctx context.Context, id int64, prevState models.PrinterState) *models.PrinterStatus {
	deadline := time.Now().Add(h.printControlTimeout())
	for {
		h.poller.Repoll(ctx, id)
		status := h.poller.GetStatus(id)
		if status == nil || status.State != prevState || time.Now().After(deadline) {
			return status
		}
		time.Sleep(time.Second)
	}
}

func (h *Handler) uploadFile(w http.ResponseWriter, r *http.Request, id int64) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	filename := r.URL.Query().Get("filename")
	if filename == "" {
		jsonError(w, "filename is required", http.StatusBadRequest)
		return
	}
	if strings.Contains(filename, "..") {
		jsonError(w, "invalid filename", http.StatusBadRequest)
		return
	}
	printNow := r.URL.Query().Get("print_now") == "true"

	r.Body = http.MaxBytesReader(w, r.Body, 200<<20)
	data, err := io.ReadAll(r.Body)
	if err != nil {
		jsonError(w, "file too large or unreadable", http.StatusBadRequest)
		return
	}

	// PrusaLink's internal flash ("local") is read-only over the network API -
	// only removable media ("usb", which also covers SD cards) accepts writes.
	if err := h.poller.UploadFile(r.Context(), id, "usb", filename, data, printNow); err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	jsonResponse(w, map[string]any{"success": true})
}

func (h *Handler) controlPrint(w http.ResponseWriter, r *http.Request, id int64) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Action string `json:"action"` // "pause", "resume", "cancel"
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "invalid request body", http.StatusBadRequest)
		return
	}

	prevState := currentState(h.poller.GetStatus(id))

	if err := h.poller.ControlPrint(r.Context(), id, req.Action); err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// See waitForStateChange for why this waits rather than repolling once.
	jsonResponse(w, map[string]any{"success": true, "status": h.waitForStateChange(r.Context(), id, prevState)})
}

// Config export/import

type configExport struct {
	Settings   map[string]string       `yaml:"settings,omitempty"`
	Printers   []configExportPrinter   `yaml:"printers"`
	SmartPlugs []configExportSmartPlug `yaml:"smart_plugs,omitempty"`
	Cameras    []configExportCamera    `yaml:"cameras,omitempty"`
}

// PrinterIndex references configExport.Printers by position, since printer
// IDs are reassigned on import and won't match the source instance.
type configExportSmartPlug struct {
	PrinterIndex *int   `yaml:"printer_index,omitempty"`
	IP           string `yaml:"ip"`
	Idx          string `yaml:"idx,omitempty"`
	Label        string `yaml:"label,omitempty"`
	HideLabel    bool   `yaml:"hide_label,omitempty"`
}

type configExportCamera struct {
	PrinterIndex *int   `yaml:"printer_index,omitempty"`
	URL          string `yaml:"url"`
	Name         string `yaml:"name,omitempty"`
}

type configExportPrinter struct {
	Name               string  `yaml:"name"`
	Type               string  `yaml:"type"`
	Model              string  `yaml:"model,omitempty"`
	HideModel          bool    `yaml:"hide_model,omitempty"`
	URL                string  `yaml:"url"`
	APIKey             string  `yaml:"api_key"`
	Username           string  `yaml:"username,omitempty"`
	PollInterval       int     `yaml:"poll_interval"`
	Enabled            bool    `yaml:"enabled"`
	IdleTimeoutMinutes int     `yaml:"idle_timeout_minutes,omitempty"`
	MaxBedTemp         float64 `yaml:"max_bed_temp,omitempty"`
	MaxExtruderTemp    float64 `yaml:"max_extruder_temp,omitempty"`
}

func (h *Handler) handleConfigExport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	printers, err := h.db.ListPrinters()
	if err != nil {
		jsonError(w, "failed to list printers", http.StatusInternalServerError)
		return
	}

	settings, err := h.db.GetAllSettings()
	if err != nil {
		settings = make(map[string]string)
	}

	export := configExport{
		Settings: settings,
		Printers: make([]configExportPrinter, len(printers)),
	}

	printerIndex := make(map[int64]int, len(printers))
	for i, p := range printers {
		printerIndex[p.ID] = i
		full, err := h.db.GetPrinter(p.ID)
		if err != nil {
			continue
		}
		export.Printers[i] = configExportPrinter{
			Name:               full.Name,
			Type:               full.Type,
			Model:              full.Model,
			HideModel:          full.HideModel,
			URL:                full.URL,
			APIKey:             full.APIKey,
			Username:           full.Username,
			PollInterval:       full.PollInterval,
			Enabled:            full.Enabled,
			IdleTimeoutMinutes: full.IdleTimeoutMinutes,
			MaxBedTemp:         full.MaxBedTemp,
			MaxExtruderTemp:    full.MaxExtruderTemp,
		}
	}

	if plugs, err := h.db.ListAllSmartPlugs(); err == nil {
		export.SmartPlugs = make([]configExportSmartPlug, len(plugs))
		for i, sp := range plugs {
			export.SmartPlugs[i] = configExportSmartPlug{
				PrinterIndex: printerIndexPtr(sp.PrinterID, printerIndex),
				IP:           sp.IP,
				Idx:          sp.Idx,
				Label:        sp.Label,
				HideLabel:    sp.HideLabel,
			}
		}
	}

	if cams, err := h.db.ListAllCameras(); err == nil {
		export.Cameras = make([]configExportCamera, len(cams))
		for i, c := range cams {
			export.Cameras[i] = configExportCamera{
				PrinterIndex: printerIndexPtr(c.PrinterID, printerIndex),
				URL:          c.URL,
				Name:         c.Name,
			}
		}
	}

	w.Header().Set("Content-Type", "application/x-yaml")
	w.Header().Set("Content-Disposition", "attachment; filename=printspy-config.yaml")
	yaml.NewEncoder(w).Encode(export)
}

func printerIndexPtr(id *int64, index map[int64]int) *int {
	if id == nil {
		return nil
	}
	if i, ok := index[*id]; ok {
		return &i
	}
	return nil
}

func (h *Handler) handleConfigImport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var export configExport
	if err := yaml.NewDecoder(r.Body).Decode(&export); err != nil {
		jsonError(w, "invalid YAML: "+err.Error(), http.StatusBadRequest)
		return
	}

	for k, v := range export.Settings {
		validated, err := validateSetting(k, v)
		if err != nil {
			continue
		}
		h.db.SetSetting(k, validated)
	}

	added := 0
	newPrinterIDs := make([]*int64, len(export.Printers))
	for i, ep := range export.Printers {
		if ep.URL == "" || ep.APIKey == "" {
			continue
		}
		if ep.Type == "" {
			ep.Type = "octoprint"
		}
		if ep.PollInterval <= 0 {
			ep.PollInterval = 10
		}
		p := models.PrinterConfig{
			Name:               ep.Name,
			Type:               ep.Type,
			Model:              ep.Model,
			HideModel:          ep.HideModel,
			URL:                ep.URL,
			APIKey:             ep.APIKey,
			Username:           ep.Username,
			PollInterval:       ep.PollInterval,
			Enabled:            ep.Enabled,
			IdleTimeoutMinutes: ep.IdleTimeoutMinutes,
			MaxBedTemp:         ep.MaxBedTemp,
			MaxExtruderTemp:    ep.MaxExtruderTemp,
		}
		if err := h.db.CreatePrinter(&p); err != nil {
			continue
		}
		newPrinterIDs[i] = &p.ID
		if p.Enabled {
			h.poller.AddPrinter(h.ctx, p)
		}
		added++
	}

	resolvePrinterID := func(idx *int) *int64 {
		if idx == nil || *idx < 0 || *idx >= len(newPrinterIDs) {
			return nil
		}
		return newPrinterIDs[*idx]
	}

	plugsAdded := 0
	for _, sp := range export.SmartPlugs {
		if sp.IP == "" {
			continue
		}
		if _, err := h.db.CreateSmartPlug(sp.IP, sp.Idx, sp.Label, sp.HideLabel, resolvePrinterID(sp.PrinterIndex)); err == nil {
			plugsAdded++
		}
	}

	camerasAdded := 0
	for _, c := range export.Cameras {
		if c.URL == "" {
			continue
		}
		if _, err := h.db.CreateCamera(c.URL, c.Name, resolvePrinterID(c.PrinterIndex)); err == nil {
			camerasAdded++
		}
	}

	h.poller.BroadcastRefresh()
	jsonResponse(w, map[string]any{
		"success":        true,
		"printers_added": added,
		"plugs_added":    plugsAdded,
		"cameras_added":  camerasAdded,
	})
}

// Webcam/Thumbnail proxies

func (h *Handler) handleWebcamProxy(w http.ResponseWriter, r *http.Request) {
	idStr := strings.TrimPrefix(r.URL.Path, "/api/webcam/")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid printer id", http.StatusBadRequest)
		return
	}

	webcamURL := ""
	usingCamera := false
	if cam, err := h.db.GetCameraForPrinter(id); err == nil {
		webcamURL = strings.TrimRight(cam.URL, "/") + "/stream"
		usingCamera = true
	} else {
		webcamURL = h.poller.GetWebcamURL(id)
	}
	if webcamURL == "" {
		http.Error(w, "no webcam configured", http.StatusNotFound)
		return
	}

	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, webcamURL, nil)
	if err != nil {
		http.Error(w, "failed to create request", http.StatusInternalServerError)
		return
	}
	if !usingCamera {
		printer, err := h.db.GetPrinter(id)
		if err != nil {
			http.Error(w, "printer not found", http.StatusNotFound)
			return
		}
		req.Header.Set("X-Api-Key", printer.APIKey)
	}

	log.Printf("[webcam:%d] proxying stream from %s", id, webcamURL)
	streamTransport := netguard.Transport()
	streamTransport.ResponseHeaderTimeout = 10 * time.Second
	streamClient := &http.Client{Transport: streamTransport}
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

	snapshotURL := ""
	usingCamera := false
	if cam, err := h.db.GetCameraForPrinter(id); err == nil {
		snapshotURL = strings.TrimRight(cam.URL, "/") + "/snapshot"
		usingCamera = true
	} else {
		snapshotURL = h.poller.GetSnapshotURL(id)
	}
	if snapshotURL == "" {
		http.Error(w, "no webcam configured", http.StatusNotFound)
		return
	}

	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, snapshotURL, nil)
	if err != nil {
		http.Error(w, "failed to create request", http.StatusInternalServerError)
		return
	}

	var resp *http.Response
	if usingCamera {
		resp, err = h.proxy.Do(req)
	} else {
		resp, err = h.poller.AuthenticatedDo(id, h.proxy, req)
	}
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
	w.WriteHeader(resp.StatusCode)
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

	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, thumbURL, nil)
	if err != nil {
		http.Error(w, "failed to create request", http.StatusInternalServerError)
		return
	}

	resp, err := h.poller.AuthenticatedDo(id, h.proxy, req)
	if err != nil {
		http.Error(w, "failed to fetch thumbnail", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

func (h *Handler) handleFileThumbnailProxy(w http.ResponseWriter, r *http.Request) {
	idStr := strings.TrimPrefix(r.URL.Path, "/api/file-thumbnail/")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid printer id", http.StatusBadRequest)
		return
	}

	thumbPath := r.URL.Query().Get("path")
	if thumbPath == "" {
		http.Error(w, "path parameter required", http.StatusBadRequest)
		return
	}

	if strings.Contains(thumbPath, "..") {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}

	printer, err := h.db.GetPrinter(id)
	if err != nil {
		http.Error(w, "printer not found", http.StatusNotFound)
		return
	}

	thumbURL := strings.TrimRight(printer.URL, "/") + "/" + strings.TrimLeft(thumbPath, "/")
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, thumbURL, nil)
	if err != nil {
		http.Error(w, "failed to create request", http.StatusInternalServerError)
		return
	}

	resp, err := h.poller.AuthenticatedDo(id, h.proxy, req)
	if err != nil {
		http.Error(w, "failed to fetch thumbnail", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
	w.Header().Set("Cache-Control", "max-age=3600")
	w.WriteHeader(resp.StatusCode)
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
	case "recent_files_count":
		n, err := strconv.Atoi(value)
		if err != nil {
			return "", fmt.Errorf("recent_files_count must be a number")
		}
		if n < 1 {
			n = 1
		} else if n > 20 {
			n = 20
		}
		return strconv.Itoa(n), nil
	case "poll_interval":
		n, err := strconv.Atoi(value)
		if err != nil {
			return "", fmt.Errorf("poll_interval must be a number")
		}
		if n < 3 {
			n = 3
		} else if n > 60 {
			n = 60
		}
		return strconv.Itoa(n), nil
	case "print_control_timeout_secs":
		n, err := strconv.Atoi(value)
		if err != nil {
			return "", fmt.Errorf("print_control_timeout_secs must be a number")
		}
		if n < 5 {
			n = 5
		} else if n > 60 {
			n = 60
		}
		return strconv.Itoa(n), nil
	case "prusalink_ping_interval":
		n, err := strconv.Atoi(value)
		if err != nil {
			return "", fmt.Errorf("prusalink_ping_interval must be a number")
		}
		if n <= 0 {
			return "0", nil
		}
		if n < 1 {
			n = 1
		} else if n > 60 {
			n = 60
		}
		return strconv.Itoa(n), nil
	case "auto_off_idle_minutes":
		n, err := strconv.Atoi(value)
		if err != nil {
			return "", fmt.Errorf("auto_off_idle_minutes must be a number")
		}
		if n <= 0 {
			return "0", nil
		}
		if n > 1440 {
			n = 1440
		}
		return strconv.Itoa(n), nil
	case "auto_off_cooldown_temp":
		n, err := strconv.Atoi(value)
		if err != nil {
			return "", fmt.Errorf("auto_off_cooldown_temp must be a number")
		}
		if n < 10 {
			n = 10
		} else if n > 100 {
			n = 100
		}
		return strconv.Itoa(n), nil
	case "thermal_max_bed_temp":
		return validateTempSetting(key, value, 150)
	case "thermal_max_extruder_temp":
		return validateTempSetting(key, value, 350)
	case "notify_on_start", "notify_on_complete", "notify_on_failed", "notify_on_error",
		"notify_checkpoint1_enabled", "notify_checkpoint2_enabled",
		"notify_start_high_priority", "notify_complete_high_priority", "notify_failed_high_priority", "notify_error_high_priority",
		"notify_checkpoint1_high_priority", "notify_checkpoint2_high_priority":
		if value != "0" && value != "1" {
			return "", fmt.Errorf("%s must be 0 or 1", key)
		}
		return value, nil
	case "notify_checkpoint1_percent", "notify_checkpoint2_percent":
		n, err := strconv.Atoi(value)
		if err != nil {
			return "", fmt.Errorf("%s must be a number", key)
		}
		if n < 1 {
			n = 1
		} else if n > 99 {
			n = 99
		}
		return strconv.Itoa(n), nil
	case "notify_start_sound", "notify_complete_sound", "notify_failed_sound", "notify_error_sound",
		"notify_checkpoint1_sound", "notify_checkpoint2_sound":
		if value != "" && !slices.Contains(notify.Sounds, value) {
			return "", fmt.Errorf("%s must be a valid Pushover sound", key)
		}
		return value, nil
	case "notify_start_title", "notify_complete_title", "notify_failed_title", "notify_error_title",
		"notify_checkpoint1_title", "notify_checkpoint2_title",
		"notify_start_message", "notify_complete_message", "notify_failed_message", "notify_error_message",
		"notify_checkpoint1_message", "notify_checkpoint2_message":
		return value, nil
	case "pushover_user_key", "pushover_app_token":
		return value, nil
	default:
		return "", fmt.Errorf("unknown setting: %s", key)
	}
}

// validateTempSetting validates a °C setting that's disabled at 0 and
// clamped to max otherwise (thermal_max_bed_temp/thermal_max_extruder_temp).
func validateTempSetting(key, value string, max float64) (string, error) {
	n, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return "", fmt.Errorf("%s must be a number", key)
	}
	if n <= 0 {
		return "0", nil
	}
	if n > max {
		n = max
	}
	return strconv.FormatFloat(n, 'f', -1, 64), nil
}

// Ingest targets/jobs — slicer print-host target admin API.
// See ingest.Handler for the slicer-facing (X-Api-Key authenticated) side.

const dispatchOnlineTimeout = 90 * time.Second

func (h *Handler) handleIngestKeys(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		targets, err := h.db.ListIngestTargets()
		if err != nil {
			jsonError(w, "failed to list ingest targets", http.StatusInternalServerError)
			return
		}
		jsonResponse(w, targets)

	case http.MethodPost:
		var req ingestTargetRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			jsonError(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if err := req.validate(); err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		if req.Label != "" {
			if _, err := h.db.GetIngestTargetByLabel(req.Label); err == nil {
				jsonError(w, "label already in use by another target", http.StatusConflict)
				return
			}
		}
		apiKey, err := newSessionToken()
		if err != nil {
			jsonError(w, "failed to generate api key", http.StatusInternalServerError)
			return
		}
		id, err := h.db.CreateIngestTarget("", req.PrinterID, req.Label, apiKey)
		if err != nil {
			jsonError(w, "failed to create ingest target", http.StatusInternalServerError)
			return
		}
		jsonResponse(w, map[string]any{"id": id, "api_key": apiKey})

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// ingestTargetRequest is always pinned to one specific printer - no ambiguity
// to resolve at relay time.
type ingestTargetRequest struct {
	PrinterID *int64 `json:"printer_id"`
	Label     string `json:"label"`
}

var ingestLabelSlug = regexp.MustCompile(`^[a-z0-9-]+$`)

func (r ingestTargetRequest) validate() error {
	if r.PrinterID == nil {
		return fmt.Errorf("printer_id is required")
	}
	// Label doubles as the /ingest/{label} URL slug (see ingest.Handler.route),
	// so it's restricted to what's safe in a URL path segment.
	if r.Label != "" && !ingestLabelSlug.MatchString(r.Label) {
		return fmt.Errorf("label must be lowercase letters, numbers, and hyphens only")
	}
	return nil
}

func (h *Handler) handleIngestKeyByID(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(strings.TrimPrefix(r.URL.Path, "/api/ingest-keys/"), 10, 64)
	if err != nil {
		jsonError(w, "invalid ingest target id", http.StatusBadRequest)
		return
	}

	switch r.Method {
	case http.MethodPut:
		var req ingestTargetRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			jsonError(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if err := req.validate(); err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		if req.Label != "" {
			if existing, err := h.db.GetIngestTargetByLabel(req.Label); err == nil && existing.ID != id {
				jsonError(w, "label already in use by another target", http.StatusConflict)
				return
			}
		}
		if err := h.db.UpdateIngestTarget(id, "", req.PrinterID, req.Label); err != nil {
			jsonError(w, "failed to update ingest target", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)

	case http.MethodDelete:
		if err := h.db.DeleteIngestTarget(id); err != nil {
			jsonError(w, "failed to delete ingest target", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (h *Handler) handleIngestJobs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	jobs, err := h.db.ListIngestJobs()
	if err != nil {
		jsonError(w, "failed to list ingest jobs", http.StatusInternalServerError)
		return
	}
	jsonResponse(w, jobs)
}

func (h *Handler) handleIngestJobByID(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/ingest-jobs/")
	parts := strings.SplitN(path, "/", 2)
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		jsonError(w, "invalid ingest job id", http.StatusBadRequest)
		return
	}

	if len(parts) == 2 && parts[1] == "retry" {
		h.retryIngestJob(w, r, id)
		return
	}

	if r.Method != http.MethodDelete {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	job, err := h.db.GetIngestJob(id)
	if err != nil {
		jsonError(w, "ingest job not found", http.StatusNotFound)
		return
	}
	if err := h.db.DeleteIngestJob(id); err != nil {
		jsonError(w, "failed to delete ingest job", http.StatusInternalServerError)
		return
	}
	os.RemoveAll(filepath.Dir(job.FilePath))
	jsonResponse(w, map[string]bool{"success": true})
}

// retryIngestJob manually re-triggers a failed job - same power-on-if-needed
// + wait + relay path as the automatic flows, just human-initiated instead
// of fired by an upload or a poller state transition. Printer's already
// known (jobs are always pinned), so this needs no request body.
func (h *Handler) retryIngestJob(w http.ResponseWriter, r *http.Request, jobID int64) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	job, err := h.db.GetIngestJob(jobID)
	if err != nil {
		jsonError(w, "ingest job not found", http.StatusNotFound)
		return
	}
	if job.PinnedPrinterID == nil {
		jsonError(w, "job has no pinned printer", http.StatusBadRequest)
		return
	}
	printer, err := h.db.GetPrinter(*job.PinnedPrinterID)
	if err != nil {
		jsonError(w, "printer not found", http.StatusNotFound)
		return
	}
	claimed, err := h.db.ClaimIngestJobForDispatch(jobID, *job.PinnedPrinterID)
	if err != nil {
		jsonError(w, "failed to claim ingest job", http.StatusInternalServerError)
		return
	}
	if !claimed {
		jsonError(w, "job already dispatching or resolved", http.StatusConflict)
		return
	}
	go h.runDispatch(*job, *printer)
	w.WriteHeader(http.StatusAccepted)
}

// AutoDispatchIngestJob is ingest.Handler's callback for a Print-After-Upload
// slicer upload - power on the pinned printer if needed, wait for it
// online, relay, and print. Errors just land the job in 'failed'.
func (h *Handler) AutoDispatchIngestJob(jobID, printerID int64) {
	job, printer, err := h.getJobAndPrinter(jobID, printerID)
	if err != nil {
		return
	}
	claimed, err := h.db.ClaimIngestJobForDispatch(jobID, printerID)
	if err != nil || !claimed {
		return
	}
	h.runDispatch(*job, *printer)
}

func (h *Handler) getJobAndPrinter(jobID, printerID int64) (*models.IngestJob, *models.PrinterConfig, error) {
	job, err := h.db.GetIngestJob(jobID)
	if err != nil {
		return nil, nil, fmt.Errorf("ingest job not found")
	}
	printer, err := h.db.GetPrinter(printerID)
	if err != nil {
		return nil, nil, fmt.Errorf("printer not found")
	}
	return job, printer, nil
}

func plugIsOn(status *models.PrinterStatus, plugID string) bool {
	if status == nil {
		return false
	}
	for _, p := range status.Power {
		if p.ID == plugID {
			return p.On
		}
	}
	return false
}

// runDispatch powers on printer's assigned smart plug(s) if off, waits for
// it to come online, then relays the staged file via the same UploadFile
// path the dashboard's own upload feature uses. Uses h.ctx (the app's
// long-lived context) rather than the request's, since the HTTP request is
// already closed by any HTTP path that reaches here (main.go wires this to
// ingest.Handler as an async callback, not a request handler).
func (h *Handler) runDispatch(job models.IngestJob, printer models.PrinterConfig) {
	defer h.poller.BroadcastRefresh()

	plugs, err := h.db.ListSmartPlugs(printer.ID)
	if err != nil {
		h.db.SetIngestJobFailed(job.ID, "failed to look up smart plugs: "+err.Error())
		return
	}
	status := h.poller.GetStatus(printer.ID)
	for _, plug := range plugs {
		plugID := plug.IP + ":" + plug.Idx
		if plugIsOn(status, plugID) {
			continue
		}
		if err := h.poller.SetPowerState(h.ctx, printer.ID, plugID, true); err != nil {
			h.db.SetIngestJobFailed(job.ID, "failed to power on printer: "+err.Error())
			return
		}
	}

	ctx, cancel := context.WithTimeout(h.ctx, dispatchOnlineTimeout)
	defer cancel()
	if err := h.poller.WaitOnline(ctx, printer.ID); err != nil {
		h.db.SetIngestJobFailed(job.ID, err.Error())
		return
	}

	data, err := os.ReadFile(job.FilePath)
	if err != nil {
		h.db.SetIngestJobFailed(job.ID, "failed to read staged file: "+err.Error())
		return
	}
	// job.PrintAfter, not a hardcoded true - runDispatch also serves manual
	// retry (retryIngestJob) for a job that originally staged without
	// Print-After-Upload, which shouldn't start printing just because a
	// human retried the transfer.
	if err := h.poller.UploadFile(h.ctx, printer.ID, "usb", job.Filename, data, job.PrintAfter); err != nil {
		h.db.SetIngestJobFailed(job.ID, err.Error())
		return
	}

	h.db.DeleteIngestJob(job.ID)
	os.RemoveAll(filepath.Dir(job.FilePath))
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
