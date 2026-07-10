// Package notify sends push notifications via Pushover. Single-provider by
// design - no interface, no abstraction, until a second provider is real.
package notify

import (
	"bytes"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"time"
)

const pushoverURL = "https://api.pushover.net/1/messages.json"

var client = &http.Client{Timeout: 15 * time.Second}

// SendPushover posts a notification to Pushover. image may be nil for a
// text-only notification - never block sending on a missing/failed image.
func SendPushover(token, userKey, title, message string, image []byte, imageContentType string) error {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)

	fields := map[string]string{
		"token":   token,
		"user":    userKey,
		"title":   title,
		"message": message,
	}
	for k, v := range fields {
		if err := w.WriteField(k, v); err != nil {
			return fmt.Errorf("write field %s: %w", k, err)
		}
	}
	if len(image) > 0 {
		if imageContentType == "" {
			imageContentType = "image/jpeg"
		}
		part, err := w.CreatePart(map[string][]string{
			"Content-Disposition": {`form-data; name="attachment"; filename="snapshot.jpg"`},
			"Content-Type":        {imageContentType},
		})
		if err != nil {
			return fmt.Errorf("create attachment part: %w", err)
		}
		if _, err := part.Write(image); err != nil {
			return fmt.Errorf("write attachment: %w", err)
		}
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("close multipart writer: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, pushoverURL, &buf)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", w.FormDataContentType())

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("send: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("pushover http %d: %s", resp.StatusCode, body)
	}
	return nil
}
