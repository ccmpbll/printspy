package api

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ccmpbll/printspy/db"
	"github.com/ccmpbll/printspy/models"
)

func TestHistoryLimitIsCapped(t *testing.T) {
	h, d := newTestHandler(t)
	p := &models.PrinterConfig{Name: "p", Type: "octoprint", URL: "http://x", APIKey: "k", PollInterval: 10}
	if err := d.CreatePrinter(p); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < maxHistoryLimit+20; i++ {
		if err := d.InsertPrintHistory(&models.PrintHistory{PrinterID: p.ID, FileName: "f", Result: "completed"}); err != nil {
			t.Fatal(err)
		}
	}
	rec := httptest.NewRecorder()
	h.getPrintHistoryList(rec, httptest.NewRequest("GET", "/?limit=9223372036854775807", nil), p.ID)
	var out struct {
		Entries []json.RawMessage `json:"entries"`
		HasMore bool              `json:"has_more"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Entries) != maxHistoryLimit || !out.HasMore {
		t.Errorf("entries=%d has_more=%v, want %d/true", len(out.Entries), out.HasMore, maxHistoryLimit)
	}
}

func TestDeleteUserRemovesSessionsAndReportsMissing(t *testing.T) {
	_, d := newTestHandler(t)
	id, err := d.CreateUser("bob", "x")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.CreateSession("tok", "bob", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := d.DeleteUser(id); err != nil {
		t.Fatal(err)
	}
	if u, _ := d.GetSessionUser("tok"); u != "" {
		t.Errorf("session survived user deletion for %q", u)
	}
	if err := d.DeleteUser(id); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("second delete err = %v, want ErrNotFound", err)
	}
}

func TestPutSettingsIsAllOrNothing(t *testing.T) {
	h, d := newTestHandler(t)
	for i := 0; i < 50; i++ { // map order is random; every run must persist nothing
		rec := do(h.handleSettings, "PUT", "/api/settings", `{"mqtt_broker_url":"ftp://bad","status_api_key":"short","auto_off_idle_minutes":"30"}`)
		if rec.Code != 400 {
			t.Fatalf("code = %d, want 400", rec.Code)
		}
		if v, _ := d.GetSetting("auto_off_idle_minutes"); v != "" {
			t.Fatalf("run %d: setting persisted despite 400 (%q)", i, v)
		}
	}
	if rec := do(h.handleSettings, "PUT", "/api/settings", `{"auto_off_idle_minutes":"30"}`); rec.Code != 204 {
		t.Fatalf("valid PUT code = %d", rec.Code)
	}
	if v, _ := d.GetSetting("auto_off_idle_minutes"); v != "30" {
		t.Errorf("valid setting not saved: %q", v)
	}
}
