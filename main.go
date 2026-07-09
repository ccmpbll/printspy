package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"

	"github.com/ccmpbll/printspy/api"
	"github.com/ccmpbll/printspy/db"
	"github.com/ccmpbll/printspy/ingest"
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

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	p := poller.New(database)
	if err := p.Start(ctx); err != nil {
		log.Fatalf("failed to start poller: %v", err)
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
	server := &http.Server{Addr: addr, Handler: nosniff(handler.RequireAuth(mux))}

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
