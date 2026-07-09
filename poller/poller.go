package poller

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/ccmpbll/printspy/db"
	"github.com/ccmpbll/printspy/models"
	"github.com/ccmpbll/printspy/plugin"
	"github.com/ccmpbll/printspy/smartplug"
)

type polledPrinter struct {
	plugin plugin.PrinterPlugin
	cancel context.CancelFunc
	// idleSince tracks when this printer entered the idle state, for the
	// auto-off-after-idle-timeout feature. Zero value means "not idle" (or
	// auto-off already fired for this idle streak).
	idleSince time.Time
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
}

func (p *Poller) uploadLock(id int64) *sync.Mutex {
	l, _ := p.uploadLocks.LoadOrStore(id, &sync.Mutex{})
	return l.(*sync.Mutex)
}

func New(database *db.DB) *Poller {
	return &Poller{
		printers:    make(map[int64]*polledPrinter),
		cache:       make(map[int64]*models.PrinterStatus),
		db:          database,
		subscribers: make(map[*subscriber]struct{}),
	}
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

func (p *Poller) GetWebcamURL(id int64) string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if pp, ok := p.printers[id]; ok {
		return pp.plugin.GetWebcamURL()
	}
	return ""
}

// GetDisplayName returns the plugin's human-readable name for its printer
// type (e.g. "PrusaLink", "OctoPrint"), so callers don't need to hardcode
// a mapping from config.Type themselves.
func (p *Poller) GetDisplayName(id int64) string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if pp, ok := p.printers[id]; ok {
		return pp.plugin.DisplayName()
	}
	return ""
}

func (p *Poller) GetSnapshotURL(id int64) string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if pp, ok := p.printers[id]; ok {
		return pp.plugin.GetSnapshotURL()
	}
	return ""
}

// AuthenticatedDo proxies req through the printer's plugin, which applies
// whatever auth scheme that printer type needs - callers don't need to
// know or care which one.
func (p *Poller) AuthenticatedDo(id int64, client *http.Client, req *http.Request) (*http.Response, error) {
	p.mu.RLock()
	pp, ok := p.printers[id]
	p.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("printer %d not found", id)
	}
	return pp.plugin.AuthenticatedDo(client, req)
}

func (p *Poller) GetRecentFiles(ctx context.Context, id int64, limit int) ([]models.RecentFile, error) {
	p.mu.RLock()
	pp, ok := p.printers[id]
	p.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("printer %d not found", id)
	}
	files, err := pp.plugin.GetRecentFiles(ctx, limit)
	if err != nil {
		return nil, err
	}
	p.backfillFileStats(id, files)
	return files, nil
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
	p.mu.RLock()
	pp, ok := p.printers[id]
	p.mu.RUnlock()
	if !ok {
		return fmt.Errorf("printer %d not found", id)
	}
	lock := p.uploadLock(id)
	lock.Lock()
	defer lock.Unlock()
	return pp.plugin.UploadFile(ctx, storage, path, data, printAfter)
}

func (p *Poller) DeleteFile(ctx context.Context, id int64, storage, path string) error {
	p.mu.RLock()
	pp, ok := p.printers[id]
	p.mu.RUnlock()
	if !ok {
		return fmt.Errorf("printer %d not found", id)
	}
	return pp.plugin.DeleteFile(ctx, storage, path)
}

func (p *Poller) StartPrint(ctx context.Context, id int64, location, path string) error {
	p.mu.RLock()
	pp, ok := p.printers[id]
	p.mu.RUnlock()
	if !ok {
		return fmt.Errorf("printer %d not found", id)
	}
	return pp.plugin.StartPrint(ctx, location, path)
}

func (p *Poller) ControlPrint(ctx context.Context, id int64, action string) error {
	p.mu.RLock()
	pp, ok := p.printers[id]
	p.mu.RUnlock()
	if !ok {
		return fmt.Errorf("printer %d not found", id)
	}
	switch action {
	case "pause":
		return pp.plugin.PausePrint(ctx)
	case "resume":
		return pp.plugin.ResumePrint(ctx)
	case "cancel":
		return pp.plugin.CancelPrint(ctx)
	default:
		return fmt.Errorf("unknown action: %s", action)
	}
}

