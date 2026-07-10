package prusalink

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ccmpbll/printspy/digestauth"
	"github.com/ccmpbll/printspy/models"
	"github.com/ccmpbll/printspy/netguard"
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
	// uploadClient has a much longer timeout than client - status/job polls
	// are small JSON and should fail fast to detect an offline printer
	// quickly, but a real gcode file can be several MB and take well past
	// 10s to transfer over wifi to an embedded device.
	uploadClient *http.Client

	mu         sync.RWMutex
	cachedName string
	lastJobID  int
}

func New(config models.PrinterConfig) *Plugin {
	if config.Username == "" {
		config.Username = "maker"
	}
	return &Plugin{
		config:       config,
		client:       &http.Client{Timeout: 10 * time.Second, Transport: netguard.Transport()},
		uploadClient: &http.Client{Timeout: 5 * time.Minute, Transport: netguard.Transport()},
	}
}

func (p *Plugin) Type() string        { return "prusalink" }
func (p *Plugin) DisplayName() string { return "PrusaLink" }

// AuthenticatedDo tries HTTP Basic first (fast path for callers that don't
// care which scheme wins) and falls back to Digest if the printer demands
// it via a 401 challenge - the same dance doGetRaw/doMutate do internally,
// exposed generically so callers outside this package (proxy handlers) can
// fetch arbitrary printer resources without knowing PrusaLink's auth
// scheme at all.
func (p *Plugin) AuthenticatedDo(client *http.Client, req *http.Request) (*http.Response, error) {
	req.SetBasicAuth(p.config.Username, p.config.APIKey)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusUnauthorized {
		return resp, nil
	}

	authHeader := resp.Header.Get("WWW-Authenticate")
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	req2 := req.Clone(req.Context())
	req2.Header.Set("Authorization", digestauth.BuildHeader(p.config.Username, p.config.APIKey, req.Method, req.URL.RequestURI(), authHeader))
	return client.Do(req2)
}

// KeepaliveHost implements plugin.Keepalive - some PrusaLink printers' wifi
// interfaces have been observed dropping off the network after idle
// periods, so the poller ICMP-pings them independently of status polling.
func (p *Plugin) KeepaliveHost() (string, bool) {
	u, err := url.Parse(p.config.URL)
	if err != nil || u.Hostname() == "" {
		return "", false
	}
	return u.Hostname(), true
}

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
	if status.State == models.StateError || status.State == models.StateAttention {
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
				FilePath:       jr.File.Refs.Download,
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
	var result []models.RecentFile
	var lastErr error

	// USB is only present when a drive is actually plugged in - a failed
	// fetch there isn't fatal, just means nothing to list from that storage.
	for _, storage := range []string{"local", "usb"} {
		data, err := p.doGet(ctx, "/api/v1/files/"+storage)
		if err != nil {
			lastErr = err
			continue
		}

		var resp struct {
			Children []prusalinkFile `json:"children"`
		}
		if err := json.Unmarshal(data, &resp); err != nil {
			var files []prusalinkFile
			if err := json.Unmarshal(data, &files); err != nil {
				lastErr = err
				continue
			}
			resp.Children = files
		}
		collectFiles(resp.Children, storage, &result)
	}

	if len(result) == 0 && lastErr != nil {
		return nil, lastErr
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].UploadedAt > result[j].UploadedAt
	})

	if limit > 0 && len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}

type prusalinkFile struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Type        string `json:"type"`
	Size        int64  `json:"size"`
	MTimestamp  int64  `json:"m_timestamp"`
	Refs        struct {
		Thumbnail string `json:"thumbnail"`
		Download  string `json:"download"`
	} `json:"refs"`
	Children []prusalinkFile `json:"children"`
}

func collectFiles(files []prusalinkFile, origin string, out *[]models.RecentFile) {
	for _, f := range files {
		if f.Type == "FOLDER" && len(f.Children) > 0 {
			collectFiles(f.Children, origin, out)
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
			Origin:        origin,
			UploadedAt:    f.MTimestamp,
			SizeMB:        float64(f.Size) / (1024 * 1024),
			ThumbnailPath: f.Refs.Thumbnail,
		}
		*out = append(*out, rf)
	}
}

// metadataRangeBytes caps a Range request for a .bgcode file's metadata -
// generously past any real Printer/Print Metadata + thumbnail block sizes
// seen so far (tens of KB), comfortably before the multi-MB gcode body that
// always follows them per the format's fixed block ordering.
const metadataRangeBytes = 2 * 1024 * 1024

