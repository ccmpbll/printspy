package api

import (
	"strings"
	"testing"

	_ "github.com/ccmpbll/printspy/plugin/octoprint" // register plugin types for plugin.Known
	_ "github.com/ccmpbll/printspy/plugin/prusalink"
)

func TestImportDropsChildrenOfSkippedPrintersAndUnknownTypes(t *testing.T) {
	h, d := newTestHandler(t)
	yaml := `
printers:
  - {name: good, type: octoprint, url: "http://10.1.1.1", api_key: k, enabled: true}
  - {name: nokey, type: octoprint, url: "http://10.1.1.2", enabled: true}
  - {name: weird, type: nonesuch, url: "http://10.1.1.3", api_key: k, enabled: true}
smart_plugs:
  - {ip: 10.9.9.1, idx: "1", label: onGood, printer_index: 0}
  - {ip: 10.9.9.2, idx: "1", label: onSkipped, printer_index: 1}
  - {ip: 10.9.9.3, idx: "1", label: onWeird, printer_index: 2}
  - {ip: 10.9.9.4, idx: "1", label: standalone}
cameras:
  - {url: "http://10.8.8.1", name: camSkipped, printer_index: 1}
  - {url: "http://10.8.8.2", name: camFree}
ingest_targets:
  - {label: tSkipped, api_key: k1, printer_index: 1}
  - {label: tGood, api_key: k2, printer_index: 0}
`
	rec := do(h.handleConfigImport, "POST", "/api/config/import", yaml)
	body := rec.Body.String()
	if rec.Code != 200 {
		t.Fatalf("code %d %s", rec.Code, body)
	}
	for _, want := range []string{`unknown type \"nonesuch\"`, "onSkipped", "onWeird", "camSkipped", "tSkipped"} {
		if !strings.Contains(body, want) {
			t.Errorf("response missing %q: %s", want, body)
		}
	}
	printers, _ := d.ListPrinters()
	if len(printers) != 1 || printers[0].Name != "good" {
		t.Errorf("printers = %+v, want only 'good'", printers)
	}
	plugs, _ := d.ListAllSmartPlugs()
	cams, _ := d.ListAllCameras()
	targets, _ := d.ListIngestTargets()
	if len(plugs) != 2 || len(cams) != 1 || len(targets) != 1 || targets[0].Label != "tGood" {
		t.Errorf("plugs=%d cams=%d targets=%+v, want 2 (onGood+standalone), 1 (camFree), only tGood", len(plugs), len(cams), targets)
	}
}

func TestAddPrinterRejectsUnknownType(t *testing.T) {
	h, _ := newTestHandler(t)
	rec := do(h.handlePrinters, "POST", "/api/printers", `{"name":"x","type":"nonesuch","url":"http://10.1.1.1","api_key":"k"}`)
	if rec.Code != 400 {
		t.Errorf("code = %d, want 400: %s", rec.Code, rec.Body.String())
	}
}
