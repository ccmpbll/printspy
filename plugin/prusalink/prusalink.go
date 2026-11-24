package prusalink

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"log/slog"
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
	// lastUnknownState is the last unmapped printer.state logged, so a
	// new one is logged once per change, not every poll.
	lastUnknownState string
	// challenge is the printer's last Digest challenge. PrusaLink accepts
	// its nonce again (checked on both printers 2026-11-24), so requests
	// are signed up front and a poll needs one round trip per call.
	challenge string
	// nc counts requests signed with challenge (qop=auth needs a new
	// count for each reuse of a nonce); reset when the challenge changes.
	nc int
	// noSnapUntil: cameras/snap answered 404 (no camera), so
	// GetSnapshotURL offers nothing until then.
	noSnapUntil time.Time
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

// buildURL joins the printer's base URL with a request path that may itself
// contain characters needing escaping - notably a literal "%" in a file's
// 8.3-mangled short name (e.g. a file named "apple%20TV..." mangles to
// "APPLE%~7.BGC"). Raw string concatenation breaks here: "%~7" isn't valid
// percent-encoding (must be followed by two hex digits), and
// http.NewRequestWithContext parses the URL and rejects it outright.
// Setting url.URL.Path (the decoded form) and letting String() re-encode it
// escapes a literal "%" to "%25" correctly, same as any other reserved
// character.
func buildURL(base, path string) string {
	u, err := url.Parse(base)
	if err != nil {
		return base + path // fall back to the old behavior rather than fail outright
	}
	u.Path += path
	return u.String()
}

// snapNoCameraFor is how long a cameras/snap 404 is remembered. A camera
// plugged in meanwhile shows up once it runs out (or when the printer is
// edited, which makes a new plugin): accepted, against one wasted request
// per snapshot refresh on the printers without a camera.
const snapNoCameraFor = 10 * time.Minute

// AuthenticatedDo sends req with the printer's Digest auth (see do), so
// callers outside this package (proxy handlers) can fetch printer resources
// without knowing PrusaLink's auth scheme. req must have no body: a retry
// after a 401 re-sends a clone. A 404 from cameras/snap is remembered (see
// GetSnapshotURL).
func (p *Plugin) AuthenticatedDo(client *http.Client, req *http.Request) (*http.Response, error) {
	resp, err := p.do(client, func() (*http.Request, error) { return req.Clone(req.Context()), nil })
	if err == nil && resp.StatusCode == http.StatusNotFound && strings.HasSuffix(req.URL.Path, "/api/v1/cameras/snap") {
		p.mu.Lock()
		p.noSnapUntil = time.Now().Add(snapNoCameraFor)
		p.mu.Unlock()
	}
	return resp, err
}

// do sends the request newReq builds, signed with the saved Digest
// challenge when there is one. A 401 saves the new challenge and retries
// once with a fresh request (newReq is called again, so a body is re-sent
// only then). Without a saved challenge the first request goes unsigned,
// which is also how a printer without auth works.
func (p *Plugin) do(client *http.Client, newReq func() (*http.Request, error)) (*http.Response, error) {
	req, err := newReq()
	if err != nil {
		return nil, err
	}
	if ch, nc := p.nextNC(""); ch != "" {
		p.sign(req, ch, nc)
	}
	resp, err := client.Do(req)
	if err != nil || resp.StatusCode != http.StatusUnauthorized {
		return resp, err
	}
	fresh := digestauth.Challenge(resp.Header)
	if fresh == "" {
		// No Digest challenge to answer: a retry would be signed with an
		// empty or stale one and fail the same way.
		return resp, nil
	}
	readBody(resp.Body)
	resp.Body.Close()

	if req, err = newReq(); err != nil {
		return nil, err
	}
	ch, nc := p.nextNC(fresh)
	p.sign(req, ch, nc)
	return client.Do(req)
}

// nextNC returns the saved challenge and the next nonce count for it. A
// non-empty fresh challenge (from a 401) replaces the saved one first, and
// its count starts again at 1; an empty one leaves the saved one in use.
func (p *Plugin) nextNC(fresh string) (string, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if fresh != "" || p.challenge == "" {
		if fresh != p.challenge {
			p.nc = 0
		}
		p.challenge = fresh
	}
	p.nc++
	return p.challenge, p.nc
}

