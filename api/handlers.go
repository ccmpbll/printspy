package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ccmpbll/printspy/db"
	"github.com/ccmpbll/printspy/digestauth"
	"github.com/ccmpbll/printspy/models"
	"github.com/ccmpbll/printspy/netguard"
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
	Username     string `json:"username"`
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
		Username:     req.Username,
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
		case "power":
			h.handlePower(w, r, id)
		case "recent":
			h.getRecentPrints(w, r, id)
		case "print":
			h.startPrint(w, r, id)
		case "control":
			h.controlPrint(w, r, id)
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
		ID:           id,
		Name:         req.Name,
		Type:         req.Type,
		URL:          req.URL,
		APIKey:       req.APIKey,
		Username:     req.Username,
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

	log.Printf("[sse] client connected: %s", r.RemoteAddr)
	defer log.Printf("[sse] client disconnected: %s", r.RemoteAddr)

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
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
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

// Print history and control

func (h *Handler) getRecentPrints(w http.ResponseWriter, r *http.Request, id int64) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	limit := 5
	if v, err := h.db.GetSetting("recent_files_count"); err == nil && v != "" {
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

	if err := h.poller.ControlPrint(r.Context(), id, req.Action); err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	jsonResponse(w, map[string]any{"success": true})
}

// Config export/import

type configExport struct {
	Settings map[string]string     `yaml:"settings,omitempty"`
	Printers []configExportPrinter `yaml:"printers"`
}

type configExportPrinter struct {
	Name         string `yaml:"name"`
	Type         string `yaml:"type"`
	URL          string `yaml:"url"`
	APIKey       string `yaml:"api_key"`
	Username     string `yaml:"username,omitempty"`
	PollInterval int    `yaml:"poll_interval"`
	Enabled      bool   `yaml:"enabled"`
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

	for i, p := range printers {
		full, err := h.db.GetPrinter(p.ID)
		if err != nil {
			continue
		}
		export.Printers[i] = configExportPrinter{
			Name:         full.Name,
			Type:         full.Type,
			URL:          full.URL,
			APIKey:       full.APIKey,
			Username:     full.Username,
			PollInterval: full.PollInterval,
			Enabled:      full.Enabled,
		}
	}

	w.Header().Set("Content-Type", "application/x-yaml")
	w.Header().Set("Content-Disposition", "attachment; filename=printspy-config.yaml")
	yaml.NewEncoder(w).Encode(export)
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
	for _, ep := range export.Printers {
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
			Name:         ep.Name,
			Type:         ep.Type,
			URL:          ep.URL,
			APIKey:       ep.APIKey,
			Username:     ep.Username,
			PollInterval: ep.PollInterval,
			Enabled:      ep.Enabled,
		}
		if err := h.db.CreatePrinter(&p); err != nil {
			continue
		}
		if p.Enabled {
			h.poller.AddPrinter(h.ctx, p)
		}
		added++
	}

	h.poller.BroadcastRefresh()
	jsonResponse(w, map[string]any{"success": true, "printers_added": added})
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
	if printer.Type == "prusalink" {
		req.SetBasicAuth(printer.Username, printer.APIKey)
	} else {
		req.Header.Set("X-Api-Key", printer.APIKey)
	}

	resp, err := h.proxy.Do(req)
	if err != nil {
		h.logOnce(fmt.Sprintf("snapshot-err-%d", id), 30*time.Second, "[snapshot:%d] failed to fetch from %s: %v", id, snapshotURL, err)
		http.Error(w, "failed to fetch snapshot", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized && printer.Type == "prusalink" {
		authHeader := resp.Header.Get("WWW-Authenticate")
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()

		parsed, _ := url.Parse(snapshotURL)
		resp2, err := h.digestRetry(r, snapshotURL, parsed.Path, printer.Username, printer.APIKey, authHeader)
		if err != nil {
			h.logOnce(fmt.Sprintf("snapshot-err-%d", id), 30*time.Second, "[snapshot:%d] digest auth failed for %s: %v", id, snapshotURL, err)
			http.Error(w, "failed to fetch snapshot", http.StatusBadGateway)
			return
		}
		defer resp2.Body.Close()
		w.Header().Set("Content-Type", resp2.Header.Get("Content-Type"))
		w.Header().Set("Cache-Control", "no-cache, no-store")
		io.Copy(w, resp2.Body)
		return
	}

	if resp.StatusCode != http.StatusOK {
		h.logOnce(fmt.Sprintf("snapshot-status-%d", id), 30*time.Second, "[snapshot:%d] unexpected status %d from %s", id, resp.StatusCode, snapshotURL)
	}

	w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
	w.Header().Set("Cache-Control", "no-cache, no-store")
	io.Copy(w, resp.Body)
}

// digestRetry re-issues a GET with a digest Authorization header built from a 401's WWW-Authenticate challenge.
func (h *Handler) digestRetry(r *http.Request, targetURL, uriPath, username, apiKey, authHeader string) (*http.Response, error) {
	req2, err := http.NewRequestWithContext(r.Context(), http.MethodGet, targetURL, nil)
	if err != nil {
		return nil, err
	}
	req2.Header.Set("Authorization", digestauth.BuildHeader(username, apiKey, http.MethodGet, uriPath, authHeader))
	return h.proxy.Do(req2)
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
	if printer.Type == "prusalink" {
		req.SetBasicAuth(printer.Username, printer.APIKey)
	} else {
		req.Header.Set("X-Api-Key", printer.APIKey)
	}

	resp, err := h.proxy.Do(req)
	if err != nil {
		http.Error(w, "failed to fetch thumbnail", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized && printer.Type == "prusalink" {
		authHeader := resp.Header.Get("WWW-Authenticate")
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()

		parsed, _ := url.Parse(thumbURL)
		resp2, err := h.digestRetry(r, thumbURL, parsed.Path, printer.Username, printer.APIKey, authHeader)
		if err != nil {
			http.Error(w, "failed to fetch thumbnail", http.StatusBadGateway)
			return
		}
		defer resp2.Body.Close()
		w.Header().Set("Content-Type", resp2.Header.Get("Content-Type"))
		w.Header().Set("Cache-Control", "no-cache")
		io.Copy(w, resp2.Body)
		return
	}

	w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
	w.Header().Set("Cache-Control", "no-cache")
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
	if printer.Type == "prusalink" {
		req.SetBasicAuth(printer.Username, printer.APIKey)
	} else {
		req.Header.Set("X-Api-Key", printer.APIKey)
	}

	resp, err := h.proxy.Do(req)
	if err != nil {
		http.Error(w, "failed to fetch thumbnail", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized && printer.Type == "prusalink" {
		authHeader := resp.Header.Get("WWW-Authenticate")
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()

		uriPath := "/" + strings.TrimLeft(thumbPath, "/")
		resp2, err := h.digestRetry(r, thumbURL, uriPath, printer.Username, printer.APIKey, authHeader)
		if err != nil {
			http.Error(w, "failed to fetch thumbnail", http.StatusBadGateway)
			return
		}
		defer resp2.Body.Close()
		w.Header().Set("Content-Type", resp2.Header.Get("Content-Type"))
		w.Header().Set("Cache-Control", "max-age=3600")
		io.Copy(w, resp2.Body)
		return
	}

	w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
	w.Header().Set("Cache-Control", "max-age=3600")
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