// DownloadFileForMetadata fetches path (a "refs.download"-style ref, e.g.
// "/usb/SPOOLD~1.BGC") for print-metadata extraction. A plain .gcode file's
// metadata lives in trailing comments, so it needs the whole file; a
// .bgcode file's metadata blocks always precede the gcode body, so only the
// leading bytes are requested via HTTP Range - if the printer doesn't honor
// Range it just serves the full file with a 200 instead of 206, and this
// still works, only slower.
func (p *Plugin) DownloadFileForMetadata(ctx context.Context, path, displayName string) ([]byte, error) {
	if strings.HasSuffix(strings.ToLower(displayName), ".bgcode") {
		return p.downloadFile(ctx, path, metadataRangeBytes)
	}
	return p.downloadFile(ctx, path, 0)
}

func (p *Plugin) downloadFile(ctx context.Context, path string, rangeBytes int) ([]byte, error) {
	url := strings.TrimRight(p.config.URL, "/") + path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if rangeBytes > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=0-%d", rangeBytes-1))
	}

	resp, err := p.AuthenticatedDo(p.uploadClient, req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		io.Copy(io.Discard, resp.Body)
		return nil, fmt.Errorf("prusalink returned %d for %s", resp.StatusCode, path)
	}
	return io.ReadAll(resp.Body)
}

func (p *Plugin) UploadFile(ctx context.Context, storage, path string, data []byte, printAfter bool) error {
	return p.doUpload(ctx, "/api/v1/files/"+storage+"/"+path, data, printAfter)
}

func (p *Plugin) DeleteFile(ctx context.Context, storage, path string) error {
	return p.doDelete(ctx, "/api/v1/files/"+storage+"/"+path)
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
		digestAuth := digestauth.BuildHeader(p.config.Username, p.config.APIKey, http.MethodGet, req2.URL.RequestURI(), authHeader)
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
		digestAuth := digestauth.BuildHeader(p.config.Username, p.config.APIKey, method, req2.URL.RequestURI(), authHeader)
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

// uploadContentType returns the Content-Type PrusaLink's firmware expects
// for a given filename - it validates the upload against this and rejects
// unrecognized values with 403 regardless of auth. Confirmed against a real
// upload captured from PrusaLink's own web UI (.bgcode -> application/gcode+binary).
func uploadContentType(path string) string {
	if strings.HasSuffix(strings.ToLower(path), ".bgcode") {
		return "application/gcode+binary"
	}
	return "text/x.gcode"
}

func (p *Plugin) doUpload(ctx context.Context, path string, data []byte, printAfter bool) error {
	base := strings.TrimRight(p.config.URL, "/")
	url := base + path

	setHeaders := func(req *http.Request) {
		req.Header.Set("Content-Type", uploadContentType(path))
		req.Header.Set("Overwrite", "?1")
		if printAfter {
			req.Header.Set("Print-After-Upload", "?1")
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Origin", base)
		req.Header.Set("Referer", base+"/")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, bytes.NewReader(data))
	if err != nil {
		return err
	}
	setHeaders(req)

	resp, err := p.uploadClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		authHeader := resp.Header.Get("WWW-Authenticate")
		io.ReadAll(resp.Body)
		resp.Body.Close()

		req2, err := http.NewRequestWithContext(ctx, http.MethodPut, url, bytes.NewReader(data))
		if err != nil {
			return err
		}
		setHeaders(req2)
		digestAuth := digestauth.BuildHeader(p.config.Username, p.config.APIKey, http.MethodPut, req2.URL.RequestURI(), authHeader)
		req2.Header.Set("Authorization", digestAuth)

		resp2, err := p.uploadClient.Do(req2)
		if err != nil {
			return err
		}
		defer resp2.Body.Close()

		body, err := io.ReadAll(resp2.Body)
		if err != nil {
			return err
		}
		if resp2.StatusCode < 200 || resp2.StatusCode >= 300 {
			return fmt.Errorf("prusalink API returned %d: %s", resp2.StatusCode, string(body))
		}
		return nil
	}

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("prusalink API returned %d: %s", resp.StatusCode, string(body))
	}
	return nil
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
	case strings.Contains(s, "ATTENTION"):
		// ATTENTION covers non-fatal, needs-user-input conditions (filament
		// runout, MMU prompts, etc) - distinct from a real ERROR state.
		return models.StateAttention
	case strings.Contains(s, "ERROR"):
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
		State        string  `json:"state"`
		TempNozzle   float64 `json:"temp_nozzle"`
		TargetNozzle float64 `json:"target_nozzle"`
		TempBed      float64 `json:"temp_bed"`
		TargetBed    float64 `json:"target_bed"`
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
