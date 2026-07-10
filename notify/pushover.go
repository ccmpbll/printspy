// Package notify sends push notifications via Pushover. Single-provider by
// design - no interface, no abstraction, until a second provider is real.
package notify

import (
	"bytes"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strconv"
	"time"
)

const pushoverURL = "https://api.pushover.net/1/messages.json"

var client = &http.Client{Timeout: 15 * time.Second}

// Sounds is Pushover's fixed set of built-in notification sounds.
var Sounds = []string{
	"pushover", "bike", "bugle", "cashregister", "classical", "cosmic",
	"falling", "gamelan", "incoming", "intermission", "magic", "mechanical",
	"pianobar", "siren", "spacealarm", "tugboat", "alien", "climb",
	"persistent", "echo", "updown", "vibrate", "none",
}

// Message is a Pushover notification. Image may be nil for text-only.
// Priority 0 = normal, 1 = high (bypasses phone quiet hours/DND). Sound
// empty uses the receiving device's default Pushover sound.
type Message struct {
	Title, Text      string
	Image            []byte
	ImageContentType string
	Priority         int
	Sound            string
}

// Send posts msg to Pushover.
func Send(token, userKey string, msg Message) error {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)

	fields := map[string]string{
		"token":   token,
		"user":    userKey,
		"title":   msg.Title,
		"message": msg.Text,
	}
	if msg.Priority != 0 {
		fields["priority"] = strconv.Itoa(msg.Priority)
	}
	if msg.Sound != "" {
		fields["sound"] = msg.Sound
	}
	for k, v := range fields {
		if err := w.WriteField(k, v); err != nil {
			return fmt.Errorf("write field %s: %w", k, err)
		}
	}
	if len(msg.Image) > 0 {
		imageContentType := msg.ImageContentType
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
		if _, err := part.Write(msg.Image); err != nil {
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
