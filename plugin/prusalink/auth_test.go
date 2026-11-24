package prusalink

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/ccmpbll/printspy/models"
)

// fakePrinter answers like PrusaLink (checked 2026-11-24): a Digest
// challenge with no qop, and the same nonce accepted again until rotated.
type fakePrinter struct {
	mu       sync.Mutex
	nonce    string
	requests int
	puts     int
	basic    bool
	snap404  bool
	qop      bool // offer qop=auth and record each nc sent
	ncs      []string
}

func (f *fakePrinter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests++
	auth := r.Header.Get("Authorization")
	if strings.HasPrefix(auth, "Basic") {
		f.basic = true
	}
	if i := strings.Index(auth, "nc="); i >= 0 {
		f.ncs = append(f.ncs, auth[i+3:i+11])
	}
	if !strings.Contains(auth, `nonce="`+f.nonce+`"`) {
		io.Copy(io.Discard, r.Body)
		qop := ""
		if f.qop {
			qop = `, qop="auth"`
		}
		w.Header().Set("WWW-Authenticate", `Digest realm="Printer API", nonce="`+f.nonce+`"`+qop)
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	switch {
	case r.Method == http.MethodPut:
		f.puts++
		io.Copy(io.Discard, r.Body)
		w.Write([]byte(`{"name":"A~1.BGC","m_timestamp":1}`))
	case r.URL.Path == "/api/v1/cameras/snap" && f.snap404, strings.HasPrefix(r.URL.Path, "/thumb/"):
		http.NotFound(w, r)
	default:
		w.Write([]byte(`{}`))
	}
}

func newFake(t *testing.T) (*Plugin, *fakePrinter) {
	f := &fakePrinter{nonce: "n1", snap404: true}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	p := New(models.PrinterConfig{URL: srv.URL, APIKey: "pw"})
	p.client, p.uploadClient = srv.Client(), srv.Client() // netguard blocks loopback
	return p, f
}

// Item 17: after the first challenge, requests are signed up front, so a
// poll's calls take one round trip each, not two.
func TestDigestReusesChallenge(t *testing.T) {
	p, f := newFake(t)
	for i := 0; i < 3; i++ {
		if _, code, err := p.doGetRaw(context.Background(), "/api/v1/status"); err != nil || code != 200 {
			t.Fatalf("call %d: %d %v", i, code, err)
		}
	}
	if f.requests != 4 {
		t.Errorf("3 calls took %d requests, want 4 (one 401, then signed)", f.requests)
	}
}

// A rotated nonce costs one 401 and a retry, then signing resumes.
func TestDigestRefreshesNonce(t *testing.T) {
	p, f := newFake(t)
	p.doGetRaw(context.Background(), "/api/v1/status")
	f.mu.Lock()
	f.nonce, f.requests = "n2", 0
	f.mu.Unlock()
	if _, code, err := p.doGetRaw(context.Background(), "/api/v1/status"); err != nil || code != 200 {
		t.Fatalf("after rotation: %d %v", code, err)
	}
	if _, err := p.doMutate(context.Background(), http.MethodPost, "/api/v1/job/1/pause", nil); err != nil {
		t.Fatal(err)
	}
	if f.requests != 3 {
		t.Errorf("took %d requests after rotation, want 3", f.requests)
	}
}

// An upload after any earlier request goes out signed: the file is sent
// once, not first unauthenticated and then again.
func TestUploadSendsFileOnce(t *testing.T) {
	p, f := newFake(t)
	p.doGetRaw(context.Background(), "/api/v1/status")
	f.mu.Lock()
	f.requests = 0
	f.mu.Unlock()
	if _, _, err := p.doUpload(context.Background(), "/api/v1/files/usb/a.bgcode", []byte("gcode"), false); err != nil {
		t.Fatal(err)
	}
	if f.requests != 1 {
		t.Errorf("upload took %d requests, want 1", f.requests)
	}
}

// AuthenticatedDo no longer tries Basic (the printers refuse it, 401).
// Item 18: a 404 from cameras/snap is remembered, so GetSnapshotURL stops
// offering a URL that cannot work.
func TestAuthenticatedDoAndSnapshot404(t *testing.T) {
	p, f := newFake(t)
	// A 404 for something else (a thumbnail) says nothing about the camera.
	thumb, _ := http.NewRequest(http.MethodGet, strings.TrimSuffix(p.GetSnapshotURL(), "/api/v1/cameras/snap")+"/thumb/l/usb/X.BGC", nil)
	if resp, err := p.AuthenticatedDo(p.client, thumb); err == nil {
		resp.Body.Close()
	}
	if p.GetSnapshotURL() == "" {
		t.Fatal("snapshot URL empty after a thumbnail 404")
	}
	req, _ := http.NewRequest(http.MethodGet, p.GetSnapshotURL(), nil)
	resp, err := p.AuthenticatedDo(p.client, req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("snap status %d, want 404", resp.StatusCode)
	}
	if f.basic {
		t.Error("AuthenticatedDo sent Basic auth")
	}
	if u := p.GetSnapshotURL(); u != "" {
		t.Errorf("GetSnapshotURL after a 404 = %q, want empty", u)
	}
}

// With qop=auth, each reuse of a nonce counts up (a repeated nc is a
// replay to a strict server), and a new nonce starts again at 1.
func TestDigestNonceCount(t *testing.T) {
	p, f := newFake(t)
	f.qop = true
	for i := 0; i < 3; i++ {
		p.doGetRaw(context.Background(), "/api/v1/status")
	}
	f.mu.Lock()
	f.nonce = "n2"
	f.mu.Unlock()
	p.doGetRaw(context.Background(), "/api/v1/status")
	want := "00000001 00000002 00000003 00000004 00000001"
	if got := strings.Join(f.ncs, " "); got != want {
		t.Errorf("nc sent: %s, want %s", got, want)
	}
}

// A 401 with no Digest challenge is returned as-is, not retried with an
// empty signature.
func TestNoDigestChallengeNoRetry(t *testing.T) {
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		w.Header().Set("WWW-Authenticate", `Basic realm="x"`)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)
	p := New(models.PrinterConfig{URL: srv.URL, APIKey: "pw"})
	p.client = srv.Client()
	if _, code, _ := p.doGetRaw(context.Background(), "/api/v1/status"); code != http.StatusUnauthorized || n != 1 {
		t.Errorf("code=%d requests=%d; want 401 after 1 request", code, n)
	}
}
