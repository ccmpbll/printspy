package poller

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
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
		if printer.Enabled {
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
		p.pollLoop(ctx, config.ID, config.Name, pp, interval)
	}()
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

func (p *Poller) GetSnapshotURL(id int64) string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if pp, ok := p.printers[id]; ok {
		return pp.plugin.GetSnapshotURL()
	}
	return ""
}

func (p *Poller) GetRecentFiles(ctx context.Context, id int64, limit int) ([]models.RecentFile, error) {
	p.mu.RLock()
	pp, ok := p.printers[id]
	p.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("printer %d not found", id)
	}
	return pp.plugin.GetRecentFiles(ctx, limit)
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

// SetPowerState toggles a plug, then kicks off a re-poll in the background
// so the dashboard reflects the change without waiting for the next
// scheduled poll tick. The re-poll runs async — it includes a full printer
// status fetch (pl.GetStatus), which can take up to that plugin's HTTP
// timeout if the printer is slow or unreachable, and toggling a plug
// shouldn't block on that.
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

	go p.poll(context.Background(), id, pp)
	return setErr
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

func (p *Poller) pollLoop(ctx context.Context, id int64, name string, pp *polledPrinter, interval time.Duration) {
	log.Printf("starting poller for printer %d (%s) every %s", id, name, interval)

	if err := pp.plugin.Connect(ctx); err != nil {
		log.Printf("[printer:%d] initial connect failed: %v (will retry on poll)", id, err)
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	p.poll(ctx, id, pp)

	for {
		select {
		case <-ctx.Done():
			log.Printf("stopping poller for printer %d (%s)", id, name)
			return
		case <-ticker.C:
			p.poll(ctx, id, pp)
		}
	}
}

func (p *Poller) poll(ctx context.Context, id int64, pp *polledPrinter) {
	status, err := pp.plugin.GetStatus(ctx)
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
		ps, err := client.GetState(ctx, sp.IP, sp.Idx, sp.Label)
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
