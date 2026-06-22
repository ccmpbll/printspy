package models

import "time"

type PrinterState string

const (
	StateIdle     PrinterState = "idle"
	StatePrinting PrinterState = "printing"
	StatePaused   PrinterState = "paused"
	StateError    PrinterState = "error"
	StateOffline  PrinterState = "offline"
)

type PrinterConfig struct {
	ID           int64  `json:"id" yaml:"-"`
	Name         string `json:"name" yaml:"name"`
	Type         string `json:"type" yaml:"type"`
	URL          string `json:"url" yaml:"url"`
	APIKey       string `json:"api_key" yaml:"api_key"`
	Enabled      bool   `json:"enabled" yaml:"enabled"`
	PollInterval int    `json:"poll_interval" yaml:"poll_interval"`
	SortOrder    int    `json:"sort_order" yaml:"-"`
	CreatedAt    string `json:"created_at" yaml:"-"`
	UpdatedAt    string `json:"updated_at" yaml:"-"`
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

type PrinterStatus struct {
	State       PrinterState `json:"state"`
	Temps       Temperatures `json:"temps"`
	Job         *JobInfo     `json:"job"`
	LastUpdated time.Time    `json:"last_updated"`
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
