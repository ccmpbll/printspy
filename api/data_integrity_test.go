package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ccmpbll/printspy/db"
	"github.com/ccmpbll/printspy/models"
	"github.com/ccmpbll/printspy/poller"
)

func newTestHandler(t *testing.T) (*Handler, *db.DB) {
	t.Helper()
	d, err := db.Open(t.TempDir() + "/t.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return New(context.Background(), d, poller.New(d)), d
}

func do(h http.HandlerFunc, method, path, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
	return rec
}

func TestUpdatePrinterRejectsPartialBodyAndMissingID(t *testing.T) {
	h, d := newTestHandler(t)
	p := models.PrinterConfig{Name: "P", Type: "prusalink", URL: "http://10.0.0.1", APIKey: "k", PollInterval: 10, Enabled: true}
	if err := d.CreatePrinter(&p); err != nil {
		t.Fatal(err)
	}

	if rec := do(h.handlePrinterByID, "PUT", "/api/printers/1", `{"name":"renamed"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("partial PUT = %d, want 400", rec.Code)
	}
	got, _ := d.GetPrinter(p.ID)
	if got.URL != "http://10.0.0.1" || got.APIKey != "k" || got.Type != "prusalink" {
		t.Errorf("partial PUT blanked the row: %+v", got)
	}

	full := `{"name":"P2","url":"http://10.0.0.2","api_key":"k2","enabled":true}`
	if rec := do(h.handlePrinterByID, "PUT", "/api/printers/1", full); rec.Code != http.StatusOK {
		t.Errorf("full PUT = %d, want 200", rec.Code)
	}
	if got, _ := d.GetPrinter(p.ID); got.Type != "prusalink" {
		t.Errorf("empty type should keep existing, got %q", got.Type)
	}

	if rec := do(h.handlePrinterByID, "PUT", "/api/printers/999", full); rec.Code != http.StatusNotFound {
		t.Errorf("PUT missing = %d, want 404", rec.Code)
	}
	if rec := do(h.handlePrinterByID, "DELETE", "/api/printers/999", ""); rec.Code != http.StatusNotFound {
		t.Errorf("DELETE missing = %d, want 404", rec.Code)
	}
}

func TestConfigImportReportsSkippedItems(t *testing.T) {
	h, _ := newTestHandler(t)
	yaml := `
settings:
  poll_interval: "not-a-number"
  debug_logging: "0"
printers:
  - name: ok
    url: http://10.0.0.1
    api_key: k
    enabled: true
  - name: nokey
    url: http://10.0.0.2
`
	rec := do(h.handleConfigImport, "POST", "/api/config/import", yaml)
	var resp struct {
		Success       bool     `json:"success"`
		Skipped       []string `json:"skipped"`
		PrintersAdded int      `json:"printers_added"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Success || len(resp.Skipped) != 2 || resp.PrintersAdded != 1 {
		t.Errorf("got success=%v skipped=%v added=%d, want false/2 skipped/1", resp.Success, resp.Skipped, resp.PrintersAdded)
	}
}

func TestConfigImportRejectsOversizeBody(t *testing.T) {
	h, _ := newTestHandler(t)
	rec := do(h.handleConfigImport, "POST", "/api/config/import", "settings:\n  x: "+strings.Repeat("a", maxConfigImportBytes+10))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("oversize import = %d, want 400", rec.Code)
	}
}
