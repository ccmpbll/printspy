package poller

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
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
		p.patchPowerState(id, plugID, on)
	}
	go p.poll(context.Background(), id, pp.plugin)
	return setErr
}

// patchPowerState flips a single plug's on/off value in the cached status
// and broadcasts it, without waiting on a full printer poll.
func (p *Poller) patchPowerState(id int64, plugID string, on bool) {
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

	p.broadcast(id, status)
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