func (p *Plugin) sign(req *http.Request, challenge string, nc int) {
	req.Header.Set("Authorization", digestauth.BuildHeaderNC(p.config.Username, p.config.APIKey, req.Method, req.URL.RequestURI(), challenge, nc))
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
		// Debug only: the poller logs the offline transition once; this fires every tick.
		slog.Debug("prusalink: failed to get status", "url", p.config.URL, "err", err)
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

	st, known := mapState(sr.Printer.State)
	status.State = st
	if !known {
		// Shown on the card: busy blocks start and power-off, so say why.
		status.StateMessage = "Unknown printer state: " + sr.Printer.State
		p.mu.Lock()
		first := p.lastUnknownState != sr.Printer.State
		p.lastUnknownState = sr.Printer.State
		p.mu.Unlock()
		if first {
			log.Printf("prusalink %s: unknown printer state %q, treating as busy", p.config.URL, sr.Printer.State)
		}
	}
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
				JobState:       jr.State,
				JobID:          jr.ID,
				FileMTimestamp: jr.File.MTimestamp,
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

// debugEndpoints is every fixed-path GET endpoint in PrusaLink's OpenAPI
// spec (prusa3d/Prusa-Link-Web/spec/openapi.yaml) that doesn't need a
// dynamic ID or return binary - dumped raw and unfiltered by DebugDump for
// the Settings debug view. /api/v1/job/{id}, pause/resume/continue, and the
// cameras/{id} sub-resources are mutate-only or need an ID we don't have
// ahead of time; /api/v1/cameras/snap and /{id}/snap return an image, not
// JSON. Everything else the spec documents is here, even endpoints known
// dead on real firmware (cameras, update) - that's the point of a raw dump.
var debugEndpoints = []struct{ key, path string }{
	{"version", "/api/version"},
	{"info", "/api/v1/info"},
	{"status", "/api/v1/status"},
	{"job", "/api/v1/job"},
	{"storage", "/api/v1/storage"},
	{"transfer", "/api/v1/transfer"},
	{"files_usb", "/api/v1/files/usb"},
	{"files_local", "/api/v1/files/local"},
	{"cameras", "/api/v1/cameras"},
	{"update_prusalink", "/api/v1/update/prusalink"},
}

// DebugDump fetches every known PrusaLink endpoint and returns each raw
// response body keyed by name, for the raw-API debug view. A failed or
// non-200 call is captured as an error value under its own key rather than
// aborting the whole dump - e.g. files_local is expected to 404/be empty on
// USB-only setups, and shouldn't blank out the other endpoints.
func (p *Plugin) DebugDump(ctx context.Context) map[string]json.RawMessage {
	out := make(map[string]json.RawMessage, len(debugEndpoints))
	for _, e := range debugEndpoints {
		out[e.key] = p.debugFetch(ctx, e.path)
	}
	return out
}

func (p *Plugin) debugFetch(ctx context.Context, path string) json.RawMessage {
	data, statusCode, err := p.doGetRaw(ctx, path)
	if err != nil {
		msg, _ := json.Marshal(map[string]string{"_error": err.Error()})
		return msg
	}
	if statusCode == http.StatusNoContent || len(data) == 0 {
		return json.RawMessage("null")
	}
	if !json.Valid(data) {
		msg, _ := json.Marshal(map[string]any{"_status": statusCode, "_raw": string(data)})
		return msg
	}
	if statusCode != http.StatusOK {
		msg, _ := json.Marshal(map[string]any{"_status": statusCode, "body": json.RawMessage(data)})
		return msg
	}
	return json.RawMessage(data)
}

func (p *Plugin) GetWebcamURL() string {
	return ""
}

func (p *Plugin) GetSnapshotURL() string {
	p.mu.RLock()
	noSnap := time.Now().Before(p.noSnapUntil)
	p.mu.RUnlock()
	if noSnap {
		return "" // cameras/snap said 404 recently: no camera (see AuthenticatedDo)
	}
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
	url := buildURL(strings.TrimRight(p.config.URL, "/"), path)
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
	if rangeBytes > 0 {
		// A printer that ignores Range serves the whole file; keep only the
		// requested prefix (the parser tolerates a truncated tail).
		return io.ReadAll(io.LimitReader(resp.Body, int64(rangeBytes)))
	}
	return keepGcodeComments(resp.Body)
}

// maxKeptGcodeComments bounds what keepGcodeComments retains.
const maxKeptGcodeComments = 8 << 20

// keepGcodeComments streams a plain-gcode body and keeps only the ';' comment
// lines - the only part printmeta reads (config footer + base64 thumbnails).
// A 200MB+ file therefore costs a few MB, not 200MB. Output stops at
// maxKeptGcodeComments; a line longer than 1MB ends the scan.
func keepGcodeComments(r io.Reader) ([]byte, error) {
	var out bytes.Buffer
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 || line[0] != ';' {
			continue
		}
		if out.Len()+len(line)+1 > maxKeptGcodeComments {
			break
		}
		out.Write(line)
		out.WriteByte('\n')
	}
	if err := sc.Err(); err != nil && err != bufio.ErrTooLong {
		return nil, err
	}
	return out.Bytes(), nil
}

