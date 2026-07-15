// Backfill printmeta (material, filament, layer height, tool usage, path/
// uploaded_at, and a file_meta_cache row) onto print_history rows that
// predate the print-history feature or the later file-metadata-caching
// feature. Only touches rows missing material or path (the pre-feature
// defaults) and only if the matching file is still on the printer's
// storage - never overwrites already-populated rows, safe to re-run.
//
// Built into the runtime image (see Dockerfile) - run it directly against
// the live container:
//
//	docker exec printspy backfill-history
package main

import (
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"github.com/ccmpbll/printspy/digestauth"
	"github.com/ccmpbll/printspy/printmeta"
)

type printerRow struct {
	ID       int64
	Name     string
	URL      string
	Username string
	APIKey   string
}

type historyRow struct {
	ID       int64
	Filename string
}

type fileRef struct {
	Storage    string
	Name       string // 8.3 short name used in the download path
	MTimestamp int64  // matches file_meta_cache's uploaded_at freshness key
}

func main() {
	dbPath := flag.String("db", "/data/printspy.db", "sqlite db path")
	flag.Parse()

	db, err := sql.Open("sqlite3", *dbPath)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer db.Close()

	printers, err := listPrusalinkPrinters(db)
	if err != nil {
		log.Fatalf("list printers: %v", err)
	}
	if len(printers) == 0 {
		fmt.Println("no PrusaLink printers found")
		return
	}

	for _, pr := range printers {
		fmt.Printf("\n=== %s (id %d) ===\n", pr.Name, pr.ID)
		if err := backfillPrinter(db, pr); err != nil {
			log.Printf("[%s] %v", pr.Name, err)
		}
	}
}

