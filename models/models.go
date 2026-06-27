package models

import "time"

type PrinterState string

const (
	StateIdle         PrinterState = "idle"
	StatePrinting     PrinterState = "printing"
	StatePaused       PrinterState = "paused"
	StateError        PrinterState = "error"
	StateOffline      PrinterState = "offline"
	StateDisconnected PrinterState = "disconnected"
)

type PrinterConfig struct {
	ID           int64  `json:"id"`
	Name         string `json:"name"`
	Type         string `json:"type"`
	URL          string `json:"url"`
	APIKey       string `json:"-"`
	Enabled      bool   `json:"enabled"`
	PollInterval int    `json:"poll_interval"`
	SortOrder    int    `json:"sort_order"`
	CreatedAt    string `json:"created_at"`
	UpdatedAt    string `json:"updated_at"`
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
	Available bool    `json:"available"`
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
	Power        *PowerState  `json:"power,omitempty"`
	ThumbnailURL string       `json:"thumbnail_url,omitempty"`
	LastUpdated  time.Time    `json:"last_updated"`
}

type PrinterWithStatus struct {
	Config PrinterConfig  `json:"config"`
	Status *PrinterStatus `json:"status"`
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
