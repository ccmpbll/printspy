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
		"/thumb/%2e%2e/api/v1/status":        true,
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

// The ref's path is sent escaped: raw characters from a filename (a space,
// a "%") must reach the printer as themselves, and an already-escaped ref
// is not escaped twice.
func TestThumbPathEscapes(t *testing.T) {
	for ref, want := range map[string]string{
		"/thumb/s/usb/COREON~1.BGC":                       "/thumb/s/usb/COREON~1.BGC",
		"plugin/p/thumbnail/PLA 100% infill.png?20261124": "/plugin/p/thumbnail/PLA%20100%25%20infill.png?20261124",
		"plugin/p/thumbnail/My%20Part.png":                "/plugin/p/thumbnail/My%20Part.png",
	} {
		if got, ok := thumbPath(ref); !ok || got != want {
			t.Errorf("thumbPath(%q) = %q, %v; want %q", ref, got, ok, want)
		}
	}
}
