package api

import (
	"net/http"
	"net/url"
	"testing"
)

// The thumb parameter is joined onto the printer's URL and fetched with the
// printer's credentials: only thumbnail routes get through (PrusaLink
// /thumb/..., OctoPrint plugin/<name>/thumbnail/...), so a logged-in user
// cannot point the proxy at other printer paths.
func TestThumbProxyRejectsOtherPaths(t *testing.T) {
	h, _ := newTestHandler(t)
	for thumb, wantBad := range map[string]bool{
		"/thumb/s/usb/COREON~1.BGC":                                  false,
		"plugin/prusaslicerthumbnails/thumbnail/cube.png?20231010":   false,
		"plugin/prusaslicerthumbnails/thumbnail/part..v2.png?2023":   false,
		"plugin/prusaslicerthumbnails/thumbnail/PLA 100% infill.png": false,
		"/api/v1/status":                     true,
		"/thumb/../api/v1/status":            true,
		"/thumb/%2e%2e/api/v1/status":        false, // sent as %252e%252e: a literal name
		"//evil.example/thumb/x":             true,
		"plugin/../api/printer":              true,
		"plugin/backup/download/backup.zip":  true,
		"plugin/x":                           true,
		"plugin/x/thumbnail/../../api/files": true,
		"http://evil.example/thumb/x":        true,
	} {
		path := "/api/file-thumbnail/999?path=x.bgcode&uploaded_at=1&thumb=" + url.QueryEscape(thumb)
		rec := do(h.handleFileThumbnailProxy, "GET", path, "")
		if bad := rec.Code == http.StatusBadRequest; bad != wantBad {
			t.Errorf("thumb %q: status %d, want rejected=%v", thumb, rec.Code, wantBad)
		}
	}
}

// Refs are raw text (PrusaLink sends APPLE%~7.BGC unescaped): every
// character, "%" included, reaches the printer as itself.
func TestThumbPathEscapes(t *testing.T) {
	for ref, want := range map[string]string{
		"/thumb/s/usb/COREON~1.BGC": "/thumb/s/usb/COREON~1.BGC",
		// Real ref from a Core One (2026-11-24): raw "%" in the 8.3 name.
		"/thumb/l/usb/APPLE%~7.BGC":                       "/thumb/l/usb/APPLE%25~7.BGC",
		"plugin/p/thumbnail/PLA 100% infill.png?20261124": "/plugin/p/thumbnail/PLA%20100%25%20infill.png?20261124",
		"plugin/p/thumbnail/My%20Part.png":                "/plugin/p/thumbnail/My%2520Part.png",
		"/thumb/l/usb/50%AB~1.BGC":                        "/thumb/l/usb/50%25AB~1.BGC",
		"/thumb/%2e%2e/api/v1/status":                     "/thumb/%252e%252e/api/v1/status",
		// '?' in a PrusaLink name is the name; '#' in an OctoPrint query is escaped.
		"/thumb/l/usb/WHAT?~1.BGC":       "/thumb/l/usb/WHAT%3F~1.BGC",
		"plugin/p/thumbnail/a.png?1#2 3": "/plugin/p/thumbnail/a.png?1%232%203",
	} {
		if got, ok := thumbPath(ref); !ok || got != want {
			t.Errorf("thumbPath(%q) = %q, %v; want %q", ref, got, ok, want)
		}
	}
}
