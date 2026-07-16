package poller

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ccmpbll/printspy/db"
	"github.com/ccmpbll/printspy/models"
	"github.com/ccmpbll/printspy/mqttplug"
	"github.com/ccmpbll/printspy/notify"
	"github.com/ccmpbll/printspy/plugin"
	"github.com/ccmpbll/printspy/printmeta"
	"github.com/ccmpbll/printspy/smartplug"
)

type polledPrinter struct {
	plugin plugin.PrinterPlugin
	cancel context.CancelFunc
	// idleSince tracks when this printer entered the idle state, for the
	// auto-off-after-idle-timeout feature. Zero value means "not idle" (or
	// auto-off already fired for this idle streak).
	idleSince time.Time
	// notifiedCheckpoint1/2 are one-shot per print latches for the
	// checkpoint-percent notifications - reset to false whenever the
	// printer transitions into a fresh print.
	notifiedCheckpoint1 bool
	notifiedCheckpoint2 bool
}

type SSEMessage struct {
	Event string
	Data  []byte
}

type subscriber struct {
	ch     chan SSEMessage
	ctx    context.Context
	cancel context.CancelFunc
}

type Poller struct {
	mu       sync.RWMutex
	printers map[int64]*polledPrinter
	cache    map[int64]*models.PrinterStatus
	db       *db.DB
	wg       sync.WaitGroup

	subMu       sync.Mutex
	subscribers map[*subscriber]struct{}

	// uploadLocks serializes UploadFile per printer - two ingest jobs staged
	// against the same offline printer both fire a relay attempt the moment
	// it comes back online (see checkIngestOnline), and two concurrent PUTs
	// to the same printer's USB storage is exactly the kind of thing that
	// corrupts a transfer. Lazily created, keyed by printer ID.
	uploadLocks sync.Map

	// notifyClient is used for notification image capture (camera snapshots,
	// print thumbnails) - separate from other per-purpose clients in this
	// codebase (e.g. prusalink.Plugin's client/uploadClient split).
	notifyClient *http.Client

	// mqtt is the persistent broker connection for MQTT-mode smart plugs -
	// nil-safe (Configure with an empty broker URL is a no-op), coexists
	// with the existing per-tick HTTP path for plugs still in direct mode.
	mqtt *mqttplug.Client
}

func (p *Poller) uploadLock(id int64) *sync.Mutex {
	l, _ := p.uploadLocks.LoadOrStore(id, &sync.Mutex{})
	return l.(*sync.Mutex)
}

func New(database *db.DB) *Poller {
	return &Poller{
		printers:     make(map[int64]*polledPrinter),
		cache:        make(map[int64]*models.PrinterStatus),
		db:           database,
		subscribers:  make(map[*subscriber]struct{}),
		notifyClient: &http.Client{Timeout: 15 * time.Second},
		mqtt:         mqttplug.New(),
	}
}

// ConfigureMQTT (re)connects the MQTT client from the mqtt_broker_url/
// mqtt_username/mqtt_password settings, then syncs it against every
// MQTT-mode smart plug currently configured. Safe to call repeatedly -
// called once at startup and again whenever any mqtt_* setting is saved.
func (p *Poller) ConfigureMQTT() error {
	brokerURL, _ := p.db.GetSetting("mqtt_broker_url")
	username, _ := p.db.GetSetting("mqtt_username")
	password, _ := p.db.GetSetting("mqtt_password")
	if err := p.mqtt.Configure(brokerURL, username, password); err != nil {
		return err
	}
	return p.SyncMQTTSubscriptions()
}

// SyncMQTTSubscriptions resyncs the MQTT client against every smart plug
// across all printers - called after any smart-plug CRUD, mirroring the
// existing Repoll trigger for HTTP-mode plugs. Cheap in-memory operation
// (only wakes the network for topics that actually changed), safe to call
// unconditionally rather than filtering to MQTT-mode rows first.
func (p *Poller) SyncMQTTSubscriptions() error {
	plugs, err := p.db.ListAllSmartPlugs()
	if err != nil {
		return err
	}
	p.mqtt.Sync(plugs)
	return nil
}

func (p *Poller) Wait() {
	p.wg.Wait()
}

func (p *Poller) getInterval(perPrinter int) time.Duration {
	if v, err := p.db.GetSetting("poll_interval"); err == nil && v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return time.Duration(n) * time.Second
		}
	}
	interval := time.Duration(perPrinter) * time.Second
	if interval < time.Second {
		interval = 10 * time.Second
	}
	return interval
}

func (p *Poller) Start(ctx context.Context) error {
	printers, err := p.db.ListPrinters()
	if err != nil {
		return err
	}
	for _, printer := range printers {
		if printer.Enabled && !printer.Maintenance {
			p.AddPrinter(ctx, printer)
		}
	}
	return nil
}

func (p *Poller) AddPrinter(parentCtx context.Context, config models.PrinterConfig) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if existing, ok := p.printers[config.ID]; ok {
		existing.cancel()
	}

	pl, err := plugin.Create(config)
	if err != nil {
		log.Printf("failed to create plugin for printer %d (%s): %v", config.ID, config.Name, err)
		return
	}

	ctx, cancel := context.WithCancel(parentCtx)
	pp := &polledPrinter{
		plugin: pl,
		cancel: cancel,
	}
	p.printers[config.ID] = pp

	interval := p.getInterval(config.PollInterval)

	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		p.pollLoop(ctx, config.ID, config.Name, pp.plugin, interval)
	}()

	if kp, ok := pl.(plugin.Keepalive); ok {
		if host, enabled := kp.KeepaliveHost(); enabled {
			if pingInterval := p.pingInterval(); pingInterval > 0 {
				p.wg.Add(1)
				go func() {
					defer p.wg.Done()
					p.keepaliveLoop(ctx, config.ID, config.Name, host, pingInterval)
				}()
			}
		}
	}
}

// pingInterval returns the configured keepalive ping interval, or 0 if
// disabled.
func (p *Poller) pingInterval() time.Duration {
	v, err := p.db.GetSetting("prusalink_ping_interval")
	if err != nil || v == "" {
		return 0
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return 0
	}
	return time.Duration(n) * time.Second
}

// keepaliveLoop periodically ICMP-pings a printer's IP, independent of the
// status poll loop, to keep its wifi interface from dropping off the
// network during idle periods.
func (p *Poller) keepaliveLoop(ctx context.Context, id int64, name, host string, interval time.Duration) {
	log.Printf("starting keepalive ping for printer %d (%s) -> %s every %s", id, name, host, interval)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := pingHost(host, 3*time.Second); err != nil {
				log.Printf("[printer:%d] keepalive ping to %s failed: %v", id, host, err)
			}
		}
	}
}

func (p *Poller) RemovePrinter(id int64) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if pp, ok := p.printers[id]; ok {
		pp.cancel()
		delete(p.printers, id)
		delete(p.cache, id)
	}
}

func (p *Poller) GetStatus(id int64) *models.PrinterStatus {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.cache[id]
}

func (p *Poller) GetAllStatuses() map[int64]*models.PrinterStatus {
	p.mu.RLock()
	defer p.mu.RUnlock()

	result := make(map[int64]*models.PrinterStatus, len(p.cache))
	for id, status := range p.cache {
		result[id] = status
	}
	return result
}

