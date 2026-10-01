package poller

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The notification-image fetch takes user-supplied camera URLs, so it must
// go through netguard: an httptest server on 127.0.0.1 stands in for any
// loopback/metadata target.
func TestFetchImageURLBlocksLoopback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("secret"))
	}))
	defer srv.Close()

	p := New(nil)
	_, _, err := p.fetchImageURL(context.Background(), 1, srv.URL, true)
	if err == nil || !strings.Contains(err.Error(), "netguard") {
		t.Fatalf("want netguard block, got %v", err)
	}
}

func TestFetchImageURLRejectsOversize(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(make([]byte, maxNotifyImageBytes+1))
	}))
	defer srv.Close()

	p := New(nil)
	p.notifyClient = &http.Client{} // bypass netguard for loopback test server
	if _, _, err := p.fetchImageURL(context.Background(), 1, srv.URL, true); err == nil {
		t.Fatal("want error for oversize image")
	}
}
