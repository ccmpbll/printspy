package prusalink

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ccmpbll/printspy/digestauth"
	"github.com/ccmpbll/printspy/models"
	"github.com/ccmpbll/printspy/plugin"
)

func init() {
	plugin.Register("prusalink", func(config models.PrinterConfig) plugin.PrinterPlugin {
		return New(config)
	})
}

type Plugin struct {
	config models.PrinterConfig
	client *http.Client

	mu         sync.RWMutex
	cachedName string
	lastJobID  int
}

func New(config models.PrinterConfig) *Plugin {
	if config.Username == "" {
		config.Username = "maker"
	}
	return &Plugin{
		config: config,
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

func (p *Plugin) Type() string { return "prusalink" }

func (p *Plugin) Connect(ctx context.Context) error {
	data, err := p.doGet(ctx, "/api/version")
	if err != nil {
		log.Printf("[prusalink:%s] connection failed: %v", p.config.URL, err)
		return err
	}
	log.Printf("[prusalink:%s] connected: %s", p.config.URL, string(data))

	infoData, err := p.doGet(ctx, "/api/v1/info")
	if err == nil {
		var info struct {
			Hostname string `json:"hostname"`
			Name     string `json:"name"`
		}
		if json.Unmarshal(infoData, &info) == nil {
			name := info.Name
			if name == "" {
				name = info.Hostname
			}
			p.mu.Lock()
			p.cachedName = name
			p.mu.Unlock()
		}
	}

	return nil
}

func (p *Plugin) GetStatus(ctx context.Context) (*models.PrinterStatus, error) {
	status := &models.PrinterStatus{
		LastUpdated: time.Now(),
	}

	statusData, statusCode, err := p.doGetRaw(ctx, "/api/v1/status")
	if err != nil {
		log.Printf("[prusalink:%s] failed to get status: %v", p.config.URL, err)
		status.State = models.StateOffline
		status.StateMessage = "Unable to reach PrusaLink"
		return status, nil
	}

	if statusCode != http.StatusOK {
		status.State = models.StateOffline
		status.StateMessage = fmt.Sprintf("PrusaLink returned %d", statusCode)
		return status, nil
	}

	var sr statusResponse
	if err := json.Unmarshal(statusData, &sr); err != nil {
		status.State = models.StateOffline
		status.StateMessage = "Invalid response from printer"
		return status, nil
	}

	status.State = mapState(sr.Printer.State)
	if status.State == models.StateError {
		status.StateMessage = sr.Printer.State
	}

	status.Temps = models.Temperatures{
		HotendActual: sr.Printer.TempNozzle,
		HotendTarget: sr.Printer.TargetNozzle,
		BedActual:    sr.Printer.TempBed,
		BedTarget:    sr.Printer.TargetBed,
	}

	jobData, jobStatus, err := p.doGetRaw(ctx, "/api/v1/job")
	if err == nil && jobStatus == http.StatusOK {
		var jr jobResponse
		if json.Unmarshal(jobData, &jr) == nil && jr.File.DisplayName != "" {
			status.Job = &models.JobInfo{
				FileName:       jr.File.DisplayName,
				Progress:       jr.Progress,
				ElapsedSecs:    jr.TimePrinting,
				RemainingSecs:  jr.TimeRemaining,
				EstimatedTotal: jr.TimePrinting + jr.TimeRemaining,
			}

			p.mu.Lock()
			p.lastJobID = jr.ID
			p.mu.Unlock()

			if jr.File.Refs.Thumbnail != "" {
				base := strings.TrimRight(p.config.URL, "/")
				status.ThumbnailURL = base + jr.File.Refs.Thumbnail
			}
		}
	}

	return status, nil
}

func (p *Plugin) GetWebcamURL() string {
	return ""
}

func (p *Plugin) GetSnapshotURL() string {
	base := strings.TrimRight(p.config.URL, "/")
	return base + "/api/v1/cameras/snap"
}

func (p *Plugin) GetPrinterName(ctx context.Context) string {
	p.mu.RLock()
	name := p.cachedName
	p.mu.RUnlock()

	if name != "" {
		return name
	}

	infoData, err := p.doGet(ctx, "/api/v1/info")
	if err != nil {
		return ""
	}
	var info struct {
		Hostname string `json:"hostname"`
		Name     string `json:"name"`
	}
	if json.Unmarshal(infoData, &info) == nil {
		name = info.Name
		if name == "" {
			name = info.Hostname
		}
		p.mu.Lock()
		p.cachedName = name
		p.mu.Unlock()
	}
	return name
}

func (p *Plugin) GetThumbnailURL(ctx context.Context) string {
	jobData, statusCode, err := p.doGetRaw(ctx, "/api/v1/job")
	if err != nil || statusCode != http.StatusOK {
		return ""
	}
	var jr jobResponse
	if json.Unmarshal(jobData, &jr) != nil || jr.File.Refs.Thumbnail == "" {
		return ""
	}
	base := strings.TrimRight(p.config.URL, "/")
	return base + jr.File.Refs.Thumbnail
}

func (p *Plugin) SetPowerState(ctx context.Context, plugID string, on bool) error {
	return fmt.Errorf("power control not supported for PrusaLink")
}

func (p *Plugin) GetRecentFiles(ctx context.Context, limit int) ([]models.RecentFile, error) {
	data, err := p.doGet(ctx, "/api/v1/files/local")
	if err != nil {
		return nil, err
	}

	var resp struct {
		Children []prusalinkFile `json:"children"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		var files []prusalinkFile
		if err := json.Unmarshal(data, &files); err != nil {
			return nil, err
		}
		resp.Children = files
	}

	var result []models.RecentFile
	collectFiles(resp.Children, &result)

	sort.Slice(result, func(i, j int) bool {
		return result[i].UploadedAt > result[j].UploadedAt
	})

	if len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}

type prusalinkFile struct {
	Name        string  `json:"name"`
	DisplayName string  `json:"display_name"`
	Type        string  `json:"type"`
	Size        int64   `json:"size"`
	MTimestamp  int64   `json:"m_timestamp"`
	Refs        struct {
		Thumbnail string `json:"thumbnail"`
		Download  string `json:"download"`
	} `json:"refs"`
	Children []prusalinkFile `json:"children"`
}

func collectFiles(files []prusalinkFile, out *[]models.RecentFile) {
	for _, f := range files {
		if f.Type == "FOLDER" && len(f.Children) > 0 {
			collectFiles(f.Children, out)
			continue
		}
		if f.Type != "PRINT_FILE" {
			continue
		}
		name := f.DisplayName
		if name == "" {
			name = f.Name
		}
		rf := models.RecentFile{
			FileName:      name,
			Path:          f.Name,
			Origin:        "local",
			UploadedAt:    f.MTimestamp,
			SizeMB:        float64(f.Size) / (1024 * 1024),
			ThumbnailPath: f.Refs.Thumbnail,
		}
		*out = append(*out, rf)
	}
}

func (p *Plugin) StartPrint(ctx context.Context, location, path string) error {
	_, err := p.doPost(ctx, "/api/v1/files/"+location+"/"+path, nil)
	return err
}

func (p *Plugin) PausePrint(ctx context.Context) error {
	p.mu.RLock()
	jobID := p.lastJobID
	p.mu.RUnlock()
	if jobID == 0 {
		return fmt.Errorf("no active job")
	}
	return p.doPut(ctx, fmt.Sprintf("/api/v1/job/%d/pause", jobID))
}

func (p *Plugin) ResumePrint(ctx context.Context) error {
	p.mu.RLock()
	jobID := p.lastJobID
	p.mu.RUnlock()
	if jobID == 0 {
		return fmt.Errorf("no active job")
	}
	return p.doPut(ctx, fmt.Sprintf("/api/v1/job/%d/resume", jobID))
}

func (p *Plugin) CancelPrint(ctx context.Context) error {
	p.mu.RLock()
	jobID := p.lastJobID
	p.mu.RUnlock()
	if jobID == 0 {
		return fmt.Errorf("no active job")
	}
	return p.doDelete(ctx, fmt.Sprintf("/api/v1/job/%d", jobID))
}

// HTTP helpers with Digest auth

func (p *Plugin) doGet(ctx context.Context, path string) ([]byte, error) {
	data, statusCode, err := p.doGetRaw(ctx, path)
	if err != nil {
		return nil, err
	}
	if statusCode != http.StatusOK {
		return nil, fmt.Errorf("prusalink API returned %d", statusCode)
	}
	return data, nil
}

func (p *Plugin) doGetRaw(ctx context.Context, path string) ([]byte, int, error) {
	url := strings.TrimRight(p.config.URL, "/") + path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, err
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		authHeader := resp.Header.Get("WWW-Authenticate")
		io.ReadAll(resp.Body)
		resp.Body.Close()

		req2, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, 0, err
		}
		digestAuth := digestauth.BuildHeader(p.config.Username, p.config.APIKey, http.MethodGet, path, authHeader)
		req2.Header.Set("Authorization", digestAuth)

		resp2, err := p.client.Do(req2)
		if err != nil {
			return nil, 0, err
		}
		defer resp2.Body.Close()

		data, err := io.ReadAll(resp2.Body)
		if err != nil {
			return nil, resp2.StatusCode, err
		}
		return data, resp2.StatusCode, nil
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return data, resp.StatusCode, nil
}

func (p *Plugin) doPost(ctx context.Context, path string, body any) ([]byte, error) {
	return p.doMutate(ctx, http.MethodPost, path, body)
}

func (p *Plugin) doPut(ctx context.Context, path string) error {
	_, err := p.doMutate(ctx, http.MethodPut, path, nil)
	return err
}

func (p *Plugin) doDelete(ctx context.Context, path string) error {
	_, err := p.doMutate(ctx, http.MethodDelete, path, nil)
	return err
}

func (p *Plugin) doMutate(ctx context.Context, method, path string, body any) ([]byte, error) {
	url := strings.TrimRight(p.config.URL, "/") + path

	var bodyReader io.Reader
	if body != nil {
		jsonBody, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		bodyReader = strings.NewReader(string(jsonBody))
	}

	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		authHeader := resp.Header.Get("WWW-Authenticate")
		io.ReadAll(resp.Body)
		resp.Body.Close()

		var bodyReader2 io.Reader
		if body != nil {
			jsonBody, _ := json.Marshal(body)
			bodyReader2 = strings.NewReader(string(jsonBody))
		}

		req2, err := http.NewRequestWithContext(ctx, method, url, bodyReader2)
		if err != nil {
			return nil, err
		}
		if body != nil {
			req2.Header.Set("Content-Type", "application/json")
		}
		digestAuth := digestauth.BuildHeader(p.config.Username, p.config.APIKey, method, path, authHeader)
		req2.Header.Set("Authorization", digestAuth)

		resp2, err := p.client.Do(req2)
		if err != nil {
			return nil, err
		}
		defer resp2.Body.Close()

		data, err := io.ReadAll(resp2.Body)
		if err != nil {
			return nil, err
		}
		if resp2.StatusCode < 200 || resp2.StatusCode >= 300 {
			return nil, fmt.Errorf("prusalink API returned %d: %s", resp2.StatusCode, string(data))
		}
		return data, nil
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("prusalink API returned %d: %s", resp.StatusCode, string(data))
	}
	return data, nil
}

// State mapping

func mapState(state string) models.PrinterState {
	s := strings.ToUpper(state)
	switch {
	case s == "PRINTING":
		return models.StatePrinting
	case s == "PAUSED":
		return models.StatePaused
	case s == "FINISHED", s == "STOPPED", s == "IDLE", s == "READY":
		return models.StateIdle
	case strings.Contains(s, "ERROR"), strings.Contains(s, "ATTENTION"):
		return models.StateError
	case s == "BUSY":
		return models.StatePrinting
	default:
		return models.StateIdle
	}
}

// PrusaLink API response types

type statusResponse struct {
	Printer struct {
		State       string  `json:"state"`
		TempNozzle  float64 `json:"temp_nozzle"`
		TargetNozzle float64 `json:"target_nozzle"`
		TempBed     float64 `json:"temp_bed"`
		TargetBed   float64 `json:"target_bed"`
	} `json:"printer"`
	Job *struct {
		ID       int     `json:"id"`
		Progress float64 `json:"progress"`
	} `json:"job"`
}

type jobResponse struct {
	ID            int     `json:"id"`
	State         string  `json:"state"`
	Progress      float64 `json:"progress"`
	TimePrinting  int     `json:"time_printing"`
	TimeRemaining int     `json:"time_remaining"`
	File          struct {
		DisplayName string `json:"display_name"`
		Name        string `json:"name"`
		Size        int64  `json:"size"`
		Refs        struct {
			Thumbnail string `json:"thumbnail"`
			Download  string `json:"download"`
			Icon      string `json:"icon"`
		} `json:"refs"`
	} `json:"file"`
}