// plugin returns printer id's plugin, or an error if the printer's unknown -
// collapses the RLock/lookup/RUnlock/not-found-check that used to precede
// every method below individually.
func (p *Poller) plugin(id int64) (plugin.PrinterPlugin, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	pp, ok := p.printers[id]
	if !ok {
		return nil, fmt.Errorf("printer %d not found", id)
	}
	return pp.plugin, nil
}

func (p *Poller) GetWebcamURL(id int64) string {
	pl, err := p.plugin(id)
	if err != nil {
		return ""
	}
	return pl.GetWebcamURL()
}

// GetDisplayName returns the plugin's human-readable name for its printer
// type (e.g. "PrusaLink", "OctoPrint"), so callers don't need to hardcode
// a mapping from config.Type themselves.
func (p *Poller) GetDisplayName(id int64) string {
	pl, err := p.plugin(id)
	if err != nil {
		return ""
	}
	return pl.DisplayName()
}

func (p *Poller) GetSnapshotURL(id int64) string {
	pl, err := p.plugin(id)
	if err != nil {
		return ""
	}
	return pl.GetSnapshotURL()
}

// AuthenticatedDo proxies req through the printer's plugin, which applies
// whatever auth scheme that printer type needs - callers don't need to
// know or care which one.
func (p *Poller) AuthenticatedDo(id int64, client *http.Client, req *http.Request) (*http.Response, error) {
	pl, err := p.plugin(id)
	if err != nil {
		return nil, err
	}
	return pl.AuthenticatedDo(client, req)
}

func (p *Poller) GetRecentFiles(ctx context.Context, id int64, limit int) ([]models.RecentFile, error) {
	pl, err := p.plugin(id)
	if err != nil {
		return nil, err
	}
	files, err := pl.GetRecentFiles(ctx, limit)
	if err != nil {
		return nil, err
	}
	p.backfillFileStats(id, files)
	p.backfillFileMeta(id, pl, files)

	// Only an unlimited listing is the printer's complete, authoritative
	// file set - a truncated one would wrongly look like "everything else
	// is gone" and prune cache rows for files just outside the cutoff.
	if limit <= 0 {
		paths := make([]string, len(files))
		for i, f := range files {
			paths[i] = f.Path
		}
		p.db.PruneFileMetaCache(id, paths)
	}
	return files, nil
}

// backfillFileMeta fills in tools/thumbnail data (see file_meta_cache) for
// files PrintSpy never personally uploaded - USB, direct slicer connection.
// PrusaLink-only (MetadataDownloader) - files uploaded through PrintSpy are
// already cached at upload time (see cacheFileMetaFromBytes), and print
// completions cache themselves too (see trackPrintHistory), so this only
// ever does real work for files that reached the printer some other way.
//
// The cache-hit check is cheap (DB only) and applied synchronously, so a
// fresh cache still shows tools/thumbnails on this exact response. Actual
// downloads for cache misses are real network cost - one full-file fetch
// per miss - so those run in a background goroutine instead of blocking
// this call: File Manager opens instantly with whatever's already cached,
// and the newly-backfilled data shows up the next time this listing loads.
func (p *Poller) backfillFileMeta(id int64, pl plugin.PrinterPlugin, files []models.RecentFile) {
	downloader, ok := pl.(plugin.MetadataDownloader)
	if !ok {
		return
	}
	var stale []models.RecentFile
	for i := range files {
		f := &files[i]
		row, hit, _ := p.db.GetFileMetaCache(id, f.Path)
		if hit && row.UploadedAt == f.UploadedAt {
			if len(row.ToolsJSON) > 0 {
				f.Tools = row.ToolsJSON
			}
			continue
		}
		stale = append(stale, *f)
	}
	if len(stale) == 0 {
		return
	}

	go func() {
		// Not the request's context - that's cancelled the moment the HTTP
		// handler returns, which is before this goroutine even starts.
		ctx := context.Background()
		for _, f := range stale {
			// Generous on purpose - this is fully backgrounded (File Manager
			// already opens instantly regardless, see backfillFileMeta's own
			// doc comment), so there's no UI cost to waiting longer. 15s was
			// too tight for real production networks - even a Range-capped
			// 2MB .bgcode fetch was timing out on most files, not just the
			// occasional outsized one seen in local testing.
			fetchCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
			lock := p.uploadLock(id)
			lock.Lock()
			data, err := downloader.DownloadFileForMetadata(fetchCtx, "/"+f.Origin+"/"+f.Path, f.FileName)
			lock.Unlock()
			cancel()
			if err != nil {
				log.Printf("[printer:%d] backfill: failed to download %s for metadata: %v", id, f.FileName, err)
				continue
			}

			p.parseAndCacheFileMeta(id, f.FileName, f.Path, f.UploadedAt, data)
		}
	}()
}

// cacheFilePath strips a "/origin/path" ref (e.g. PrusaLink's
// Refs.Download, "/usb/COREON~1.BGCODE") down to the bare path
// ("COREON~1.BGCODE") that file_meta_cache keys on elsewhere
// (RecentFile.Path convention) - a no-op for values that are already bare.
func cacheFilePath(ref string) string {
	if rest, ok := strings.CutPrefix(ref, "/"); ok {
		if _, path, ok := strings.Cut(rest, "/"); ok {
			return path
		}
	}
	return ref
}

// backfillFileStats fills success/failure stats from print_history for
// plugins whose own API doesn't report per-file stats natively (PrusaLink) -
// OctoPrint's plugin already populates these directly, so files it touched
// are left alone.
func (p *Poller) backfillFileStats(id int64, files []models.RecentFile) {
	var needsBackfill bool
	for _, f := range files {
		if f.SuccessCount == 0 && f.FailureCount == 0 && f.LastPrinted == 0 {
			needsBackfill = true
			break
		}
	}
	if !needsBackfill {
		return
	}

	stats, err := p.db.GetFileHistoryStats(id)
	if err != nil || len(stats) == 0 {
		return
	}
	for i, f := range files {
		if f.SuccessCount != 0 || f.FailureCount != 0 || f.LastPrinted != 0 {
			continue
		}
		if stat, ok := stats[f.FileName]; ok {
			files[i].SuccessCount = stat.SuccessCount
			files[i].FailureCount = stat.FailureCount
			files[i].LastPrinted = stat.LastPrinted
			lastSuccess := stat.LastSuccess
			files[i].LastSuccess = &lastSuccess
		}
	}
}

func (p *Poller) UploadFile(ctx context.Context, id int64, storage, path string, data []byte, printAfter bool) error {
	pl, err := p.plugin(id)
	if err != nil {
		return err
	}
	lock := p.uploadLock(id)
	lock.Lock()
	realPath, uploadedAt, err := pl.UploadFile(ctx, storage, path, data, printAfter)
	lock.Unlock()
	if err == nil {
		// PrusaLink-only (see MetadataDownloader) - OctoPrint already has its
		// own richer thumbnail/job API and isn't part of this cache.
		if _, ok := pl.(plugin.MetadataDownloader); ok {
			p.parseAndCacheFileMeta(id, path, realPath, uploadedAt, data)
		}
	}
	return err
}