// Repoll re-polls a printer immediately instead of waiting for the next
// scheduled tick. Used when something outside the poll loop changes a
// printer's smart plug assignment/label, so the dashboard reflects it right
// away rather than sitting stale until the next tick.
func (p *Poller) Repoll(ctx context.Context, id int64) {
	p.mu.RLock()
	pp, ok := p.printers[id]
	p.mu.RUnlock()
	if !ok {
		return
	}
	p.poll(ctx, id, pp.plugin)
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
	if p.isOnline(id) {
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
			if p.isOnline(id) {
				return nil
			}
		}
	}
}

func (p *Poller) isOnline(id int64) bool {
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
	p.mu.RLock()
	pp, ok := p.printers[id]
	p.mu.RUnlock()
	if !ok {
		return fmt.Errorf("printer %d not found", id)
	}

	plugs, err := p.db.ListSmartPlugs(id)
	if err != nil {
		return err
	}

	var setErr error
	direct := false
	for _, sp := range plugs {
		if sp.IP+":"+sp.Idx == plugID {
			setErr = smartplug.New().SetState(ctx, sp.IP, sp.Idx, on)
			direct = true
			break
		}
	}
	if !direct {
		setErr = pp.plugin.SetPowerState(ctx, plugID, on)
	}

	if setErr == nil {
		p.patchPowerState(id, plugID, "", on)
	}
	go p.poll(context.Background(), id, pp.plugin)
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
	status, err := pl.GetStatus(ctx)
	if err != nil {
		log.Printf("[printer:%d] poll error: %v", id, err)
		status = &models.PrinterStatus{
			State:       models.StateOffline,
			LastUpdated: time.Now(),
		}
	}

	if plugs, err := p.db.ListSmartPlugs(id); err == nil && len(plugs) > 0 {
		status.Power = append(status.Power, p.fetchDirectPower(ctx, id, plugs)...)
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

	p.trackPrintHistory(id, prevState, status, prev)
	p.checkAutoOff(ctx, id, status)
	p.checkThermalRunaway(ctx, id, status)
	p.checkIngestOnline(ctx, id, prevState, status)

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
	if !p.isOnline(printerID) {
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
		return // still cooling down, check again next tick
	}

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
	client := smartplug.New()
	for _, sp := range plugs {
		plugID := sp.IP + ":" + sp.Idx
		if !p.isPlugOn(id, plugID) {
			continue
		}
		if err := client.SetState(ctx, sp.IP, sp.Idx, false); err != nil {
			log.Printf("[printer:%d] auto power-off (%s) failed for %s: %v", id, source, plugID, err)
			continue
		}
		log.Printf("[printer:%d] auto power-off (%s): %s", id, source, plugID)
		p.patchPowerState(id, plugID, source, false)
	}
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
		ps, err := client.GetState(ctx, sp.IP, sp.Idx, sp.Label, sp.HideLabel)
		if err != nil {
			log.Printf("[printer:%d] smart plug %s:%s unreachable: %v", id, sp.IP, sp.Idx, err)
			continue
		}
		states = append(states, *ps)
	}
	return states
}

func (p *Poller) trackPrintHistory(id int64, prevState models.PrinterState, status *models.PrinterStatus, prev *models.PrinterStatus) {
	wasPrinting := prevState == models.StatePrinting || prevState == models.StatePaused
	nowDone := status.State == models.StateIdle || status.State == models.StateError || status.State == models.StateOffline

	if !wasPrinting || !nowDone {
		return
	}

	fileName := ""
	elapsed := 0
	filament := 0.0
	if prev != nil && prev.Job != nil {
		fileName = prev.Job.FileName
		elapsed = prev.Job.ElapsedSecs
		filament = prev.Job.FilamentUsedMM
	}
	if fileName == "" {
		return
	}

	result := "completed"
	if status.State == models.StateError {
		result = "failed"
	} else if prev != nil && prev.Job != nil && prev.Job.Progress < 99 {
		result = "cancelled"
	}

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
	if err := p.db.InsertPrintHistory(h); err != nil {
		log.Printf("[printer:%d] failed to record print history: %v", id, err)
	} else {
		log.Printf("[printer:%d] recorded print history: %s (%s, %ds)", id, fileName, result, elapsed)
	}
}
