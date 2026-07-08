package models

import "time"

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
	FileName      string  `json:"file_name"`
	Path          string  `json:"path"`
	Origin        string  `json:"origin"`
	UploadedAt    int64   `json:"uploaded_at"`
	SuccessCount  int     `json:"success_count"`
	FailureCount  int     `json:"failure_count"`
	LastPrinted   int64   `json:"last_printed,omitempty"`
	LastSuccess   *bool   `json:"last_success,omitempty"`
	SizeMB        float64 `json:"size_mb,omitempty"`
	ThumbnailPath string  `json:"thumbnail_path,omitempty"`
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
}

type User struct {
	ID           int64  `json:"id"`
	Username     string `json:"username"`
	PasswordHash string `json:"-"`
	CreatedAt    string `json:"created_at"`
}

// IngestTarget is a slicer print-host target. Exactly one of Model or
// PrinterID applies: a model bucket (Model set, PrinterID nil) that a human
// later dispatches to any one printer sharing that model, or a target pinned
// to one specific printer (PrinterID set) with no ambiguity to resolve at
// dispatch time.
type IngestTarget struct {
	ID        int64  `json:"id"`
	Model     string `json:"model,omitempty"`
	PrinterID *int64 `json:"printer_id,omitempty"`
	Label     string `json:"label"`
	APIKey    string `json:"api_key"`
	// AutoDispatchOnPrintNow: when a slicer upload requests Print-After-Upload,
	// skip the staged banner and dispatch immediately. For a PrinterID-pinned
	// target this always applies (no ambiguity). For a Model-bucket target,
	// only when exactly one enabled, non-maintenance printer currently matches
	// - 2+ matches always fall back to the manual banner, since there's no way
	// to auto-pick which physical printer to wake.
	AutoDispatchOnPrintNow bool   `json:"auto_dispatch_on_print_now"`
	CreatedAt              string `json:"created_at"`
}

// IngestJob is a file staged by a slicer against an IngestTarget, awaiting
// dispatch to a specific printer.
type IngestJob struct {
	ID             int64  `json:"id"`
	IngestTargetID int64  `json:"ingest_target_id"`
	Model          string `json:"model"`
	// PinnedPrinterID mirrors the target's PrinterID at staging time (nil for
	// a model-bucket target) - the printer to dispatch to, with no matching
	// needed.
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
