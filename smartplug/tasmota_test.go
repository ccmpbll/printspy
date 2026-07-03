package smartplug

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newTestClient bypasses netguard so httptest's 127.0.0.1 server isn't
// blocked as loopback — the SSRF guard itself is covered separately in
// netguard's own tests, this only exercises Tasmota response parsing.
func newTestClient() *Client {
	return &Client{http: &http.Client{}}
}

func TestGetState(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cmnd := r.URL.Query().Get("cmnd")
		switch {
		case strings.HasPrefix(cmnd, "Power1"):
			w.Write([]byte(`{"POWER1":"ON"}`))
		case cmnd == "Status 8":
			w.Write([]byte(`{"StatusSNS":{"Time":"2026-07-03T00:00:00","ENERGY":{"Power":15.2,"Voltage":120.1,"Current":0.13,"Total":0.481}}}`))
		}
	}))
	defer srv.Close()

	host := strings.TrimPrefix(srv.URL, "http://")
	ps, err := newTestClient().GetState(context.Background(), host, "1", "Printer")
	if err != nil {
		t.Fatalf("GetState: %v", err)
	}
	if !ps.On {
		t.Errorf("On = false, want true")
	}
	if ps.ID != host+":1" {
		t.Errorf("ID = %q, want %q", ps.ID, host+":1")
	}
	if ps.Watts != 15.2 || ps.Voltage != 120.1 || ps.Current != 0.13 || ps.TotalKWh != 0.481 {
		t.Errorf("energy fields = %+v, want Watts=15.2 Voltage=120.1 Current=0.13 TotalKWh=0.481", ps)
	}
}

func TestGetStateSingleRelayFallback(t *testing.T) {
	// Some single-relay devices echo back "POWER" instead of "POWER1".
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"POWER":"OFF"}`))
	}))
	defer srv.Close()

	host := strings.TrimPrefix(srv.URL, "http://")
	ps, err := newTestClient().GetState(context.Background(), host, "1", "")
	if err != nil {
		t.Fatalf("GetState: %v", err)
	}
	if ps.On {
		t.Errorf("On = true, want false")
	}
}

func TestGetStateUnexpectedResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"Command":"Unknown"}`))
	}))
	defer srv.Close()

	host := strings.TrimPrefix(srv.URL, "http://")
	if _, err := newTestClient().GetState(context.Background(), host, "1", ""); err == nil {
		t.Error("expected error for response with no POWER key, got nil")
	}
}

func TestSetState(t *testing.T) {
	var gotCmnd string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCmnd = r.URL.Query().Get("cmnd")
		w.Write([]byte(`{"POWER1":"ON"}`))
	}))
	defer srv.Close()

	host := strings.TrimPrefix(srv.URL, "http://")
	if err := newTestClient().SetState(context.Background(), host, "1", true); err != nil {
		t.Fatalf("SetState: %v", err)
	}
	if gotCmnd != "Power1 On" {
		t.Errorf("cmnd = %q, want %q", gotCmnd, "Power1 On")
	}
}
