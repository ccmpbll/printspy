package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// Regression: wrapping http.ResponseWriter in statusWriter (for the debug
// request-logging middleware) silently broke SSE - handleSSE type-asserts
// w.(http.Flusher), and a wrapper that doesn't forward Flush() fails that
// assertion even though the real underlying writer supports it. The
// dashboard's printer list comes from the SSE init event, not a REST fetch
// on page load, so this alone made every printer disappear from the UI.
func TestStatusWriterImplementsFlusher(t *testing.T) {
	rec := httptest.NewRecorder()
	sw := &statusWriter{ResponseWriter: rec, status: http.StatusOK}

	flusher, ok := (http.ResponseWriter(sw)).(http.Flusher)
	if !ok {
		t.Fatal("statusWriter does not implement http.Flusher - breaks any handler (SSE) that requires it")
	}
	flusher.Flush()
	if !rec.Flushed {
		t.Error("Flush() did not reach the underlying ResponseWriter")
	}
}
