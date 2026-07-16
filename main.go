package main

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/ccmpbll/printspy/api"
	"github.com/ccmpbll/printspy/db"
	"github.com/ccmpbll/printspy/ingest"
	"github.com/ccmpbll/printspy/logging"
	"github.com/ccmpbll/printspy/poller"

	_ "github.com/ccmpbll/printspy/plugin/octoprint"
	_ "github.com/ccmpbll/printspy/plugin/prusalink"
)

var version = "dev"

func main() {
	port := 8080
	dataDir := "/data"

	if envPort := os.Getenv("PRINTSPY_PORT"); envPort != "" {
		if p, err := strconv.Atoi(envPort); err == nil && p > 0 {
			port = p
		}
	}
	if envDataDir := os.Getenv("PRINTSPY_DATA_DIR"); envDataDir != "" {
		dataDir = envDataDir
	}

	if err := os.MkdirAll(dataDir, 0755); err != nil {
		log.Fatalf("failed to create data directory: %v", err)
	}

	dbPath := filepath.Join(dataDir, "printspy.db")
	database, err := db.Open(dbPath)
	if err != nil {
		log.Fatalf("failed to open database: %v", err)
	}
	defer database.Close()

	if v, _ := database.GetSetting("debug_logging"); v == "1" {
		logging.SetDebug(true)
		log.Print("debug logging enabled")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	p := poller.New(database)
	if err := p.Start(ctx); err != nil {
		log.Fatalf("failed to start poller: %v", err)
	}
	if err := p.ConfigureMQTT(); err != nil {
		log.Printf("mqtt: initial connect failed (will retry via settings save): %v", err)
	}

	handler := api.New(ctx, database, p)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	ingestHandler := ingest.New(database, dataDir)
	ingestHandler.SetDispatchFunc(func(jobID, printerID int64) {
		go handler.AutoDispatchIngestJob(jobID, printerID)
	})
	ingestHandler.SetRelayFunc(func(jobID, printerID int64) {
		go p.RelayIngestJob(ctx, jobID, printerID)
	})
	ingestHandler.SetBroadcastFunc(p.BroadcastRefresh)
	ingestHandler.RegisterRoutes(mux)
	mux.HandleFunc("/api/version", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"version":%q}`, version)
	})

	webDir := findWebDir()
	mux.Handle("/", http.FileServer(http.Dir(webDir)))

	addr := fmt.Sprintf(":%d", port)
	server := &http.Server{Addr: addr, Handler: logRequests(nosniff(handler.RequireAuth(mux)))}

	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		log.Println("shutting down...")
		cancel()
		p.Wait()
		server.Close()
	}()

	log.Printf("PrintSpy starting on http://0.0.0.0%s", addr)
	if err := server.ListenAndServe(); err != http.ErrServerClosed {
		log.Fatalf("server error: %v", err)
	}
}

// logRequests logs every request at debug level (method, path, status,
// duration) - a no-op cost when debug logging is off (slog drops it before
// formatting), the cheapest way to get visibility into "pretty much
// everything" hitting the API without threading logging through every
// handler individually.
func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		slog.Debug("request", "method", r.Method, "path", r.URL.Path, "status", sw.status, "duration", time.Since(start))
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

// Flush forwards to the underlying ResponseWriter's Flusher, if it has one.
// Without this, wrapping breaks SSE - handleSSE type-asserts w.(http.Flusher)
// and, finding a *statusWriter that doesn't implement it, bails out with
// "streaming not supported" on every connection attempt. The dashboard's
// printer list is populated from the SSE init event, not a REST fetch on
// page load, so this alone made every printer disappear from the UI - the
// data was never touched, the live connection just never came up.
func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *statusWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func nosniff(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(w, r)
	})
}

func findWebDir() string {
	candidates := []string{
		"web",
		"/usr/local/share/printspy/web",
	}
	for _, dir := range candidates {
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			return dir
		}
	}
	log.Println("warning: web directory not found, static files will not be served")
	return "web"
}
