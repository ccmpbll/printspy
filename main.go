package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/ccmpbll/printspy/api"
	"github.com/ccmpbll/printspy/config"
	"github.com/ccmpbll/printspy/db"
	"github.com/ccmpbll/printspy/poller"

	_ "github.com/ccmpbll/printspy/plugin/octoprint"
)

func main() {
	cfg, err := config.Load("")
	if err != nil {
		log.Printf("warning: config load: %v (using defaults)", err)
	}

	if err := os.MkdirAll(cfg.Server.DataDir, 0755); err != nil {
		log.Fatalf("failed to create data directory: %v", err)
	}

	dbPath := filepath.Join(cfg.Server.DataDir, "printspy.db")
	database, err := db.Open(dbPath)
	if err != nil {
		log.Fatalf("failed to open database: %v", err)
	}
	defer database.Close()

	seedFromConfig(database, cfg)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	p := poller.New(database)
	if err := p.Start(ctx); err != nil {
		log.Fatalf("failed to start poller: %v", err)
	}

	handler := api.New(ctx, database, p)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	webDir := findWebDir()
	mux.Handle("/", http.FileServer(http.Dir(webDir)))

	addr := fmt.Sprintf(":%d", cfg.Server.Port)
	server := &http.Server{Addr: addr, Handler: mux}

	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		log.Println("shutting down...")
		cancel()
		server.Close()
	}()

	log.Printf("PrintSpy starting on http://0.0.0.0%s", addr)
	if err := server.ListenAndServe(); err != http.ErrServerClosed {
		log.Fatalf("server error: %v", err)
	}
}

func seedFromConfig(database *db.DB, cfg *config.Config) {
	for _, p := range cfg.Printers {
		exists, err := database.PrinterExistsByURL(p.URL)
		if err != nil {
			log.Printf("warning: checking printer existence: %v", err)
			continue
		}
		if exists {
			continue
		}
		if p.Type == "" {
			p.Type = "octoprint"
		}
		if p.PollInterval <= 0 {
			p.PollInterval = 10
		}
		p.Enabled = true
		if err := database.CreatePrinter(&p); err != nil {
			log.Printf("warning: seeding printer %s: %v", p.Name, err)
		} else {
			log.Printf("seeded printer from config: %s (%s)", p.Name, p.URL)
		}
	}
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