func listPrusalinkPrinters(db *sql.DB) ([]printerRow, error) {
	rows, err := db.Query(`SELECT id, name, url, username, api_key FROM printers WHERE type='prusalink'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []printerRow
	for rows.Next() {
		var pr printerRow
		if err := rows.Scan(&pr.ID, &pr.Name, &pr.URL, &pr.Username, &pr.APIKey); err != nil {
			return nil, err
		}
		out = append(out, pr)
	}
	return out, rows.Err()
}

func backfillPrinter(db *sql.DB, pr printerRow) error {
	rows, err := listCandidateRows(db, pr.ID)
	if err != nil {
		return fmt.Errorf("list candidate rows: %w", err)
	}
	if len(rows) == 0 {
		fmt.Println("nothing to backfill")
		return nil
	}
	fmt.Printf("%d row(s) missing metadata\n", len(rows))

	files, err := listFiles(pr)
	if err != nil {
		return fmt.Errorf("list printer files: %w", err)
	}

	updated, skipped := 0, 0
	for _, row := range rows {
		ref, ok := files[row.Filename]
		if !ok {
			fmt.Printf("  skip (not on printer anymore): %s\n", row.Filename)
			skipped++
			continue
		}
		data, err := downloadFile(pr, ref)
		if err != nil {
			fmt.Printf("  skip (download failed): %s: %v\n", row.Filename, err)
			skipped++
			continue
		}
		info, err := printmeta.Parse(row.Filename, data)
		if err != nil {
			fmt.Printf("  skip (parse failed): %s: %v\n", row.Filename, err)
			skipped++
			continue
		}
		if err := updateRow(db, row.ID, ref, info); err != nil {
			fmt.Printf("  skip (db update failed): %s: %v\n", row.Filename, err)
			skipped++
			continue
		}
		// Same bytes already downloaded above - also seeds file_meta_cache
		// (tools_json for every tool, not just multi-tool - matches File
		// Manager's convention, broader than print_history's own Tools
		// column) so History shows a real thumbnail for this row too.
		var allToolsJSON []byte
		if len(info.Tools) > 0 {
			allToolsJSON, _ = json.Marshal(info.Tools)
		}
		if allToolsJSON != nil || len(info.Thumbnail) > 0 {
			if err := upsertFileMetaCache(db, pr.ID, ref.Name, ref.MTimestamp, allToolsJSON, info.Thumbnail, info.ThumbnailContentType); err != nil {
				fmt.Printf("  warn: file_meta_cache upsert failed for %s: %v\n", row.Filename, err)
			}
		}
		fmt.Printf("  updated: %s (%s, %.0fg, $%.2f)\n", row.Filename, info.Material, info.FilamentUsedG, info.FilamentCost)
		updated++
	}
	fmt.Printf("done: %d updated, %d skipped\n", updated, skipped)
	return nil
}

func listCandidateRows(db *sql.DB, printerID int64) ([]historyRow, error) {
	rows, err := db.Query(`SELECT id, filename FROM print_history WHERE printer_id=? AND (material='' OR path='')`, printerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []historyRow
	for rows.Next() {
		var r historyRow
		if err := rows.Scan(&r.ID, &r.Filename); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func updateRow(db *sql.DB, id int64, ref fileRef, info *printmeta.Info) error {
	// tools_json only gets written for genuinely multi-tool prints, matching
	// poller.go's trackPrintHistory convention - single-tool rows leave it
	// empty rather than a redundant 1-element array.
	toolsJSON := ""
	if len(info.Tools) > 1 {
		if b, err := json.Marshal(info.Tools); err == nil {
			toolsJSON = string(b)
		}
	}

	_, err := db.Exec(`UPDATE print_history SET
		layer_height=?, fill_density=?, printer_model=?, material=?, tool_index=?,
		filament_used_g=?, filament_cost=?, estimated_duration_secs=?, max_layer_z=?, object_names=?,
		tool_changes=?, tools_json=?, path=?, uploaded_at=?
		WHERE id=?`,
		info.LayerHeightMM, info.FillDensity, info.PrinterModel, info.Material, info.ToolIndex,
		info.FilamentUsedG, info.FilamentCost, info.EstimatedSecs, info.MaxLayerZ, strings.Join(info.ObjectNames, ", "),
		info.ToolChanges, toolsJSON, ref.Name, ref.MTimestamp,
		id)
	return err
}

// upsertFileMetaCache is a standalone copy of db.DB's own SetFileMetaCache
// SQL (db/db.go) - same rationale as digestGet below, not worth pulling in
// the app's internal db package for one query.
func upsertFileMetaCache(db *sql.DB, printerID int64, path string, uploadedAt int64, toolsJSON []byte, thumbnail []byte, thumbnailContentType string) error {
	_, err := db.Exec(`
		INSERT INTO file_meta_cache (printer_id, path, uploaded_at, tools_json, thumbnail, thumbnail_content_type)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(printer_id, path) DO UPDATE SET
			uploaded_at = excluded.uploaded_at,
			tools_json = CASE WHEN excluded.tools_json != '' THEN excluded.tools_json ELSE file_meta_cache.tools_json END,
			thumbnail = CASE WHEN excluded.thumbnail IS NOT NULL THEN excluded.thumbnail ELSE file_meta_cache.thumbnail END,
			thumbnail_content_type = CASE WHEN excluded.thumbnail IS NOT NULL THEN excluded.thumbnail_content_type ELSE file_meta_cache.thumbnail_content_type END
	`, printerID, path, uploadedAt, string(toolsJSON), thumbnail, thumbnailContentType)
	return err
}

// listFiles walks both storages and returns a map of display name -> ref,
// mirroring plugin/prusalink.Plugin.GetRecentFiles (unexported, so this
// duplicates the small walk rather than reaching into the app's internals).
func listFiles(pr printerRow) (map[string]fileRef, error) {
	out := map[string]fileRef{}
	for _, storage := range []string{"local", "usb"} {
		data, err := digestGet(pr, "/api/v1/files/"+storage)
		if err != nil {
			continue // no USB drive plugged in is normal, not fatal
		}
		var resp struct {
			Children []struct {
				Name        string `json:"name"`
				DisplayName string `json:"display_name"`
				Type        string `json:"type"`
				MTimestamp  int64  `json:"m_timestamp"`
				Children    []struct {
					Name        string `json:"name"`
					DisplayName string `json:"display_name"`
					Type        string `json:"type"`
					MTimestamp  int64  `json:"m_timestamp"`
				} `json:"children"`
			} `json:"children"`
		}
		if json.Unmarshal(data, &resp) != nil {
			continue
		}
		for _, f := range resp.Children {
			if f.Type == "PRINT_FILE" {
				out[f.DisplayName] = fileRef{Storage: storage, Name: f.Name, MTimestamp: f.MTimestamp}
			}
			for _, c := range f.Children {
				if c.Type == "PRINT_FILE" {
					out[c.DisplayName] = fileRef{Storage: storage, Name: c.Name, MTimestamp: c.MTimestamp}
				}
			}
		}
	}
	return out, nil
}

func downloadFile(pr printerRow, ref fileRef) ([]byte, error) {
	return digestGet(pr, "/"+ref.Storage+"/"+ref.Name)
}

// digestGet is a minimal standalone copy of prusalink.Plugin's own
// GET-with-digest-retry logic (unexported there) - fine for a small
// stand-alone tool, not worth exporting new API surface in the app for it.
func digestGet(pr printerRow, path string) ([]byte, error) {
	username := pr.Username
	if username == "" {
		username = "maker"
	}
	url := strings.TrimRight(pr.URL, "/") + path
	client := &http.Client{Timeout: 60 * time.Second}

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		authHeader := resp.Header.Get("WWW-Authenticate")
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()

		req2, err := http.NewRequest(http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		req2.Header.Set("Accept", "application/json")
		req2.Header.Set("Authorization", digestauth.BuildHeader(username, pr.APIKey, http.MethodGet, req2.URL.RequestURI(), authHeader))

		resp2, err := client.Do(req2)
		if err != nil {
			return nil, err
		}
		defer resp2.Body.Close()
		if resp2.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("http %d", resp2.StatusCode)
		}
		return io.ReadAll(resp2.Body)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("http %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}