// parseAndCacheFileMeta parses data already in hand - either just relayed
// via upload, or freshly downloaded by backfillFileMeta - and writes
// whatever it finds into file_meta_cache. filename drives format detection
// (.bgcode vs .gcode - needs the real extension); cachePath is the
// printer's own resolved storage path, the cache key every other read path
// uses - the two differ for PrusaLink, which mangles an uploaded name into
// an 8.3 short name on write (e.g. "verify.bgcode" -> "VERIFY~1.BGC").
func (p *Poller) parseAndCacheFileMeta(id int64, filename, cachePath string, uploadedAt int64, data []byte) {
	if cachePath == "" {
		return // plugin couldn't tell us its real storage path - nothing to cache under
	}
	info, err := printmeta.Parse(filename, data)
	if err != nil {
		return
	}
	toolsJSON := toolsJSONFor(info)
	if toolsJSON == nil && len(info.Thumbnail) == 0 {
		return
	}
	if uploadedAt == 0 {
		uploadedAt = time.Now().Unix()
	}
	p.db.SetFileMetaCache(id, cachePath, uploadedAt, toolsJSON, info.Thumbnail, info.ThumbnailContentType)
}

func toolsJSONFor(info *printmeta.Info) []byte {
	if len(info.Tools) == 0 {
		return nil
	}
	b, _ := json.Marshal(info.Tools)
	return b
}

func (p *Poller) DeleteFile(ctx context.Context, id int64, storage, path string) error {
	pl, err := p.plugin(id)
	if err != nil {
		return err
	}
	return pl.DeleteFile(ctx, storage, path)
}

func (p *Poller) DownloadFile(ctx context.Context, id int64, storage, path string) ([]byte, error) {
	pl, err := p.plugin(id)
	if err != nil {
		return nil, err
	}
	return pl.DownloadFile(ctx, storage, path)
}

func (p *Poller) StartPrint(ctx context.Context, id int64, location, path string) error {
	pl, err := p.plugin(id)
	if err != nil {
		return err
	}
	return pl.StartPrint(ctx, location, path)
}

func (p *Poller) ControlPrint(ctx context.Context, id int64, action string) error {
	pl, err := p.plugin(id)
	if err != nil {
		return err
	}
	switch action {
	case "pause":
		return pl.PausePrint(ctx)
	case "resume":
		return pl.ResumePrint(ctx)
	case "cancel":
		return pl.CancelPrint(ctx)
	default:
		return fmt.Errorf("unknown action: %s", action)
	}
}

// Repoll re-polls a printer immediately instead of waiting for the next
// scheduled tick. Used when something outside the poll loop changes a
// printer's smart plug assignment/label, so the dashboard reflects it right
// away rather than sitting stale until the next tick.
func (p *Poller) Repoll(ctx context.Context, id int64) {
	pl, err := p.plugin(id)
	if err != nil {
		return
	}
	p.poll(ctx, id, pl)
}

// ResetAllIdleClocks restarts every printer's idle-timeout clock. Called
// when the global auto_off_idle_minutes setting changes - without this, a
// printer already sitting idle longer than a newly-lowered timeout would
// have its plug cut on the very next poll, instead of the timeout applying
// from the moment the setting was actually saved.
func (p *Poller) ResetAllIdleClocks() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, pp := range p.printers {
		pp.idleSince = time.Time{}
	}
}

// WaitOnline blocks until printer id's cached state is no longer
// offline/disconnected, or ctx's deadline/cancellation fires. Repolls
// immediately first, since a printer just powered on will still show stale
// cached "offline" from the last tick.
func (p *Poller) WaitOnline(ctx context.Context, id int64) error {
	p.Repoll(ctx, id)
	if p.IsOnline(id) {
		return nil
	}

	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("printer %d did not come online: %w", id, ctx.Err())
		case <-ticker.C:
			p.Repoll(ctx, id)
			if p.IsOnline(id) {
				return nil
			}
		}
	}
}

func (p *Poller) IsOnline(id int64) bool {
	status := p.GetStatus(id)
	if status == nil {
		return false
	}
	return status.State != models.StateOffline && status.State != models.StateDisconnected
}

// SetPowerState toggles a plug. On success it immediately patches the
// cached status with the new on/off value and broadcasts that — the device
// already ACKed the command, so this isn't optimistic, it's just not
// waiting on an unrelated full printer poll (temps, job, thumbnail) to
// confirm what's already known. A full poll still runs in the background
// to reconcile everything else (watts, printer state), but the visible
// plug toggle doesn't wait on it.
func (p *Poller) SetPowerState(ctx context.Context, id int64, plugID string, on bool) error {
	pl, err := p.plugin(id)
	if err != nil {
		return err
	}

	plugs, err := p.db.ListSmartPlugs(id)
	if err != nil {
		return err
	}

	var setErr error
	direct := false
	for _, sp := range plugs {
		if PlugIDFor(sp) != plugID {
			continue
		}
		if sp.MQTTTopic != "" {
			setErr = p.mqtt.SetState(ctx, sp.MQTTTopic, sp.Idx, on)
		} else {
			setErr = smartplug.New().SetState(ctx, sp.IP, sp.Idx, on)
		}
		direct = true
		break
	}
	if !direct {
		setErr = pl.SetPowerState(ctx, plugID, on)
	}

	if setErr != nil {
		log.Printf("[printer:%d] manual power toggle failed for %s (on=%v): %v", id, plugID, on, setErr)
	} else {
		slog.Debug("power toggle succeeded", "printer", id, "plug", plugID, "on", on)
		p.patchPowerState(id, plugID, "", on)
	}
	go p.poll(context.Background(), id, pl)
	return setErr
}

// patchPowerState flips a single plug's on/off value in the cached status
// and broadcasts it, without waiting on a full printer poll. source, if
// non-empty, overrides the plug's reported Source (e.g. "auto-idle") so the
// UI can tell an automatic action apart from a manual toggle - left as-is
// (whatever the plugin/smart-plug client last reported) when empty.
func (p *Poller) patchPowerState(id int64, plugID, source string, on bool) {
	p.mu.Lock()
	status, ok := p.cache[id]
	if !ok || status == nil {
		p.mu.Unlock()
		return
	}
	patched := *status
	patched.Power = append([]models.PowerState(nil), status.Power...)
	found := false
	for i := range patched.Power {
		// Multiple entries can share an ID if the same physical device is
		// both auto-detected (e.g. OctoPrint's own Tasmota plugin) and
		// separately assigned as a direct smart plug — patch all of them.
		if patched.Power[i].ID == plugID {
			patched.Power[i].On = on
			if source != "" {
				patched.Power[i].Source = source
			}
			found = true
		}
	}
	p.cache[id] = &patched
	p.mu.Unlock()

	if found {
		p.broadcast(id, &patched)
	}
}

func (p *Poller) GetThumbnailURL(id int64) string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if status, ok := p.cache[id]; ok {
		return status.ThumbnailURL
	}
	return ""
}

