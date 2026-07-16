package api

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ccmpbll/printspy/db"
	"github.com/ccmpbll/printspy/models"
	"github.com/ccmpbll/printspy/poller"
)

// Guards config export/import against silently falling behind new features -
// seeds a DB with one of everything (maintenance mode, HTTP + MQTT smart
// plugs, a camera, an ingest target, a representative settings spread),
// exports, imports into a fresh DB, and checks it all landed.
func TestConfigExportImportRoundTrip(t *testing.T) {
	src, err := db.Open(t.TempDir() + "/src.db")
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()

	p := models.PrinterConfig{
		Name: "Core One", Type: "prusalink", Model: "Core One", HideModel: true,
		URL: "http://10.0.0.1", APIKey: "printer-key", Username: "maker",
		PollInterval: 10, Enabled: true,
		IdleTimeoutMinutes: 30, MaxBedTemp: 110, MaxExtruderTemp: 280,
	}
	if err := src.CreatePrinter(&p); err != nil {
		t.Fatal(err)
	}
	if err := src.SetMaintenance(p.ID, true); err != nil {
		t.Fatal(err)
	}

	if _, err := src.CreateSmartPlug("192.168.1.50:80", "1", "Printer Plug", false, &p.ID, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := src.CreateSmartPlug("", "2", "MQTT Plug", true, &p.ID, "tasmota_topic"); err != nil {
		t.Fatal(err)
	}
	if _, err := src.CreateCamera("http://192.168.1.60", "Bed Cam", &p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := src.CreateIngestTarget("Core One", &p.ID, "core-one", "ingest-key-123"); err != nil {
		t.Fatal(err)
	}

	wantSettings := map[string]string{
		"mqtt_broker_url":       "tcp://192.168.1.10:1883",
		"mqtt_username":         "mqttuser",
		"mqtt_password":         "mqttpass",
		"pushover_user_key":     "pushoveruser",
		"pushover_app_token":    "pushovertoken",
		"auto_off_idle_minutes": "45",
		"notify_on_complete":    "1",
		"notify_complete_image": "thumbnail",
	}
	for k, v := range wantSettings {
		validated, err := validateSetting(k, v)
		if err != nil {
			t.Fatalf("validateSetting(%s): %v", k, err)
		}
		if err := src.SetSetting(k, validated); err != nil {
			t.Fatal(err)
		}
	}

	srcHandler := New(context.Background(), src, poller.New(src))

	rec := httptest.NewRecorder()
	srcHandler.handleConfigExport(rec, httptest.NewRequest(http.MethodGet, "/api/config/export", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("export status %d: %s", rec.Code, rec.Body.String())
	}
	yamlBody := rec.Body.Bytes()
	t.Logf("exported YAML:\n%s", yamlBody)

	dst, err := db.Open(t.TempDir() + "/dst.db")
	if err != nil {
		t.Fatal(err)
	}
	defer dst.Close()
	dstHandler := New(context.Background(), dst, poller.New(dst))

	rec2 := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/config/import", bytes.NewReader(yamlBody))
	dstHandler.handleConfigImport(rec2, req)
	if rec2.Code != http.StatusOK {
		t.Fatalf("import status %d: %s", rec2.Code, rec2.Body.String())
	}
	t.Logf("import result: %s", rec2.Body.String())

	printers, err := dst.ListPrinters()
	if err != nil || len(printers) != 1 {
		t.Fatalf("ListPrinters: %v, %d printers", err, len(printers))
	}
	got, err := dst.GetPrinter(printers[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Maintenance {
		t.Error("maintenance flag did not round-trip")
	}
	if got.HideModel != true || got.Model != "Core One" {
		t.Errorf("model/hide_model did not round-trip: %+v", got)
	}
	if got.IdleTimeoutMinutes != 30 || got.MaxBedTemp != 110 || got.MaxExtruderTemp != 280 {
		t.Errorf("auto-off/thermal overrides did not round-trip: %+v", got)
	}

	plugs, err := dst.ListAllSmartPlugs()
	if err != nil || len(plugs) != 2 {
		t.Fatalf("ListAllSmartPlugs: %v, %d plugs", err, len(plugs))
	}
	foundMQTT := false
	for _, sp := range plugs {
		if sp.MQTTTopic == "tasmota_topic" {
			foundMQTT = true
		}
	}
	if !foundMQTT {
		t.Error("MQTT-mode smart plug did not round-trip")
	}

	cams, err := dst.ListAllCameras()
	if err != nil || len(cams) != 1 {
		t.Fatalf("ListAllCameras: %v, %d cameras", err, len(cams))
	}

	targets, err := dst.ListIngestTargets()
	if err != nil || len(targets) != 1 {
		t.Fatalf("ListIngestTargets: %v, %d targets", err, len(targets))
	}
	if targets[0].Label != "core-one" || targets[0].APIKey != "ingest-key-123" {
		t.Errorf("ingest target did not round-trip: %+v", targets[0])
	}

	gotSettings, err := dst.GetAllSettings()
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range wantSettings {
		if gotSettings[k] != v {
			t.Errorf("setting %s: got %q, want %q", k, gotSettings[k], v)
		}
	}
}
