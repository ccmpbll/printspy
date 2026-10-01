package api

import "testing"

func TestSafeImageType(t *testing.T) {
	for in, want := range map[string]string{
		"image/jpeg": "image/jpeg",
		"image/png":  "image/png",
		"multipart/x-mixed-replace;boundary=frame": "multipart/x-mixed-replace;boundary=frame",
		"text/html":                "application/octet-stream",
		"text/html; charset=utf-8": "application/octet-stream",
		"image/svg+xml":            "application/octet-stream",
		"application/octet-stream": "application/octet-stream",
		"":                         "application/octet-stream",
	} {
		if got := safeImageType(in); got != want {
			t.Errorf("safeImageType(%q) = %q, want %q", in, got, want)
		}
	}
}