// captureCameraOrThumbnail fetches an image to attach to a notification:
// the assigned camera's live snapshot if reachable, else the current print's
// plate thumbnail. Mirrors handleSnapshotProxy/handleThumbnailProxy
// (api/handlers.go) exactly, just returning bytes instead of streaming to
// an http.ResponseWriter. Returns an error only when neither source is
// available - callers should treat that as "send text-only", not a failure.
// urlOverride, when non-empty, is used instead of the live cached status's
// thumbnail URL - the print-complete/failed notification fires after the
// job has already cleared from the cache (the next poll's status has no
// Job), so by then GetThumbnailURL would always return "". Callers still
// mid-job (checkpoints, print-started) pass "" and get the live lookup.
func (p *Poller) captureCameraOrThumbnail(ctx context.Context, id int64, urlOverride string) ([]byte, string, error) {
	snapshotURL := ""
	usingCamera := false
	if cam, err := p.db.GetCameraForPrinter(id); err == nil {
		snapshotURL = strings.TrimRight(cam.URL, "/") + "/snapshot"
		usingCamera = true
	} else {
		snapshotURL = p.GetSnapshotURL(id)
	}
	if snapshotURL != "" {
		if data, ct, err := p.fetchImageURL(ctx, id, snapshotURL, usingCamera); err == nil {
			return data, ct, nil
		}
	}
	return p.captureThumbnail(ctx, id, urlOverride)
}

// captureThumbnail fetches only the current print's plate thumbnail,
// skipping any assigned camera entirely. See captureCameraOrThumbnail for
// what urlOverride is for.
func (p *Poller) captureThumbnail(ctx context.Context, id int64, urlOverride string) ([]byte, string, error) {
	thumbURL := urlOverride
	if thumbURL == "" {
		thumbURL = p.GetThumbnailURL(id)
	}
	if thumbURL == "" {
		return nil, "", fmt.Errorf("no image available")
	}
	return p.fetchImageURL(ctx, id, thumbURL, false)
}

func (p *Poller) fetchImageURL(ctx context.Context, id int64, url string, direct bool) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, "", err
	}

	var resp *http.Response
	if direct {
		resp, err = p.notifyClient.Do(req)
	} else {
		resp, err = p.AuthenticatedDo(id, p.notifyClient, req)
	}
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("http %d", resp.StatusCode)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", err
	}
	return data, resp.Header.Get("Content-Type"), nil
}

// SSE subscriber management

func (p *Poller) Subscribe(ctx context.Context) *subscriber {
	subCtx, cancel := context.WithCancel(ctx)
	s := &subscriber{
		ch:     make(chan SSEMessage, 64),
		ctx:    subCtx,
		cancel: cancel,
	}
	p.subMu.Lock()
	p.subscribers[s] = struct{}{}
	p.subMu.Unlock()
	return s
}

func (p *Poller) Unsubscribe(s *subscriber) {
	s.cancel()
	p.subMu.Lock()
	delete(p.subscribers, s)
	p.subMu.Unlock()
}

func (s *subscriber) Chan() <-chan SSEMessage {
	return s.ch
}

func (s *subscriber) Done() <-chan struct{} {
	return s.ctx.Done()
}

func (p *Poller) BroadcastRefresh() {
	p.sendToAll(SSEMessage{Event: "refresh", Data: []byte(`{}`)})
}

func (p *Poller) broadcast(printerID int64, status *models.PrinterStatus) {
	payload := struct {
		PrinterID int64                 `json:"printer_id"`
		Status    *models.PrinterStatus `json:"status"`
	}{printerID, status}

	data, err := json.Marshal(payload)
	if err != nil {
		return
	}

	p.sendToAll(SSEMessage{Event: "status", Data: data})
}

func (p *Poller) sendToAll(msg SSEMessage) {
	p.subMu.Lock()
	defer p.subMu.Unlock()
	for s := range p.subscribers {
		select {
		case s.ch <- msg:
		default:
		}
	}
}

