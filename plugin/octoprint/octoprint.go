package octoprint

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

	"github.com/ccmpbll/printspy/models"
	"github.com/ccmpbll/printspy/plugin"
)

func init() {
	plugin.Register("octoprint", func(config models.PrinterConfig) plugin.PrinterPlugin {
		return New(config)
	})
}

type cachedSettings struct {
	streamURL   string
	snapshotURL string
	printerName string
	fetched     bool
}

type pluginCache struct {
	installed   map[string]bool
	lastFetched time.Time
}

type Plugin struct {
	config models.PrinterConfig
	client *http.Client

	settingsMu sync.RWMutex
	settings   cachedSettings

	pluginMu sync.RWMutex
	plugins  pluginCache
}

func New(config models.PrinterConfig) *Plugin {
	return &Plugin{
		config: config,
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

func (p *Plugin) Type() string { return "octoprint" }

func (p *Plugin) Connect(ctx context.Context) error {
	data, err := p.doGet(ctx, "/api/version")
	if err != nil {
		log.Printf("[octoprint:%s] connection failed: %v", p.config.URL, err)
		return err
	}
	log.Printf("[octoprint:%s] connected: %s", p.config.URL, string(data))

	p.refreshPlugins(ctx)
	p.fetchSettings(ctx)

	return nil
}

func (p *Plugin) GetStatus(ctx context.Context) (*models.PrinterStatus, error) {
	status := &models.PrinterStatus{
		LastUpdated: time.Now(),
	}

	// Fetch settings if not yet loaded (e.g. printer was offline at startup)
	p.settingsMu.RLock()
	fetched := p.settings.fetched
	p.settingsMu.RUnlock()
	if !fetched {
		p.refreshPlugins(ctx)
		p.fetchSettings(ctx)
	} else {
		p.refreshPlugins(ctx)
	}

	printerData, statusCode, err := p.doGetRaw(ctx, "/api/printer?exclude=sd")
	if err != nil {
		log.Printf("[octoprint:%s] failed to get printer state: %v", p.config.URL, err)
		status.State = models.StateOffline
		status.StateMessage = "Unable to reach OctoPrint"
		return status, nil
	}

	if statusCode == http.StatusConflict {
		status.State = models.StateDisconnected
		status.StateMessage = "Printer not connected to OctoPrint"
		connData, err := p.doGet(ctx, "/api/connection")
		if err == nil {
			var connResp connectionResponse
			if err := json.Unmarshal(connData, &connResp); err == nil && connResp.Current.State != "" {
				status.StateMessage = connResp.Current.State
			}
		}
		return status, nil
	}

	if statusCode != http.StatusOK {
		status.State = models.StateOffline
		status.StateMessage = fmt.Sprintf("OctoPrint returned %d", statusCode)
		return status, nil
	}

	var printerResp printerResponse
	if err := json.Unmarshal(printerData, &printerResp); err != nil {
		status.State = models.StateOffline
		status.StateMessage = "Invalid response from printer"
		return status, nil
	}

	status.State = mapState(printerResp.State.Flags)
	if status.State == models.StateError {
		status.StateMessage = printerResp.State.Text
	}
	status.Temps = models.Temperatures{
		HotendActual: printerResp.Temperature.Tool0.Actual,
		HotendTarget: printerResp.Temperature.Tool0.Target,
		BedActual:    printerResp.Temperature.Bed.Actual,
		BedTarget:    printerResp.Temperature.Bed.Target,
	}
	if printerResp.Temperature.Chamber.Actual != 0 || printerResp.Temperature.Chamber.Target != 0 {
		status.Temps.HasChamber = true
		status.Temps.ChamberActual = printerResp.Temperature.Chamber.Actual
		status.Temps.ChamberTarget = printerResp.Temperature.Chamber.Target
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

	// Fetch layer info from DisplayLayerProgress plugin if installed
	if status.Job != nil && p.hasPlugin("DisplayLayerProgress") {
		layerData, err := p.doGet(ctx, "/plugin/DisplayLayerProgress/values")
		if err == nil {
			var layerResp layerProgressResponse
			if err := json.Unmarshal(layerData, &layerResp); err == nil {
				if current, err := strconv.Atoi(layerResp.Layer.Current); err == nil {
					status.Job.CurrentLayer = current
				}
				if total, err := strconv.Atoi(layerResp.Layer.Total); err == nil {
					status.Job.TotalLayers = total
				}
			}
		}
	}

	return status, nil
}

func (p *Plugin) fetchSettings(ctx context.Context) {
	base := strings.TrimRight(p.config.URL, "/")

	data, err := p.doGet(ctx, "/api/settings")
	if err != nil {
		log.Printf("[octoprint:%s] failed to fetch settings for webcam config: %v", p.config.URL, err)
		p.settingsMu.Lock()
		p.settings = cachedSettings{
			streamURL:   base + "/webcam/?action=stream",
			snapshotURL: base + "/webcam/?action=snapshot",
			fetched:     true,
		}
		p.settingsMu.Unlock()
		return
	}

	var settings settingsResponse
	if err := json.Unmarshal(data, &settings); err != nil {
		log.Printf("[octoprint:%s] failed to parse settings: %v", p.config.URL, err)
		p.settingsMu.Lock()
		p.settings = cachedSettings{
			streamURL:   base + "/webcam/?action=stream",
			snapshotURL: base + "/webcam/?action=snapshot",
			fetched:     true,
		}
		p.settingsMu.Unlock()
		return
	}

	var streamURL string

	// Check plugin-specific camera config based on installed plugins
	if p.hasPlugin("camera-streamer-control") {
		// camera-streamer plugin stores URL in its own settings
		if cs := settings.Plugins.CameraStreamer.StreamURL; cs != "" {
			streamURL = p.resolveURL(cs)
			log.Printf("[octoprint:%s] using camera-streamer plugin URL: %s", p.config.URL, streamURL)
		}
	}

	if streamURL == "" && p.hasPlugin("classicwebcam") {
		if cw := settings.Plugins.ClassicWebcam.Stream; cw != "" {
			streamURL = p.resolveURL(cw)
			log.Printf("[octoprint:%s] using classic webcam plugin URL: %s", p.config.URL, streamURL)
		}
	}

	// Fall back to top-level webcam settings
	if streamURL == "" {
		streamURL = settings.Webcam.StreamURL
	}

	// Check the newer multi-webcam config
	if streamURL == "" && len(settings.Webcam.Webcams) > 0 {
		wc := settings.Webcam.Webcams[0]
		streamURL = wc.Extras.StreamURL
		if streamURL == "" {
			streamURL = wc.StreamURL
		}
	}

	streamURL = p.resolveURL(streamURL)

	if streamURL == "" {
		streamURL = base + "/webcam/?action=stream"
	}

	// Derive snapshot URL from stream URL rather than trusting OctoPrint's
	// snapshot setting, which is often misconfigured with localhost/127.0.0.1
	snapshotURL := deriveSnapshotURL(streamURL)

	printerName := settings.Appearance.Name
	log.Printf("[octoprint:%s] settings: name=%q stream=%s snapshot=%s", p.config.URL, printerName, streamURL, snapshotURL)

	p.settingsMu.Lock()
	p.settings = cachedSettings{
		streamURL:   streamURL,
		snapshotURL: snapshotURL,
		printerName: printerName,
		fetched:     true,
	}
	p.settingsMu.Unlock()
}

const pluginRefreshInterval = 5 * time.Minute

func (p *Plugin) refreshPlugins(ctx context.Context) {
	p.pluginMu.RLock()
	if !p.plugins.lastFetched.IsZero() && time.Since(p.plugins.lastFetched) < pluginRefreshInterval {
		p.pluginMu.RUnlock()
		return
	}
	p.pluginMu.RUnlock()

	data, err := p.doGet(ctx, "/plugin/pluginmanager/plugins")
	if err != nil {
		log.Printf("[octoprint:%s] failed to fetch plugin list: %v", p.config.URL, err)
		return
	}

	var resp pluginManagerResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		log.Printf("[octoprint:%s] failed to parse plugin list: %v", p.config.URL, err)
		return
	}

	installed := make(map[string]bool)
	var names []string
	for _, pl := range resp.Plugins {
		if pl.Enabled {
			installed[pl.Key] = true
			names = append(names, pl.Key)
		}
	}

	p.pluginMu.Lock()
	p.plugins = pluginCache{installed: installed, lastFetched: time.Now()}
	p.pluginMu.Unlock()

	log.Printf("[octoprint:%s] plugins (%d): %s", p.config.URL, len(names), strings.Join(names, ", "))
}

func (p *Plugin) hasPlugin(key string) bool {
	p.pluginMu.RLock()
	defer p.pluginMu.RUnlock()
	return p.plugins.installed[key]
}

func (p *Plugin) resolveURL(rawURL string) string {
	if rawURL == "" {
		return ""
	}
	if strings.HasPrefix(rawURL, "/") {
		base := strings.TrimRight(p.config.URL, "/")
		return base + rawURL
	}
	return rawURL
}

// deriveSnapshotURL converts a known-working stream URL into its snapshot equivalent.
// mjpg-streamer: ?action=stream -> ?action=snapshot
// camera-streamer: /stream -> /snapshot
func deriveSnapshotURL(streamURL string) string {
	if strings.Contains(streamURL, "?action=stream") {
		return strings.Replace(streamURL, "?action=stream", "?action=snapshot", 1)
	}
	if strings.HasSuffix(streamURL, "/stream") {
		return strings.TrimSuffix(streamURL, "/stream") + "/snapshot"
	}
	// Unknown format — try appending snapshot as a sibling path
	parsed, err := url.Parse(streamURL)
	if err != nil {
		return strings.TrimSuffix(streamURL, "/") + "/?action=snapshot"
	}
	parts := strings.Split(parsed.Path, "/")
	if len(parts) > 0 {
		parts[len(parts)-1] = "snapshot"
	}
	parsed.Path = strings.Join(parts, "/")
	parsed.RawQuery = ""
	return parsed.String()
}

func (p *Plugin) GetWebcamURL() string {
	p.settingsMu.RLock()
	defer p.settingsMu.RUnlock()
	if p.settings.fetched {
		return p.settings.streamURL
	}
	base := strings.TrimRight(p.config.URL, "/")
	return base + "/webcam/?action=stream"
}

func (p *Plugin) GetSnapshotURL() string {
	p.settingsMu.RLock()
	defer p.settingsMu.RUnlock()
	if p.settings.fetched {
		return p.settings.snapshotURL
	}
	base := strings.TrimRight(p.config.URL, "/")
	return base + "/webcam/?action=snapshot"
}

func (p *Plugin) GetPrinterName(ctx context.Context) string {
	p.settingsMu.RLock()
	fetched := p.settings.fetched
	name := p.settings.printerName
	p.settingsMu.RUnlock()

	if !fetched {
		p.fetchSettings(ctx)
		p.settingsMu.RLock()
		name = p.settings.printerName
		p.settingsMu.RUnlock()
	}
	return name
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
	data, statusCode, err := p.doGetRaw(ctx, path)
	if err != nil {
		return nil, err
	}
	if statusCode != http.StatusOK {
		return nil, fmt.Errorf("octoprint API returned %d", statusCode)
	}
	return data, nil
}

func (p *Plugin) doGetRaw(ctx context.Context, path string) ([]byte, int, error) {
	url := strings.TrimRight(p.config.URL, "/") + path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("X-Api-Key", p.config.APIKey)

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return data, resp.StatusCode, nil
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
		Text  string     `json:"text"`
		Flags stateFlags `json:"flags"`
	} `json:"state"`
	Temperature struct {
		Tool0   tempData `json:"tool0"`
		Bed     tempData `json:"bed"`
		Chamber tempData `json:"chamber"`
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

type settingsResponse struct {
	Appearance struct {
		Name string `json:"name"`
	} `json:"appearance"`
	Webcam struct {
		StreamURL   string `json:"streamUrl"`
		SnapshotURL string `json:"snapshotUrl"`
		Webcams     []struct {
			Name        string `json:"name"`
			StreamURL   string `json:"streamUrl"`
			SnapshotURL string `json:"snapshotUrl"`
			Extras      struct {
				StreamURL   string `json:"streamUrl"`
				SnapshotURL string `json:"snapshotUrl"`
			} `json:"extras"`
		} `json:"webcams"`
	} `json:"webcam"`
	Plugins struct {
		ClassicWebcam struct {
			Stream   string `json:"stream"`
			Snapshot string `json:"snapshot"`
		} `json:"classicwebcam"`
		CameraStreamer struct {
			StreamURL string `json:"streamUrl"`
		} `json:"camera-streamer-control"`
	} `json:"plugins"`
}

type pluginManagerResponse struct {
	Plugins []struct {
		Key     string `json:"key"`
		Name    string `json:"name"`
		Enabled bool   `json:"enabled"`
	} `json:"plugins"`
}

type connectionResponse struct {
	Current struct {
		State string `json:"state"`
	} `json:"current"`
}

type layerProgressResponse struct {
	Layer struct {
		Current string `json:"current"`
		Total   string `json:"total"`
	} `json:"layer"`
	Height struct {
		Current          string `json:"current"`
		CurrentFormatted string `json:"currentFormatted"`
		Total            string `json:"total"`
		TotalFormatted   string `json:"totalFormatted"`
	} `json:"height"`
}
