// Package smartplug talks directly to Tasmota devices over their HTTP API.
// Plugs are managed independently of printers and can be assigned to any
// printer, regardless of type — including OctoPrint printers that already
// auto-detect their own power plugins, for cases like a second plug an
// auto-detected one doesn't cover.
package smartplug

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/ccmpbll/printspy/models"
	"github.com/ccmpbll/printspy/netguard"
)

type Client struct {
	http *http.Client
}

// sharedHTTP is reused by every Client so plug polling reuses connections
// instead of building a new transport (and leaking its idle conns) per call.
var sharedHTTP = &http.Client{Timeout: 5 * time.Second, Transport: netguard.Transport()}

func New() *Client {
	return &Client{http: sharedHTTP}
}

// maxResponseBytes bounds a Tasmota reply; real ones are a few hundred bytes.
const maxResponseBytes = 64 << 10

func (c *Client) GetState(ctx context.Context, ip, idx, label string, hideLabel bool) (*models.PowerState, error) {
	data, err := c.command(ctx, ip, "Power"+idx)
	if err != nil {
		return nil, err
	}
	var powerResp map[string]string
	if err := json.Unmarshal(data, &powerResp); err != nil {
		return nil, err
	}
	state, ok := powerResp["POWER"+idx]
	if !ok {
		state, ok = powerResp["POWER"]
	}
	if !ok {
		return nil, fmt.Errorf("smartplug: unexpected response from %s: %s", ip, data)
	}

	ps := &models.PowerState{
		ID:        ip + ":" + idx,
		Label:     label,
		HideLabel: hideLabel,
		On:        state == "ON",
		Source:    "tasmota-direct",
	}

	if data, err := c.command(ctx, ip, "Status 8"); err == nil {
		var statusResp struct {
			StatusSNS struct {
				Energy struct {
					Power   float64 `json:"Power"`
					Voltage float64 `json:"Voltage"`
					Current float64 `json:"Current"`
					Total   float64 `json:"Total"`
				} `json:"ENERGY"`
			} `json:"StatusSNS"`
		}
		if json.Unmarshal(data, &statusResp) == nil {
			ps.Watts = statusResp.StatusSNS.Energy.Power
			ps.Voltage = statusResp.StatusSNS.Energy.Voltage
			ps.Current = statusResp.StatusSNS.Energy.Current
			ps.TotalKWh = statusResp.StatusSNS.Energy.Total
		}
	}

	return ps, nil
}

func (c *Client) SetState(ctx context.Context, ip, idx string, on bool) error {
	action := "Off"
	if on {
		action = "On"
	}
	data, err := c.command(ctx, ip, "Power"+idx+" "+action)
	if err != nil {
		return err
	}
	// Tasmota echoes the new state ({"POWER1":"ON"}); a reply that says the
	// opposite means the command didn't take. A reply without the key is
	// accepted rather than guessed at.
	var echo map[string]string
	if json.Unmarshal(data, &echo) == nil {
		got, ok := echo["POWER"+idx]
		if !ok {
			got, ok = echo["POWER"]
		}
		if want := map[bool]string{true: "ON", false: "OFF"}[on]; ok && got != want {
			return fmt.Errorf("smartplug: %s reported %s, wanted %s", ip, got, want)
		}
	}
	return nil
}

func (c *Client) command(ctx context.Context, ip, cmnd string) ([]byte, error) {
	reqURL := fmt.Sprintf("http://%s/cm?cmnd=%s", ip, url.QueryEscape(cmnd))
	slog.Debug("smartplug command", "url", reqURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		slog.Debug("smartplug command failed", "url", reqURL, "error", err)
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	slog.Debug("smartplug command response", "url", reqURL, "status", resp.StatusCode, "body", string(body))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("smartplug: %s returned HTTP %d", ip, resp.StatusCode)
	}
	return body, nil
}