func (p *Poller) pollLoop(ctx context.Context, id int64, name string, pl plugin.PrinterPlugin, interval time.Duration) {
	log.Printf("starting poller for printer %d (%s) every %s", id, name, interval)

	if err := pl.Connect(ctx); err != nil {
		log.Printf("[printer:%d] initial connect failed: %v (will retry on poll)", id, err)
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	p.poll(ctx, id, pl)

	for {
		select {
		case <-ctx.Done():
			log.Printf("stopping poller for printer %d (%s)", id, name)
			return
		case <-ticker.C:
			p.poll(ctx, id, pl)
		}
	}
}

func (p *Poller) poll(ctx context.Context, id int64, pl plugin.PrinterPlugin) {
	start := time.Now()
	status, err := pl.GetStatus(ctx)
	if err != nil {
		log.Printf("[printer:%d] poll error: %v", id, err)
		status = &models.PrinterStatus{
			State:       models.StateOffline,
			LastUpdated: time.Now(),
		}
	}
	if status.Job != nil {
		slog.Debug("poll: status fetched", "printer", id, "state", status.State, "job_state", status.Job.JobState, "progress", status.Job.Progress, "duration", time.Since(start))
	} else {
		slog.Debug("poll: status fetched", "printer", id, "state", status.State, "duration", time.Since(start))
	}

	if plugs, err := p.db.ListSmartPlugs(id); err == nil && len(plugs) > 0 {
		status.Power = append(status.Power, p.fetchDirectPower(ctx, id, plugs)...)
	}
	for _, ps := range status.Power {
		slog.Debug("poll: plug state", "printer", id, "plug", ps.ID, "on", ps.On, "source", ps.Source)
	}

	p.mu.Lock()
	prev := p.cache[id]
	p.cache[id] = status
	p.mu.Unlock()

	prevState := models.StateOffline
	if prev != nil {
		prevState = prev.State
	}
	if prevState != status.State {
		log.Printf("[printer:%d] state changed: %s -> %s", id, prevState, status.State)
	}

	if status.State == models.StatePrinting && status.Job != nil {
		prevProgress := 0.0
		if prev != nil && prev.Job != nil {
			prevProgress = prev.Job.Progress
		}
		if int(status.Job.Progress) != int(prevProgress) {
			log.Printf("[printer:%d] printing %s (%.0f%%) hotend=%.0f/%.0f bed=%.0f/%.0f",
				id, status.Job.FileName, status.Job.Progress,
				status.Temps.HotendActual, status.Temps.HotendTarget,
				status.Temps.BedActual, status.Temps.BedTarget)
		}
	}

	p.trackPrintHistory(ctx, id, prevState, status, prev, pl)
	p.checkAutoOff(ctx, id, status)
	p.checkThermalRunaway(ctx, id, status)
	p.checkIngestOnline(ctx, id, prevState, status)

	// prev == nil is the very first poll for this printer since the poller
	// started (not just "was offline" - a printer legitimately offline
	// between polls still has a cached prev). If it's already printing at
	// that moment, printspy just started watching an in-progress print, not
	// witnessing one begin - seed checkpoint state from current progress
	// instead of running the normal checks, so a restart mid-print doesn't
	// fire a spurious "Print Started" or re-fire a checkpoint that already
	// passed before printspy came back up.
	if prev == nil && status.State == models.StatePrinting {
		p.seedPrintingState(id, status)
	} else {
		p.checkCheckpoints(ctx, id, status)
		p.checkPrintStartNotify(ctx, id, prevState, status)
	}
	p.checkErrorNotify(ctx, id, prevState, status)

	p.broadcast(id, status)
}

// checkIngestOnline relays any ingest jobs staged against this printer the
// moment it's next seen online - covers the "slicer sent Upload while the
// printer was off" case, where nothing else would ever trigger the relay
// (no proactive power-on for a plain Upload, no manual dispatch button).
// Firing on every poll where prevState was offline/disconnected (including
// the very first poll after startup, where prevState defaults to Offline)
// means a job staged while the app itself was down still gets picked up.
func (p *Poller) checkIngestOnline(ctx context.Context, id int64, prevState models.PrinterState, status *models.PrinterStatus) {
	wasOffline := prevState == models.StateOffline || prevState == models.StateDisconnected
	isOnline := status.State != models.StateOffline && status.State != models.StateDisconnected
	if !wasOffline || !isOnline {
		return
	}
	jobs, err := p.db.ListPendingIngestJobsForPrinter(id)
	if err != nil || len(jobs) == 0 {
		return
	}
	// One goroutine processing jobs in order, not one goroutine per job -
	// UploadFile's per-printer lock would serialize the actual transfers
	// either way, but running the claim+WaitOnline steps for job 2 while
	// job 1 is still uploading is pointless contention for no benefit here.
	go func() {
		for _, job := range jobs {
			p.RelayIngestJob(ctx, job.ID, id)
		}
	}()
}

// RelayIngestJob claims a staged job and, if the printer is currently
// online, uploads it without issuing a print command - the passive "Upload"
// path. No-ops (leaves the job pending) if the printer isn't online yet;
// callers only invoke this when they already believe it's online
// (checkIngestOnline, or ingest.Handler.upload for the already-online-at-
// upload-time case), but re-checking here avoids a race against a printer
// that dropped offline again in between.
func (p *Poller) RelayIngestJob(ctx context.Context, jobID, printerID int64) {
	if !p.IsOnline(printerID) {
		return
	}
	claimed, err := p.db.ClaimIngestJobForDispatch(jobID, printerID)
	if err != nil || !claimed {
		return
	}
	defer p.BroadcastRefresh()

	job, err := p.db.GetIngestJob(jobID)
	if err != nil {
		return
	}
	data, err := os.ReadFile(job.FilePath)
	if err != nil {
		p.db.SetIngestJobFailed(jobID, "failed to read staged file: "+err.Error())
		return
	}
	if err := p.UploadFile(ctx, printerID, "usb", job.Filename, data, false); err != nil {
		p.db.SetIngestJobFailed(jobID, err.Error())
		return
	}
	p.db.DeleteIngestJob(jobID)
	os.RemoveAll(filepath.Dir(job.FilePath))
}

// sendNotification is the shared Pushover send path for every notification
// type (checkpoints, complete/failed, error). No-ops silently if Pushover
// credentials aren't configured. Image capture failure (no camera, no
// thumbnail) degrades to a text-only notification rather than blocking send.
//
// eventType is the settings-key prefix ("checkpoint1", "complete", "error",
// etc) used to look up a per-type custom title/message/sound/priority
// override; defaultTitle/defaultMessage are used verbatim when no override
// is set, so leaving a type uncustomized reproduces the exact previous
// hardcoded behavior. When a custom template IS set, placeholders (e.g.
// "{printer}") are substituted from values.
func (p *Poller) sendNotification(ctx context.Context, id int64, eventType, defaultTitle, defaultMessage string, values map[string]string, thumbnailURLOverride string) {
	token, _ := p.db.GetSetting("pushover_app_token")
	userKey, _ := p.db.GetSetting("pushover_user_key")
	if token == "" || userKey == "" {
		return
	}

	title := defaultTitle
	if custom, _ := p.db.GetSetting("notify_" + eventType + "_title"); custom != "" {
		title = applyPlaceholders(custom, values)
	}
	message := defaultMessage
	if custom, _ := p.db.GetSetting("notify_" + eventType + "_message"); custom != "" {
		message = applyPlaceholders(custom, values)
	}
	priority := 0
	if p.notifySettingBool("notify_" + eventType + "_high_priority") {
		priority = 1
	}
	sound, _ := p.db.GetSetting("notify_" + eventType + "_sound")

	// Print Started defaults to thumbnail-only (nothing's necessarily even
	// visible on the camera yet at the very start of a print); every other
	// type defaults to camera-with-thumbnail-fallback. Either is overridable
	// per type via notify_<type>_image.
	defaultImageMode := "camera"
	if eventType == "start" {
		defaultImageMode = "thumbnail"
	}
	imageMode := defaultImageMode
	if v, _ := p.db.GetSetting("notify_" + eventType + "_image"); v != "" {
		imageMode = v
	}

	var image []byte
	var contentType string
	switch imageMode {
	case "none":
	case "thumbnail":
		image, contentType, _ = p.captureThumbnail(ctx, id, thumbnailURLOverride)
	default: // "camera"
		image, contentType, _ = p.captureCameraOrThumbnail(ctx, id, thumbnailURLOverride)
	}

	msg := notify.Message{
		Title: title, Text: message,
		Image: image, ImageContentType: contentType,
		Priority: priority, Sound: sound,
	}
	if err := notify.Send(token, userKey, msg); err != nil {
		log.Printf("[printer:%d] pushover send failed: %v", id, err)
	}
}

func applyPlaceholders(tmpl string, values map[string]string) string {
	pairs := make([]string, 0, len(values)*2)
	for k, v := range values {
		pairs = append(pairs, "{"+k+"}", v)
	}
	return strings.NewReplacer(pairs...).Replace(tmpl)
}

// notifyPrinterName resolves the printer's configured nickname for use in
// a notification title/message; falls back to the numeric id if the
// printer record can't be read (shouldn't happen for an actively-polled
// printer, but a notification is worth sending either way).
func (p *Poller) notifyPrinterName(id int64) string {
	if printer, err := p.db.GetPrinter(id); err == nil {
		return printer.Name
	}
	return fmt.Sprintf("printer %d", id)
}

func (p *Poller) notifySettingBool(key string) bool {
	v, err := p.db.GetSetting(key)
	return err == nil && v == "1"
}

func (p *Poller) notifySettingPercent(key string, def float64) float64 {
	v, err := p.db.GetSetting(key)
	if err != nil || v == "" {
		return def
	}
	n, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return def
	}
	return n
}

// seedPrintingState runs once, on the very first poll after a restart,
// when a printer's discovered already mid-print. Marks any checkpoint
// whose threshold the print has already passed as fired, so it won't
// notify for progress that happened before printspy came back up - a
// checkpoint whose threshold hasn't been reached yet is left alone and
// fires normally once actually crossed.
func (p *Poller) seedPrintingState(id int64, status *models.PrinterStatus) {
	if status.Job == nil {
		return
	}
	progress := status.Job.Progress
	c1 := progress >= p.notifySettingPercent("notify_checkpoint1_percent", 5)
	c2 := progress >= p.notifySettingPercent("notify_checkpoint2_percent", 50)

	p.mu.Lock()
	defer p.mu.Unlock()
	pp, ok := p.printers[id]
	if !ok {
		return
	}
	if c1 {
		pp.notifiedCheckpoint1 = true
	}
	if c2 {
		pp.notifiedCheckpoint2 = true
	}
}

