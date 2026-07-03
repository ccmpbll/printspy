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
	"net/http"
	"net/url"
	"time"

	"github.com/ccmpbll/printspy/models"
	"github.com/ccmpbll/printspy/netguard"
)

type Client struct {
	http *http.Client
}

func New() *Client {
	return &Client{http: &http.Client{Timeout: 5 * time.Second, Transport: netguard.Transport()}}
}

func (c *Client) GetState(ctx context.Context, ip, idx, label string) (*models.PowerState, error) {
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
		ID:     ip + ":" + idx,
		Label:  label,
		On:     state == "ON",
		Source: "tasmota-direct",
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
	_, err := c.command(ctx, ip, "Power"+idx+" "+action)
	return err
}

func (c *Client) command(ctx context.Context, ip, cmnd string) ([]byte, error) {
	reqURL := fmt.Sprintf("http://%s/cm?cmnd=%s", ip, url.QueryEscape(cmnd))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}
