package plugin

import (
	"context"
	"fmt"
	"net/http"
	"sync"

	"github.com/ccmpbll/printspy/models"
)

type PrinterPlugin interface {
	Type() string
	DisplayName() string
	Connect(ctx context.Context) error
	GetStatus(ctx context.Context) (*models.PrinterStatus, error)
	GetWebcamURL() string
	GetSnapshotURL() string
	GetThumbnailURL(ctx context.Context) string
	GetPrinterName(ctx context.Context) string
	SetPowerState(ctx context.Context, plugID string, on bool) error
	// GetRecentFiles returns files newest-first, capped at limit - limit <= 0
	// means unlimited (the file manager's "all files" view).
	GetRecentFiles(ctx context.Context, limit int) ([]models.RecentFile, error)
	// UploadFile returns the printer's own resolved storage path and
	// timestamp for the uploaded file - PrusaLink renames on write to an
	// 8.3-mangled short name (e.g. "COREON~1.BGC"), different from path,
	// and every other read path (listings, thumbnails, deletes) keys on
	// that real name, not the one this was called with. Empty/zero when a
	// plugin doesn't rename on write (OctoPrint - path is already correct).
	UploadFile(ctx context.Context, storage, path string, data []byte, printAfter bool) (realPath string, uploadedAt int64, err error)
	DeleteFile(ctx context.Context, storage, path string) error
	// DownloadFile returns a file's raw bytes for the user to save locally -
	// same storage/path convention as DeleteFile/StartPrint.
	DownloadFile(ctx context.Context, storage, path string) ([]byte, error)
	StartPrint(ctx context.Context, location, path string) error
	PausePrint(ctx context.Context) error
	ResumePrint(ctx context.Context) error
	CancelPrint(ctx context.Context) error

	// AuthenticatedDo applies this plugin's auth scheme to req (and retries
	// with a challenge-based scheme like HTTP Digest if the printer demands
	// it) before performing it with client. Lets callers proxy arbitrary
	// printer resources (snapshots, thumbnails) without knowing how any
	// given plugin authenticates.
	AuthenticatedDo(client *http.Client, req *http.Request) (*http.Response, error)
}

// Keepalive is an optional capability a plugin can implement if its printer
// type benefits from an out-of-band network keepalive (e.g. a wifi
// interface known to drop after idle periods). Not part of PrinterPlugin
// itself since most plugin types won't need it.
type Keepalive interface {
	// KeepaliveHost returns the host to ping and whether keepalive applies.
	KeepaliveHost() (string, bool)
}

// MetadataDownloader is an optional capability for plugins whose printer
// type keeps a completed print's own metadata (material, filament used,
// layer height, ...) embedded in the file itself rather than exposing it
// via the API (PrusaLink). Not part of PrinterPlugin since OctoPrint
// already exposes richer job metadata through its own API and has no
// equivalent need.
type MetadataDownloader interface {
	// DownloadFileForMetadata fetches path (a plugin-native ref, e.g.
	// models.JobInfo.FilePath) for the purpose of extracting print
	// metadata from it - displayName (the human-readable filename) decides
	// whether the whole file is needed or just its leading bytes.
	DownloadFileForMetadata(ctx context.Context, path, displayName string) ([]byte, error)
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
