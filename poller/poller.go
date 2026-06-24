package poller

import (
	"context"
	"encoding/json"
	"log"
	"sync"
	"time"

	"github.com/ccmpbll/printspy/db"
	"github.com/ccmpbll/printspy/models"
	"github.com/ccmpbll/printspy/plugin"
)

type polledPrinter struct {
	plugin plugin.PrinterPlugin
	cancel context.CancelFunc
}

type subscriber struct {
	ch     chan []byte
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

	interval := time.Duration(config.PollInterval) * time.Second
	if interval < time.Second {
		interval = 10 * time.Second
	}

	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		p.pollLoop(ctx, config.ID, config.Name, pp.plugin, interval)
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
		ch:     make(chan []byte, 64),
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

func (s *subscriber) Chan() <-chan []byte {
	return s.ch
}

func (s *subscriber) Done() <-chan struct{} {
	return s.ctx.Done()
}

func (p *Poller) BroadcastRefresh() {
	data := []byte(`{"refresh":true}`)
	p.subMu.Lock()
	defer p.subMu.Unlock()
	for s := range p.subscribers {
		select {
		case s.ch <- data:
		default:
		}
	}
}

func (p *Poller) broadcast(printerID int64, status *models.PrinterStatus) {
	msg := struct {
		PrinterID int64                `json:"printer_id"`
		Status    *models.PrinterStatus `json:"status"`
	}{printerID, status}

	data, err := json.Marshal(msg)
	if err != nil {
		return
	}

	p.subMu.Lock()
	defer p.subMu.Unlock()
	for s := range p.subscribers {
		select {
		case s.ch <- data:
		default:
			// subscriber too slow, skip
		}
	}
}

func (p *Poller) pollLoop(ctx context.Context, id int64, name string, pl plugin.PrinterPlugin, interval time.Duration) {
	log.Printf("starting poller for printer %d (%s) every %s", id, name, interval)
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

	p.broadcast(id, status)
}
