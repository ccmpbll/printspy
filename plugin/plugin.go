package plugin

import (
	"context"
	"fmt"
	"sync"

	"github.com/ccmpbll/printspy/models"
)

type PrinterPlugin interface {
	Type() string
	Connect(ctx context.Context) error
	GetStatus(ctx context.Context) (*models.PrinterStatus, error)
	GetWebcamURL() string
	GetSnapshotURL() string
	GetThumbnailURL(ctx context.Context) string
	GetPrinterName(ctx context.Context) string
	SetPowerState(ctx context.Context, on bool) error
	GetRecentFiles(ctx context.Context, limit int) ([]models.RecentFile, error)
	StartPrint(ctx context.Context, location, path string) error
	PausePrint(ctx context.Context) error
	ResumePrint(ctx context.Context) error
	CancelPrint(ctx context.Context) error
}

type PluginFactory func(config models.PrinterConfig) PrinterPlugin

var (
	mu       sync.RWMutex
	registry = map[string]PluginFactory{}
)

func Register(pluginType string, factory PluginFactory) {
	mu.Lock()
	defer mu.Unlock()
	registry[pluginType] = factory
}

func Create(config models.PrinterConfig) (PrinterPlugin, error) {
	mu.RLock()
	defer mu.RUnlock()
	factory, ok := registry[config.Type]
	if !ok {
		return nil, fmt.Errorf("unknown printer plugin type: %s", config.Type)
	}
	return factory(config), nil
}

func RegisteredTypes() []string {
	mu.RLock()
	defer mu.RUnlock()
	types := make([]string, 0, len(registry))
	for t := range registry {
		types = append(types, t)
	}
	return types
}
