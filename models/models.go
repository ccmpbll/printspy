package models

import (
	"encoding/json"
	"time"
)

type PrinterState string

const (
	StateIdle         PrinterState = "idle"
	StatePrinting     PrinterState = "printing"
	StatePaused       PrinterState = "paused"
	StateError        PrinterState = "error"
	StateAttention    PrinterState = "attention"
	StateOffline      PrinterState = "offline"
	StateDisconnected PrinterState = "disconnected"
)

type PrinterConfig struct {
	ID           int64  `json:"id"`
	Name         string `json:"name"`
	Type         string `json:"type"`
	Model        string `json:"model,omitempty"`
	HideModel    bool   `json:"hide_model,omitempty"`
	URL          string `json:"url"`
	APIKey       string `json:"-"`
	Username     string `json:"username,omitempty"`
	Enabled      bool   `json:"enabled"`
	Maintenance  bool   `json:"maintenance"`
	PollInterval int    `json:"poll_interval"`
	SortOrder    int    `json:"sort_order"`
	CreatedAt    string `json:"created_at"`
	UpdatedAt    string `json:"updated_at"`
	// Per-printer overrides for auto-off/thermal-runaway (poller.go's
	// checkAutoOff/checkThermalRunaway); global settings of the same name
	// win if set. 0 = disabled (per-printer default).
	IdleTimeoutMinutes int     `json:"idle_timeout_minutes,omitempty"`
	MaxBedTemp         float64 `json:"max_bed_temp,omitempty"`
	MaxExtruderTemp    float64 `json:"max_extruder_temp,omitempty"`
}

// SmartPlug is a directly-configured Tasmota device, managed independently of
// printers and optionally assigned to any one of them.
type SmartPlug struct {
	ID          int64  `json:"id"`
	PrinterID   *int64 `json:"printer_id"`
	PrinterName string `json:"printer_name,omitempty"`
	IP          string `json:"ip"`
	Idx         string `json:"idx"`
	Label       string `json:"label"`
	HideLabel   bool   `json:"hide_label"`
	MQTTTopic   string `json:"mqtt_topic,omitempty"`
}

// Camera is a directly-configured printspy-cam device, managed independently
// of printers and optionally assigned to any one of them. When assigned, it
// overrides whatever webcam a printer's own plugin would otherwise discover.
type Camera struct {
	ID          int64  `json:"id"`
	PrinterID   *int64 `json:"printer_id"`
	PrinterName string `json:"printer_name,omitempty"`
	URL         string `json:"url"`
	Name        string `json:"name"`
}

type Temperatures struct {
	HotendActual  float64 `json:"hotend_actual"`
	HotendTarget  float64 `json:"hotend_target"`
	BedActual     float64 `json:"bed_actual"`
	BedTarget     float64 `json:"bed_target"`
	ChamberActual float64 `json:"chamber_actual"`
	ChamberTarget float64 `json:"chamber_target"`
	HasChamber    bool    `json:"has_chamber"`
}

type JobInfo struct {
	FileName       string  `json:"file_name"`
	Progress       float64 `json:"progress"`
	ElapsedSecs    int     `json:"elapsed_secs"`
	RemainingSecs  int     `json:"remaining_secs"`
	EstimatedTotal int     `json:"estimated_total"`
	CurrentLayer   int     `json:"current_layer"`
	TotalLayers    int     `json:"total_layers"`
	FilamentUsedMM float64 `json:"filament_used_mm"`
	// FilePath is the plugin-native ref for re-fetching this exact file
	// (e.g. PrusaLink's "/usb/SPOOLD~1.BGC") - distinct from FileName, which
	// is the human-readable display name and NOT a valid path segment (the
	// printer's own filesystem uses an 8.3-mangled short name instead).
	// Empty for plugins with no such capability (OctoPrint).
	FilePath string `json:"-"`
}

type PowerState struct {
	ID        string  `json:"id"`
	Label     string  `json:"label,omitempty"`
	HideLabel bool    `json:"hide_label,omitempty"`
	On        bool    `json:"on"`
	Source    string  `json:"source,omitempty"`
	Watts     float64 `json:"watts,omitempty"`
	Voltage   float64 `json:"voltage,omitempty"`
	Current   float64 `json:"current,omitempty"`
	TotalKWh  float64 `json:"total_kwh,omitempty"`
}

type PrinterStatus struct {
	State        PrinterState `json:"state"`
	StateMessage string       `json:"state_message,omitempty"`
	Temps        Temperatures `json:"temps"`
	Job          *JobInfo     `json:"job"`
	Power        []PowerState `json:"power,omitempty"`
	ThumbnailURL string       `json:"thumbnail_url,omitempty"`
	LastUpdated  time.Time    `json:"last_updated"`
}