// checkCheckpoints fires the checkpoint1/checkpoint2 percent-of-completion
// notifications, each one-shot per print. Latches reset whenever the
// printer isn't actively printing/paused, so they're armed again for the
// next print without needing a separate "print just started" transition
// check.
func (p *Poller) checkCheckpoints(ctx context.Context, id int64, status *models.PrinterStatus) {
	printing := status.State == models.StatePrinting || status.State == models.StatePaused

	p.mu.Lock()
	pp, ok := p.printers[id]
	if !ok {
		p.mu.Unlock()
		return
	}
	if !printing {
		pp.notifiedCheckpoint1 = false
		pp.notifiedCheckpoint2 = false
		p.mu.Unlock()
		return
	}
	if status.State != models.StatePrinting || status.Job == nil {
		p.mu.Unlock()
		return
	}

	progress := status.Job.Progress
	fire1 := !pp.notifiedCheckpoint1 && p.notifySettingBool("notify_checkpoint1_enabled") && progress >= p.notifySettingPercent("notify_checkpoint1_percent", 5)
	fire2 := !pp.notifiedCheckpoint2 && p.notifySettingBool("notify_checkpoint2_enabled") && progress >= p.notifySettingPercent("notify_checkpoint2_percent", 50)
	if fire1 {
		pp.notifiedCheckpoint1 = true
	}
	if fire2 {
		pp.notifiedCheckpoint2 = true
	}
	p.mu.Unlock()

	if !fire1 && !fire2 {
		return
	}
	printerName := p.notifyPrinterName(id)
	fileName := status.Job.FileName
	defaultMessage := fmt.Sprintf("%s: %s reached %.0f%%", printerName, fileName, status.Job.Progress)
	placeholders := map[string]string{
		"printer": printerName,
		"file":    fileName,
		"percent": fmt.Sprintf("%.0f", status.Job.Progress),
	}
	if fire1 {
		p.sendNotification(ctx, id, "checkpoint1", "Print checkpoint", defaultMessage, placeholders, "")
	}
	if fire2 {
		p.sendNotification(ctx, id, "checkpoint2", "Print checkpoint", defaultMessage, placeholders, "")
	}
}

// notifyPrintResult sends the Print Complete/Failed notification. A
// cancelled print (user-initiated) sends nothing - not a "failure" in the
// alarming sense a Pushover alert implies.
func (p *Poller) notifyPrintResult(ctx context.Context, id int64, h *models.PrintHistory, thumbnailURL string) {
	var enabled bool
	var eventType, title string
	switch h.Result {
	case "completed":
		enabled = p.notifySettingBool("notify_on_complete")
		eventType, title = "complete", "Print complete"
	case "failed":
		enabled = p.notifySettingBool("notify_on_failed")
		eventType, title = "failed", "Print failed"
	default:
		return
	}
	if !enabled {
		return
	}

	printerName := p.notifyPrinterName(id)
	message := fmt.Sprintf("%s: %s", printerName, h.FileName)
	if h.Material != "" {
		message += fmt.Sprintf(" (%s", h.Material)
		if h.FilamentUsedG > 0 {
			message += fmt.Sprintf(", %.0fg", h.FilamentUsedG)
		}
		message += ")"
	}
	message += fmt.Sprintf(" - %s", formatDuration(h.DurationSecs))

	placeholders := map[string]string{
		"printer":    printerName,
		"file":       h.FileName,
		"material":   h.Material,
		"filament_g": fmt.Sprintf("%.0f", h.FilamentUsedG),
		"duration":   formatDuration(h.DurationSecs),
	}
	p.sendNotification(ctx, id, eventType, title, message, placeholders, thumbnailURL)
}

func formatDuration(secs int) string {
	d := time.Duration(secs) * time.Second
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	if h > 0 {
		return fmt.Sprintf("%dh%dm", h, m)
	}
	return fmt.Sprintf("%dm", m)
}

// checkErrorNotify fires the Error/Attention notification on the
// transition edge only - independent of trackPrintHistory's printing->done
// logic, since a thermal-runaway cutoff while idle should still notify.
func (p *Poller) checkErrorNotify(ctx context.Context, id int64, prevState models.PrinterState, status *models.PrinterStatus) {
	wasError := prevState == models.StateError || prevState == models.StateAttention
	isError := status.State == models.StateError || status.State == models.StateAttention
	if wasError || !isError {
		return
	}
	if !p.notifySettingBool("notify_on_error") {
		return
	}
	printerName := p.notifyPrinterName(id)
	message := status.StateMessage
	if message == "" {
		message = string(status.State)
	}
	placeholders := map[string]string{"printer": printerName, "message": message}
	p.sendNotification(ctx, id, "error", fmt.Sprintf("%s: error", printerName), message, placeholders, "")
}

// checkPrintStartNotify fires the Print Started notification on the
// transition edge only, mirroring checkErrorNotify. Known limitation shared
// with checkErrorNotify/checkIngestOnline: on the very first poll after an
// app restart, prevState defaults to Offline, so a printer already mid-print
// at startup looks like a fresh start and fires once spuriously - accepted,
// same tradeoff this codebase already makes elsewhere for restart edges.
func (p *Poller) checkPrintStartNotify(ctx context.Context, id int64, prevState models.PrinterState, status *models.PrinterStatus) {
	wasPrinting := prevState == models.StatePrinting || prevState == models.StatePaused
	nowPrinting := status.State == models.StatePrinting
	if wasPrinting || !nowPrinting || status.Job == nil {
		return
	}
	if !p.notifySettingBool("notify_on_start") {
		return
	}
	printerName := p.notifyPrinterName(id)
	fileName := status.Job.FileName
	message := fmt.Sprintf("%s: %s", printerName, fileName)
	placeholders := map[string]string{"printer": printerName, "file": fileName}
	p.sendNotification(ctx, id, "start", "Print started", message, placeholders, "")
}

// checkAutoOff powers off a printer's assigned smart plug(s) after it's
// stayed idle for longer than the configured timeout, once temps have
// dropped below the cooldown threshold. Global auto_off_idle_minutes wins
// if set (same override precedence as poll_interval); otherwise falls back
// to the printer's own idle_timeout_minutes. 0 = disabled.
func (p *Poller) checkAutoOff(ctx context.Context, id int64, status *models.PrinterStatus) {
	p.mu.Lock()
	pp, ok := p.printers[id]
	if !ok {
		p.mu.Unlock()
		return
	}
	if status.State != models.StateIdle {
		pp.idleSince = time.Time{}
		p.mu.Unlock()
		return
	}
	if pp.idleSince.IsZero() {
		pp.idleSince = time.Now()
		p.mu.Unlock()
		return
	}
	idleSince := pp.idleSince
	p.mu.Unlock()

	timeoutMinutes := p.autoOffIdleMinutes(id)
	if timeoutMinutes <= 0 || time.Since(idleSince) < time.Duration(timeoutMinutes)*time.Minute {
		return
	}

	cooldown := p.autoOffCooldownTemp()
	if status.Temps.HotendActual > cooldown || status.Temps.BedActual > cooldown {
		slog.Debug("auto-off: cooldown gate blocking", "printer", id, "hotend", status.Temps.HotendActual, "bed", status.Temps.BedActual, "cooldown", cooldown)
		return // still cooling down, check again next tick
	}

	slog.Debug("auto-off: idle timeout reached, powering off", "printer", id, "idle_for", time.Since(idleSince), "timeout_minutes", timeoutMinutes)
	p.autoPowerOff(ctx, id, "auto-idle")

	// One-shot per idle streak - won't refire until the printer leaves and
	// re-enters idle, even if a plug gets manually turned back on.
	p.mu.Lock()
	if pp, ok := p.printers[id]; ok {
		pp.idleSince = time.Time{}
	}
	p.mu.Unlock()
}

