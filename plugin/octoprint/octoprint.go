package octoprint

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/ccmpbll/printspy/models"
	"github.com/ccmpbll/printspy/plugin"
)

func init() {
	plugin.Register("octoprint", func(config models.PrinterConfig) plugin.PrinterPlugin {
		return New(config)
	})
}

type Plugin struct {
	config models.PrinterConfig
	client *http.Client
}

func New(config models.PrinterConfig) *Plugin {
	return &Plugin{
		config: config,
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

func (p *Plugin) Type() string { return "octoprint" }

func (p *Plugin) Connect(ctx context.Context) error {
	_, err := p.doGet(ctx, "/api/version")
	return err
}

func (p *Plugin) GetStatus(ctx context.Context) (*models.PrinterStatus, error) {
	status := &models.PrinterStatus{
		LastUpdated: time.Now(),
	}

	printerData, err := p.doGet(ctx, "/api/printer?exclude=sd")
	if err != nil {
		status.State = models.StateOffline
		return status, nil
	}

	var printerResp printerResponse
	if err := json.Unmarshal(printerData, &printerResp); err != nil {
		status.State = models.StateOffline
		return status, nil
	}

	status.State = mapState(printerResp.State.Flags)
	status.Temps = models.Temperatures{
		HotendActual: printerResp.Temperature.Tool0.Actual,
		HotendTarget: printerResp.Temperature.Tool0.Target,
		BedActual:    printerResp.Temperature.Bed.Actual,
		BedTarget:    printerResp.Temperature.Bed.Target,
	}

	jobData, err := p.doGet(ctx, "/api/job")
	if err == nil {
		var jobResp jobResponse
		if err := json.Unmarshal(jobData, &jobResp); err == nil && jobResp.Job.File.Name != "" {
			status.Job = &models.JobInfo{
				FileName:       jobResp.Job.File.Name,
				Progress:       jobResp.Progress.Completion,
				ElapsedSecs:    int(jobResp.Progress.PrintTime),
				RemainingSecs:  int(jobResp.Progress.PrintTimeLeft),
				EstimatedTotal: int(jobResp.Job.EstimatedPrintTime),
			}
			if jobResp.Job.Filament.Tool0.Length > 0 {
				status.Job.FilamentUsedMM = jobResp.Job.Filament.Tool0.Length
			}
		}
	}

	return status, nil
}

func (p *Plugin) GetWebcamURL() string {
	base := strings.TrimRight(p.config.URL, "/")
	return base + "/webcam/?action=stream"
}

func (p *Plugin) GetThumbnailURL(ctx context.Context) string {
	jobData, err := p.doGet(ctx, "/api/job")
	if err != nil {
		return ""
	}
	var jobResp jobResponse
	if err := json.Unmarshal(jobData, &jobResp); err != nil {
		return ""
	}

	fileName := jobResp.Job.File.Name
	origin := jobResp.Job.File.Origin
	if fileName == "" || origin == "" {
		return ""
	}

	filePath := jobResp.Job.File.Path
	if filePath == "" {
		filePath = fileName
	}

	fileData, err := p.doGet(ctx, "/api/files/"+origin+"/"+filePath)
	if err != nil {
		return ""
	}

	var fileResp fileResponse
	if err := json.Unmarshal(fileData, &fileResp); err != nil {
		return ""
	}

	if len(fileResp.Thumbnail) > 0 {
		base := strings.TrimRight(p.config.URL, "/")
		return base + "/" + strings.TrimLeft(fileResp.Thumbnail, "/")
	}

	if len(fileResp.Thumbnails) > 0 {
		base := strings.TrimRight(p.config.URL, "/")
		best := fileResp.Thumbnails[len(fileResp.Thumbnails)-1]
		return base + "/" + strings.TrimLeft(best.URL, "/")
	}

	return ""
}

func (p *Plugin) doGet(ctx context.Context, path string) ([]byte, error) {
	url := strings.TrimRight(p.config.URL, "/") + path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Api-Key", p.config.APIKey)

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("octoprint API returned %d", resp.StatusCode)
	}

	return io.ReadAll(resp.Body)
}

func mapState(flags stateFlags) models.PrinterState {
	switch {
	case flags.Printing:
		return models.StatePrinting
	case flags.Pausing || flags.Paused:
		return models.StatePaused
	case flags.Error || flags.ClosedOrError:
		return models.StateError
	case flags.Ready || flags.Operational:
		return models.StateIdle
	default:
		return models.StateOffline
	}
}

// OctoPrint API response types

type printerResponse struct {
	State struct {
		Flags stateFlags `json:"flags"`
	} `json:"state"`
	Temperature struct {
		Tool0 tempData `json:"tool0"`
		Bed   tempData `json:"bed"`
	} `json:"temperature"`
}

type stateFlags struct {
	Operational   bool `json:"operational"`
	Printing      bool `json:"printing"`
	Pausing       bool `json:"pausing"`
	Paused        bool `json:"paused"`
	Cancelling    bool `json:"cancelling"`
	Error         bool `json:"error"`
	Ready         bool `json:"ready"`
	ClosedOrError bool `json:"closedOrError"`
}

type tempData struct {
	Actual float64 `json:"actual"`
	Target float64 `json:"target"`
}

type jobResponse struct {
	Job struct {
		File struct {
			Name   string `json:"name"`
			Path   string `json:"path"`
			Origin string `json:"origin"`
		} `json:"file"`
		EstimatedPrintTime float64 `json:"estimatedPrintTime"`
		Filament           struct {
			Tool0 struct {
				Length float64 `json:"length"`
				Volume float64 `json:"volume"`
			} `json:"tool0"`
		} `json:"filament"`
	} `json:"job"`
	Progress struct {
		Completion    float64 `json:"completion"`
		PrintTime     float64 `json:"printTime"`
		PrintTimeLeft float64 `json:"printTimeLeft"`
	} `json:"progress"`
}

type fileResponse struct {
	Thumbnail  string `json:"thumbnail"`
	Thumbnails []struct {
		URL    string `json:"url"`
		Width  int    `json:"width"`
		Height int    `json:"height"`
	} `json:"thumbnails"`
}
