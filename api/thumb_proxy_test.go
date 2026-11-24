package api

import (
	"net/http"
	"net/url"
	"testing"
)

// The thumb parameter is joined onto the printer's URL: only the shapes the
// plugins produce get through (PrusaLink /thumb/..., OctoPrint plugin/...),
// so a logged-in user cannot point the proxy at other printer paths.
func TestThumbProxyRejectsOtherPaths(t *testing.T) {
	h, _ := newTestHandler(t)
	for thumb, wantBad := range map[string]bool{
		"/thumb/s/usb/COREON~1.BGC":                                false,
		"plugin/prusaslicerthumbnails/thumbnail/cube.png?20231010": false,
		"/api/v1/status":              true,
		"/thumb/../api/v1/status":     true,
		"/thumb/%2e%2e/api/v1/status": true, // many servers decode before resolving
		"//evil.example/thumb/x":      true,
		"plugin/../api/printer":       true,
		"http://evil.example/thumb/x": true,
	} {
		path := "/api/file-thumbnail/999?path=x.bgcode&uploaded_at=1&thumb=" + url.QueryEscape(thumb)
		rec := do(h.handleFileThumbnailProxy, "GET", path, "")
		if bad := rec.Code == http.StatusBadRequest; bad != wantBad {
			t.Errorf("thumb %q: status %d, want rejected=%v", thumb, rec.Code, wantBad)
		}
	}
}