// checkThermalRunaway is a second, independent layer on top of the
// printer firmware's own thermal runaway protection (Marlin M912 /
// PrusaLink) - a flat max-temp threshold, same approach as
// OctoPrint-Tasmota's "Max Bed Temp"/"Max Extruder Temp", not trend
// analysis. Runs in every state (not just idle) since a runaway heater
// mid-print is exactly when this matters most.
func (p *Poller) checkThermalRunaway(ctx context.Context, id int64, status *models.PrinterStatus) {
	maxBed, maxExtruder := p.thermalMaxTemps(id)
	if maxBed <= 0 && maxExtruder <= 0 {
		return
	}
	overBed := maxBed > 0 && status.Temps.BedActual > maxBed
	overExtruder := maxExtruder > 0 && status.Temps.HotendActual > maxExtruder
	if !overBed && !overExtruder {
		return
	}
	log.Printf("[printer:%d] thermal runaway threshold exceeded (bed=%.0f max=%.0f, hotend=%.0f max=%.0f)",
		id, status.Temps.BedActual, maxBed, status.Temps.HotendActual, maxExtruder)
	p.autoPowerOff(ctx, id, "auto-thermal")
}

// autoPowerOff powers off every smart plug assigned to id, tagging the
// change with source so the UI can distinguish it from a manual toggle.
// No-ops per-plug if already off, so a threshold that stays tripped (or a
// printer that stays idle) doesn't spam the smart plug's API every tick.
func (p *Poller) autoPowerOff(ctx context.Context, id int64, source string) {
	plugs, err := p.db.ListSmartPlugs(id)
	if err != nil || len(plugs) == 0 {
		return
	}
	for _, sp := range plugs {
		plugID := PlugIDFor(sp)
		if !p.isPlugOn(id, plugID) {
			slog.Debug("auto-off: plug already off, skipping", "printer", id, "plug", plugID)
			continue
		}
		if err := p.forcePlugOff(ctx, sp); err != nil {
			log.Printf("[printer:%d] auto power-off (%s) failed for %s: %v", id, source, plugID, err)
			continue
		}
		log.Printf("[printer:%d] auto power-off (%s): %s", id, source, plugID)
		p.patchPowerState(id, plugID, source, false)
	}
}

// plugIDFor returns the dashboard/cache identifier for a smart plug -
// "mqtt:<topic>:<idx>" for MQTT-mode plugs, "<ip>:<idx>" for HTTP-direct
// plugs, matching SetPowerState's own matching convention.
func PlugIDFor(sp models.SmartPlug) string {
	if sp.MQTTTopic != "" {
		return "mqtt:" + sp.MQTTTopic + ":" + sp.Idx
	}
	return sp.IP + ":" + sp.Idx
}

// forcePlugOff branches HTTP/MQTT internally - shared by every call site
// that needs to force a plug off (currently just autoPowerOff, for both the
// idle-timeout and thermal-runaway triggers).
func (p *Poller) forcePlugOff(ctx context.Context, sp models.SmartPlug) error {
	if sp.MQTTTopic != "" {
		return p.mqtt.SetState(ctx, sp.MQTTTopic, sp.Idx, false)
	}
	return smartplug.New().SetState(ctx, sp.IP, sp.Idx, false)
}

func (p *Poller) isPlugOn(id int64, plugID string) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	status, ok := p.cache[id]
	if !ok || status == nil {
		return false
	}
	for _, ps := range status.Power {
		if ps.ID == plugID {
			return ps.On
		}
	}
	return false
}

// autoOffIdleMinutes resolves the effective idle-timeout: the global
// auto_off_idle_minutes setting wins if set, else the printer's own
// idle_timeout_minutes.
func (p *Poller) autoOffIdleMinutes(id int64) int {
	if v, err := p.db.GetSetting("auto_off_idle_minutes"); err == nil && v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	printer, err := p.db.GetPrinter(id)
	if err != nil {
		return 0
	}
	return printer.IdleTimeoutMinutes
}

// autoOffCooldownTemp is global-only (not per-printer) - defaults to 40°C.
func (p *Poller) autoOffCooldownTemp() float64 {
	v, err := p.db.GetSetting("auto_off_cooldown_temp")
	if err != nil || v == "" {
		return 40
	}
	n, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return 40
	}
	return n
}

// thermalMaxTemps resolves the effective max bed/extruder temps. Each
// resolves independently: global setting wins if set, else that printer's
// own override.
func (p *Poller) thermalMaxTemps(id int64) (maxBed, maxExtruder float64) {
	maxBed = p.globalThermalSetting("thermal_max_bed_temp")
	maxExtruder = p.globalThermalSetting("thermal_max_extruder_temp")
	if maxBed != 0 && maxExtruder != 0 {
		return
	}
	printer, err := p.db.GetPrinter(id)
	if err != nil {
		return
	}
	if maxBed == 0 {
		maxBed = printer.MaxBedTemp
	}
	if maxExtruder == 0 {
		maxExtruder = printer.MaxExtruderTemp
	}
	return
}

func (p *Poller) globalThermalSetting(key string) float64 {
	v, err := p.db.GetSetting(key)
	if err != nil || v == "" {
		return 0
	}
	n, _ := strconv.ParseFloat(v, 64)
	return n
}

func (p *Poller) fetchDirectPower(ctx context.Context, id int64, plugs []models.SmartPlug) []models.PowerState {
	client := smartplug.New()
	var states []models.PowerState
	for _, sp := range plugs {
		if sp.MQTTTopic != "" {
			// Cache-only read, no I/O - state arrives via subscribed MQTT
			// messages. Skip this tick if nothing's arrived yet; self-heals
			// once the broker delivers a message.
			if ps, ok := p.mqtt.GetState(sp.MQTTTopic, sp.Idx); ok {
				states = append(states, ps)
			} else {
				slog.Debug("mqtt plug cache miss, skipping this tick", "printer", id, "topic", sp.MQTTTopic, "idx", sp.Idx)
			}
			continue
		}
		ps, err := client.GetState(ctx, sp.IP, sp.Idx, sp.Label, sp.HideLabel)
		if err != nil {
			log.Printf("[printer:%d] smart plug %s:%s unreachable: %v", id, sp.IP, sp.Idx, err)
			continue
		}
		states = append(states, *ps)
	}
	return states
}