type RecentFile struct {
	FileName      string          `json:"file_name"`
	Path          string          `json:"path"`
	Origin        string          `json:"origin"`
	UploadedAt    int64           `json:"uploaded_at"`
	SuccessCount  int             `json:"success_count"`
	FailureCount  int             `json:"failure_count"`
	LastPrinted   int64           `json:"last_printed,omitempty"`
	LastSuccess   *bool           `json:"last_success,omitempty"`
	SizeMB        float64         `json:"size_mb,omitempty"`
	ThumbnailPath string          `json:"thumbnail_path,omitempty"`
	Tools         json.RawMessage `json:"tools,omitempty"`
}

type PrinterWithStatus struct {
	Config      PrinterConfig  `json:"config"`
	Status      *PrinterStatus `json:"status"`
	HasCamera   bool           `json:"has_camera"`
	DisplayName string         `json:"display_name"`
	HasWebcam   bool           `json:"has_webcam"`
}

type PrintHistory struct {
	ID             int64   `json:"id"`
	PrinterID      int64   `json:"printer_id"`
	FileName       string  `json:"filename"`
	StartedAt      string  `json:"started_at"`
	CompletedAt    string  `json:"completed_at"`
	DurationSecs   int     `json:"duration_secs"`
	Result         string  `json:"result"`
	FilamentUsedMM float64 `json:"filament_used_mm"`
	// The following are only populated for plugins that support
	// printmeta.Parse (PrusaLink) - zero/empty otherwise.
	LayerHeightMM float64 `json:"layer_height_mm,omitempty"`
	FillDensity   string  `json:"fill_density,omitempty"`
	PrinterModel  string  `json:"printer_model,omitempty"`
	Material      string  `json:"material,omitempty"`
	ToolIndex     int     `json:"tool_index"`
	FilamentUsedG float64 `json:"filament_used_g,omitempty"`
	FilamentCost  float64 `json:"filament_cost,omitempty"`
	EstimatedSecs int     `json:"estimated_secs,omitempty"`
	MaxLayerZ     float64 `json:"max_layer_z,omitempty"`
	ObjectNames   string  `json:"object_names,omitempty"`
	// ToolChanges/Tools are only populated for genuinely multi-tool prints
	// (2+ tools with non-zero usage) - Tools embeds printmeta.ToolUsage
	// JSON directly (a real nested array in the API response, not a string
	// to re-parse client-side).
	ToolChanges int             `json:"tool_changes,omitempty"`
	Tools       json.RawMessage `json:"tools,omitempty"`
	// Thumbnail/ThumbnailContentType are stored directly on this row (own
	// copy, not borrowed from file_meta_cache) - a completed print is a
	// permanent record, its thumbnail shouldn't depend on whether the
	// source file still exists on the printer. Never serialized directly
	// (fetched via GET /api/history-thumbnail/{id} instead) - HasThumbnail
	// tells the frontend whether that request is worth making.
	Thumbnail            []byte `json:"-"`
	ThumbnailContentType string `json:"-"`
	HasThumbnail         bool   `json:"has_thumbnail,omitempty"`
}

type User struct {
	ID           int64  `json:"id"`
	Username     string `json:"username"`
	PasswordHash string `json:"-"`
	CreatedAt    string `json:"created_at"`
}

// IngestTarget is a slicer print-host target, pinned to one specific
// printer - a slicer's "Upload" lands there automatically once that printer
// is reachable, "Upload and Print" also powers it on if needed. No
// model-bucket/multi-printer matching; PrinterID is always set.
type IngestTarget struct {
	ID        int64  `json:"id"`
	Model     string `json:"model,omitempty"`
	PrinterID *int64 `json:"printer_id,omitempty"`
	Label     string `json:"label"`
	APIKey    string `json:"api_key"`
	CreatedAt string `json:"created_at"`
}

// IngestJob is a file staged by a slicer against an IngestTarget, en route
// to that target's pinned printer.
type IngestJob struct {
	ID              int64  `json:"id"`
	IngestTargetID  int64  `json:"ingest_target_id"`
	Model           string `json:"model"`
	PinnedPrinterID *int64 `json:"pinned_printer_id,omitempty"`
	Filename        string `json:"filename"`
	FilePath        string `json:"-"`
	PrintAfter      bool   `json:"print_after"`
	SizeBytes       int64  `json:"size_bytes"`
	Status          string `json:"status"`
	Error           string `json:"error,omitempty"`
	TargetPrinterID *int64 `json:"target_printer_id,omitempty"`
	CreatedAt       string `json:"created_at"`
}