func (p *Plugin) UploadFile(ctx context.Context, storage, path string, data []byte, printAfter bool) (string, int64, error) {
	return p.doUpload(ctx, "/api/v1/files/"+storage+"/"+path, data, printAfter)
}

func (p *Plugin) DeleteFile(ctx context.Context, storage, path string) error {
	return p.doDelete(ctx, "/api/v1/files/"+storage+"/"+path)
}

// DownloadFile fetches the file's raw bytes - GET /api/v1/files/{storage}/{path}
// (the same URL DeleteFile/StartPrint use) returns metadata JSON, not
// content; the actual bytes live at the bare /{storage}/{path} ref, same
// shape as a file's own refs.download (see DownloadFileForMetadata).
func (p *Plugin) DownloadFile(ctx context.Context, storage, path string) ([]byte, error) {
	return p.downloadFile(ctx, "/"+storage+"/"+path, 0)
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
	url := buildURL(strings.TrimRight(p.config.URL, "/"), path)
	resp, err := p.do(p.client, func() (*http.Request, error) {
		return http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	})
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	data, err := readBody(resp.Body)
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
	url := buildURL(strings.TrimRight(p.config.URL, "/"), path)
	var jsonBody []byte
	if body != nil {
		var err error
		if jsonBody, err = json.Marshal(body); err != nil {
			return nil, err
		}
	}
	resp, err := p.do(p.client, func() (*http.Request, error) {
		var bodyReader io.Reader
		if body != nil {
			bodyReader = bytes.NewReader(jsonBody)
		}
		req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
		if err == nil && body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		return req, err
	})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := readBody(resp.Body)
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

// doUpload returns the printer's own resolved short name and timestamp for
// the uploaded file, parsed from the PUT response body (same shape as a
// listing entry - {"name":"COREON~1.BGC","m_timestamp":...,...}) - PrusaLink
// renames on write to an 8.3-mangled short name, different from the path
// this was called with, and every other read path (listings, thumbnails,
// deletes) keys on that real name. Empty/zero on parse failure - the upload
// itself still succeeded, callers just can't cache metadata under a name
// that's guaranteed to match a later listing.
func (p *Plugin) doUpload(ctx context.Context, path string, data []byte, printAfter bool) (string, int64, error) {
	base := strings.TrimRight(p.config.URL, "/")
	url := buildURL(base, path)

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

	resp, err := p.do(p.uploadClient, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, bytes.NewReader(data))
		if err == nil {
			setHeaders(req)
		}
		return req, err
	})
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()
	body, err := readBody(resp.Body)
	if err != nil {
		return "", 0, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", 0, fmt.Errorf("prusalink API returned %d: %s", resp.StatusCode, string(body))
	}
	name, mtime := parseUploadResponse(body)
	return name, mtime, nil
}

func parseUploadResponse(body []byte) (string, int64) {
	var pf prusalinkFile
	if err := json.Unmarshal(body, &pf); err != nil {
		return "", 0
	}
	return pf.Name, pf.MTimestamp
}

// State mapping

// mapState maps PrusaLink's printer.state; known is false for a state
// newer than this mapping.
func mapState(state string) (st models.PrinterState, known bool) {
	s := strings.ToUpper(state)
	switch {
	case s == "PRINTING":
		return models.StatePrinting, true
	case s == "PAUSED":
		return models.StatePaused, true
	case s == "FINISHED", s == "STOPPED", s == "IDLE", s == "READY":
		return models.StateIdle, true
	case strings.Contains(s, "ATTENTION"):
		// ATTENTION covers non-fatal, needs-user-input conditions (filament
		// runout, MMU prompts, etc) - distinct from a real ERROR state.
		return models.StateAttention, true
	case strings.Contains(s, "ERROR"):
		return models.StateError, true
	case s == "BUSY":
		// Filament load/unload, preheat, homing, self-test - not a print
		// job, must stay distinct from StatePrinting (see models.StateBusy).
		return models.StateBusy, true
	default:
		// Busy, not idle, so it neither starts the auto-off clock nor
		// ends a print in progress.
		return models.StateBusy, false
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
		MTimestamp  int64  `json:"m_timestamp"`
		Refs        struct {
			Thumbnail string `json:"thumbnail"`
			Download  string `json:"download"`
			Icon      string `json:"icon"`
		} `json:"refs"`
	} `json:"file"`
}

// maxBody caps any single API response read. Upstream printers are
// semi-trusted LAN devices; an unbounded io.ReadAll on the poll path lets a
// misbehaving or hostile one exhaust memory.
const maxBody = 16 << 20

func readBody(r io.Reader) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, maxBody+1))
	if err == nil && len(b) > maxBody {
		err = fmt.Errorf("response exceeds %d bytes", maxBody)
	}
	return b, err
}