func (p *Poller) trackPrintHistory(ctx context.Context, id int64, prevState models.PrinterState, status *models.PrinterStatus, prev *models.PrinterStatus, pl plugin.PrinterPlugin) {
	wasPrinting := prevState == models.StatePrinting || prevState == models.StatePaused
	nowDone := status.State == models.StateIdle || status.State == models.StateError || status.State == models.StateOffline

	if !wasPrinting || !nowDone {
		return
	}

	fileName := ""
	filePath := ""
	elapsed := 0
	filament := 0.0
	if prev != nil && prev.Job != nil {
		fileName = prev.Job.FileName
		filePath = prev.Job.FilePath
		elapsed = prev.Job.ElapsedSecs
		filament = prev.Job.FilamentUsedMM
	}
	if fileName == "" {
		return
	}
	// Grabbed from prev, not the live cache - by the time the completion
	// notification sends, this poll's status has already cleared Job (and
	// with it ThumbnailURL), so a live lookup would always come back empty.
	thumbnailURL := ""
	if prev != nil {
		thumbnailURL = prev.ThumbnailURL
	}

	// Prefers the plugin's own job-lifecycle state when it has one over
	// guessing from the last polled progress percentage - checks the
	// current tick's Job first (freshest - PrusaLink can still report it
	// with the terminal state on the very tick printer.state flips to
	// Idle), falling back to prev's if the current tick already cleared
	// Job.
	jobState := ""
	lastProgress := 0.0
	haveLastProgress := false
	if status.Job != nil {
		jobState = status.Job.JobState
	} else if prev != nil && prev.Job != nil {
		jobState = prev.Job.JobState
	}
	if prev != nil && prev.Job != nil {
		lastProgress = prev.Job.Progress
		haveLastProgress = true
	}
	result := printResult(jobState, status.State == models.StateError, haveLastProgress, lastProgress)

	now := time.Now().UTC().Format(time.RFC3339)
	h := &models.PrintHistory{
		PrinterID:      id,
		FileName:       fileName,
		StartedAt:      time.Now().Add(-time.Duration(elapsed) * time.Second).UTC().Format(time.RFC3339),
		CompletedAt:    now,
		DurationSecs:   elapsed,
		Result:         result,
		FilamentUsedMM: filament,
	}

	// The file is guaranteed to still be on the printer's own storage at
	// this exact instant, since it just finished printing from it - no
	// need to have intercepted the upload earlier. Best-effort: a
	// completed print is recorded either way, with or without this extra
	// detail. Runs in its own goroutine (a real network fetch of a
	// multi-MB file) so a slow/offline printer here never stalls the poll
	// tick for every other printer.
	downloader, ok := pl.(plugin.MetadataDownloader)
	if !ok || filePath == "" {
		p.insertPrintHistory(ctx, id, h, thumbnailURL)
		return
	}
	go func() {
		data, err := downloader.DownloadFileForMetadata(ctx, filePath, fileName)
		if err != nil {
			log.Printf("[printer:%d] failed to download %s for print metadata: %v", id, fileName, err)
		} else if info, err := printmeta.Parse(fileName, data); err != nil {
			log.Printf("[printer:%d] failed to parse print metadata for %s: %v", id, fileName, err)
		} else {
			h.LayerHeightMM = info.LayerHeightMM
			h.FillDensity = info.FillDensity
			h.PrinterModel = info.PrinterModel
			h.Material = info.Material
			h.ToolIndex = info.ToolIndex
			h.FilamentUsedMM = info.FilamentUsedMM
			h.FilamentUsedG = info.FilamentUsedG
			h.FilamentCost = info.FilamentCost
			h.EstimatedSecs = info.EstimatedSecs
			h.MaxLayerZ = info.MaxLayerZ
			h.ObjectNames = strings.Join(info.ObjectNames, ", ")
			h.ToolChanges = info.ToolChanges
			// History's own Tools column stays multi-tool-only (its
			// established, existing meaning - a single-tool print already
			// shows its material via h.Material/h.ToolIndex instead).
			// file_meta_cache's tools_json is broader - File Manager wants
			// every file's material/tool shown, not just multi-tool ones.
			toolsJSON := toolsJSONFor(info)
			if len(info.Tools) > 1 {
				h.Tools = toolsJSON
			}
			// History's own copy - a completed print is a permanent record
			// and shouldn't depend on whether the source file still exists
			// on the printer (see GetPrintHistoryThumbnail).
			h.Thumbnail = info.Thumbnail
			h.ThumbnailContentType = info.ThumbnailContentType

			// Same bytes already downloaded for the history metadata above -
			// also persist into the shared file_meta_cache, so File Manager
			// (a *live* view of what's currently on the printer, unlike
			// History) doesn't need its own network round-trip either. Cache
			// key is the bare path (RecentFile.Path convention, e.g.
			// "COREON~1.BGCODE") - filePath here is a full "/origin/path"
			// ref (Refs.Download), so strip the leading "/origin/" segment
			// to match what File Manager looks up. Reuses this file's
			// existing cache timestamp if one's already there instead of
			// always stamping a fresh time.Now() (the real upload timestamp
			// isn't known here) - keeps File Manager's own exact-match
			// lookup working across repeated prints of the same file.
			cachePath := cacheFilePath(filePath)
			cacheTimestamp := time.Now().Unix()
			if existing, hit, err := p.db.GetFileMetaCache(id, cachePath); err == nil && hit {
				cacheTimestamp = existing.UploadedAt
			}
			if toolsJSON != nil || len(info.Thumbnail) > 0 {
				p.db.SetFileMetaCache(id, cachePath, cacheTimestamp, toolsJSON, info.Thumbnail, info.ThumbnailContentType)
			}
		}
		p.insertPrintHistory(ctx, id, h, thumbnailURL)
	}()
}

// printResult decides a completed print's outcome. jobState is the plugin's
// own job-lifecycle field when it has one (PrusaLink's /api/v1/job:
// PRINTING/PAUSED/FINISHED/STOPPED/ERROR, straight from its OpenAPI spec) -
// an authoritative completed-vs-cancelled signal, preferred whenever
// present. Falls back to the last polled progress percentage only when no
// plugin JobState is available (OctoPrint) - that guess is wrong for a
// print that finishes between poll ticks, since a short print can easily
// have its last sample sitting well under 100% despite completing cleanly.
func printResult(jobState string, printerErrored bool, haveLastProgress bool, lastProgress float64) string {
	switch {
	case jobState == "STOPPED":
		return "cancelled"
	case jobState == "ERROR" || printerErrored:
		return "failed"
	case jobState == "FINISHED":
		return "completed"
	case jobState == "" && haveLastProgress && lastProgress < 99:
		return "cancelled"
	default:
		return "completed"
	}
}

func (p *Poller) insertPrintHistory(ctx context.Context, id int64, h *models.PrintHistory, thumbnailURL string) {
	if err := p.db.InsertPrintHistory(h); err != nil {
		log.Printf("[printer:%d] failed to record print history: %v", id, err)
		return
	}
	log.Printf("[printer:%d] recorded print history: %s (%s, %ds)", id, h.FileName, h.Result, h.DurationSecs)
	p.notifyPrintResult(ctx, id, h, thumbnailURL)

	if days := p.historyRetentionDays(); days > 0 {
		if err := p.db.PrunePrintHistory(days); err != nil {
			log.Printf("[printer:%d] failed to prune old print history: %v", id, err)
		}
	}
}

// historyRetentionDays is global-only (not per-printer) - 0 disables
// pruning entirely (default: keep everything).
func (p *Poller) historyRetentionDays() int {
	v, err := p.db.GetSetting("history_retention_days")
	if err != nil || v == "" {
		return 0
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0
	}
	return n
}
