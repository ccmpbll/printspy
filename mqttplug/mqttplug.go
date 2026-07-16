// Package mqttplug talks to Tasmota devices over MQTT instead of direct
// HTTP (see smartplug) - push-based: Tasmota publishes state/telemetry to a
// broker the instant it changes, and this package just listens, instead of
// smartplug's per-tick HTTP round-trip.
package mqttplug

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"

	"github.com/ccmpbll/printspy/models"
)

type relayMeta struct {
	Label     string
	HideLabel bool
}

type topicSubs struct {
	relays map[string]relayMeta // idx -> meta
}

// Client is a persistent MQTT connection tracking cached power/energy state
// for Tasmota devices, keyed by "<topic>:<idx>".
type Client struct {
	mu    sync.RWMutex
	cli   mqtt.Client
	state map[string]models.PowerState
	subs  map[string]topicSubs
}

func New() *Client {
	return &Client{
		state: make(map[string]models.PowerState),
		subs:  make(map[string]topicSubs),
	}
}

// Configure (re)connects to brokerURL, disconnecting any existing connection
// first - safe to call repeatedly (settings changed, or a manual retry from
// /api/mqtt-test). An empty brokerURL just disconnects, leaving MQTT mode
// fully opt-in. Subscriptions from the last Sync survive a reconfigure -
// onConnect resubscribes them once the new connection is up.
func (c *Client) Configure(brokerURL, username, password string) error {
	c.mu.Lock()
	old := c.cli
	c.cli = nil
	c.mu.Unlock()
	if old != nil && old.IsConnected() {
		old.Disconnect(250)
	}
	if brokerURL == "" {
		return nil
	}

	opts := mqtt.NewClientOptions().
		AddBroker(brokerURL).
		SetClientID("printspy-smartplugs").
		SetUsername(username).
		SetPassword(password).
		SetAutoReconnect(true).
		SetConnectRetry(true).
		SetConnectTimeout(10 * time.Second).
		SetOnConnectHandler(c.onConnect).
		SetConnectionLostHandler(func(_ mqtt.Client, err error) {
			log.Printf("mqtt: connection lost, reconnecting: %v", err)
		})

	cli := mqtt.NewClient(opts)
	token := cli.Connect()
	if !token.WaitTimeout(10 * time.Second) {
		return fmt.Errorf("mqtt: connect to %s timed out", brokerURL)
	}
	if err := token.Error(); err != nil {
		return err
	}

	c.mu.Lock()
	c.cli = cli
	c.mu.Unlock()
	return nil
}

// onConnect (re)subscribes every known plug's topics - fires on the initial
// connect and every paho auto-reconnect, so there's no separate manual
// resubscribe path needed after a network blip.
func (c *Client) onConnect(cli mqtt.Client) {
	log.Print("mqtt: connected")
	c.mu.RLock()
	subs := make(map[string]topicSubs, len(c.subs))
	for t, ts := range c.subs {
		subs[t] = ts
	}
	c.mu.RUnlock()
	for t, ts := range subs {
		subscribeAndQuery(cli, t, ts, c.handleMessage)
	}
}

// statWildcard uses "+" for the whole final level ("stat/<topic>/+"), not
// "POWER+" - MQTT wildcards must occupy an entire topic level on their own,
// "POWER+" is a malformed subscription string and gets the client
// disconnected by a spec-compliant broker. Catches POWER, POWER1, POWER2,
// RESULT, etc - handleMessage filters to the POWER* ones it cares about.
func statWildcard(topic string) string {
	return "stat/" + topic + "/+"
}

// subscribeAndQuery subscribes to a topic's state/telemetry/LWT, then
// publishes an empty-payload Power query for each of its relays. Some real
// Tasmota devices only ever publish on their own schedule (periodic
// tele/STATE) or on an actual state change - if neither has happened since
// the device booted, waiting passively leaves the cache empty indefinitely
// (silently, no error - it just never shows up on the dashboard). Tasmota
// treats cmnd/<topic>/Power with an empty payload as a query: it answers
// with the current state on stat/<topic>/POWER<N> without toggling anything.
func subscribeAndQuery(cli mqtt.Client, topic string, ts topicSubs, handler mqtt.MessageHandler) {
	cli.Subscribe(statWildcard(topic), 1, handler)
	cli.Subscribe("tele/"+topic+"/SENSOR", 1, handler)
	cli.Subscribe("tele/"+topic+"/LWT", 1, handler)
	queryState(cli, topic, ts)
}

func queryState(cli mqtt.Client, topic string, ts topicSubs) {
	for idx := range ts.relays {
		cli.Publish(commandTopic(topic, idx), 0, false, "")
	}
}

// Sync replaces the full set of subscribed plugs - call after any
// smart-plug CRUD touching an MQTT-mode row. Diffs against the previous
// topic set so unrelated topics aren't churned; two plug rows sharing one
// physical device's topic (different idx) share a single subscription.
func (c *Client) Sync(plugs []models.SmartPlug) {
	newSubs := make(map[string]topicSubs)
	for _, sp := range plugs {
		if sp.MQTTTopic == "" {
			continue
		}
		idx := sp.Idx
		if idx == "" {
			idx = "1"
		}
		ts, ok := newSubs[sp.MQTTTopic]
		if !ok {
			ts = topicSubs{relays: make(map[string]relayMeta)}
		}
		ts.relays[idx] = relayMeta{Label: sp.Label, HideLabel: sp.HideLabel}
		newSubs[sp.MQTTTopic] = ts
	}

	c.mu.Lock()
	oldSubs := c.subs
	c.subs = newSubs
	cli := c.cli
	c.mu.Unlock()

	if cli == nil || !cli.IsConnected() {
		return // next onConnect subscribes from the freshly-set c.subs
	}
	for topic := range oldSubs {
		if _, ok := newSubs[topic]; !ok {
			cli.Unsubscribe(statWildcard(topic), "tele/"+topic+"/SENSOR", "tele/"+topic+"/LWT")
		}
	}
	for topic, ts := range newSubs {
		if _, ok := oldSubs[topic]; !ok {
			subscribeAndQuery(cli, topic, ts, c.handleMessage)
		}
	}
}

func (c *Client) handleMessage(cli mqtt.Client, msg mqtt.Message) {
	topic, kind, suffix, ok := parseTopic(msg.Topic())
	if !ok {
		return
	}
	switch {
	case kind == "stat":
		c.applyPower(topic, suffix, string(msg.Payload()) == "ON")
	case kind == "tele" && suffix == "SENSOR":
		c.applyEnergy(topic, msg.Payload())
	case kind == "tele" && suffix == "LWT" && string(msg.Payload()) == "Online":
		// The device just (re)connected to the broker - could be a reboot
		// or power-cycle that left it in a different state than we last
		// knew (Tasmota's own power-on-state behavior), with nothing else
		// telling us. Query fresh instead of trusting the stale cache.
		c.mu.RLock()
		ts, ok := c.subs[topic]
		c.mu.RUnlock()
		if ok {
			queryState(cli, topic, ts)
		}
	case kind == "tele" && suffix == "LWT" && string(msg.Payload()) == "Offline":
		// The broker's own last-will fired - the device dropped off (most
		// commonly it lost power, being an inline relay). Without this, the
		// cache keeps showing whatever the last confirmed on/off read was
		// indefinitely - a plug that's actually been dead for an hour still
		// shows "On" on the dashboard forever, since nothing else ever
		// updates it. Mark every relay under this topic off and unreachable
		// instead of trusting a stale reading.
		c.markOffline(topic)
	}
}

// parseTopic splits "stat/<topic>/POWER1" or "tele/<topic>/SENSOR" into its
// parts. Tasmota topics never contain "/" themselves (it's a device name),
// so a straight 3-way split is safe.
func parseTopic(t string) (topic, kind, suffix string, ok bool) {
	parts := strings.SplitN(t, "/", 3)
	if len(parts) != 3 {
		return "", "", "", false
	}
	return parts[1], parts[0], parts[2], true
}

func (c *Client) applyPower(topic, suffix string, on bool) {
	if !strings.HasPrefix(suffix, "POWER") {
		return
	}
	idx := strings.TrimPrefix(suffix, "POWER")
	if idx == "" {
		idx = "1"
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	key := stateKey(topic, idx)
	ps := c.state[key]
	stampMeta(&ps, topic, idx, c.relayMetaLocked(topic, idx))
	ps.On = on
	c.state[key] = ps
}

// markOffline flags every relay under topic as off, called on the device's
// own LWT going Offline. "Off" (rather than leaving On untouched, or adding
// a separate unreachable flag models.PowerState doesn't have) is the safer
// assumption for an inline relay that's stopped responding - the dominant
// real-world cause is the device itself lost power, which makes Off
// correct, not just a guess; the alternative (silently trusting a stale On)
// is actively misleading on the dashboard.
func (c *Client) markOffline(topic string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	ts, ok := c.subs[topic]
	if !ok {
		return
	}
	for idx, meta := range ts.relays {
		key := stateKey(topic, idx)
		ps := c.state[key]
		stampMeta(&ps, topic, idx, meta)
		ps.On = false
		ps.Source = "mqtt-offline"
		c.state[key] = ps
	}
}

// stampMeta fills the identity fields shared by every update path
// (applyPower, applyEnergy) - only the payload-specific fields differ.
func stampMeta(ps *models.PowerState, topic, idx string, meta relayMeta) {
	ps.ID = "mqtt:" + topic + ":" + idx
	ps.Label = meta.Label
	ps.HideLabel = meta.HideLabel
	ps.Source = "tasmota-mqtt"
}

// applyEnergy updates every relay cached under topic - energy is
// device-wide, same assumption the existing HTTP Status 8 parsing already
// makes (smartplug/tasmota.go).
func (c *Client) applyEnergy(topic string, payload []byte) {
	var body struct {
		ENERGY struct {
			Power   float64 `json:"Power"`
			Voltage float64 `json:"Voltage"`
			Current float64 `json:"Current"`
			Total   float64 `json:"Total"`
		} `json:"ENERGY"`
	}
	if json.Unmarshal(payload, &body) != nil {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	ts, ok := c.subs[topic]
	if !ok {
		return
	}
	for idx, meta := range ts.relays {
		key := stateKey(topic, idx)
		ps := c.state[key]
		stampMeta(&ps, topic, idx, meta)
		ps.Watts = body.ENERGY.Power
		ps.Voltage = body.ENERGY.Voltage
		ps.Current = body.ENERGY.Current
		ps.TotalKWh = body.ENERGY.Total
		c.state[key] = ps
	}
}

// relayMetaLocked looks up label/hide_label for topic:idx - caller must
// hold c.mu.
func (c *Client) relayMetaLocked(topic, idx string) relayMeta {
	if ts, ok := c.subs[topic]; ok {
		if m, ok := ts.relays[idx]; ok {
			return m
		}
	}
	return relayMeta{}
}

func stateKey(topic, idx string) string {
	if idx == "" {
		idx = "1"
	}
	return topic + ":" + idx
}

// GetState is a cache-only read - no I/O, state arrives via subscribed MQTT
// messages, not on demand. ok is false until at least one message has been
// received for this relay (e.g. right after startup, before Tasmota's next
// periodic telemetry or state change) - callers should skip this tick and
// self-heal once one arrives.
func (c *Client) GetState(topic, idx string) (models.PowerState, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	ps, ok := c.state[stateKey(topic, idx)]
	return ps, ok
}

// commandTopic and onOffPayload match Tasmota's MQTT command conventions:
// idx "1" (the common single-relay case) uses the bare "Power" topic,
// matching smartplug's own HTTP "Power<idx>" convention where idx "1" is
// likewise the default.
func commandTopic(topic, idx string) string {
	if idx != "" && idx != "1" {
		return "cmnd/" + topic + "/Power" + idx
	}
	return "cmnd/" + topic + "/Power"
}

func onOffPayload(on bool) string {
	if on {
		return "ON"
	}
	return "OFF"
}

// SetState publishes the command topic and returns once the broker has
// acknowledged the publish (QoS 1). It also stamps the cache with the
// commanded value immediately on success - without this, a poll cycle
// already in flight when SetState is called reads the pre-command cached
// value (the device's own echo hasn't arrived yet) and clobbers the
// optimistic UI update with the stale one, so the toggle appears to revert
// for up to a full poll interval before self-correcting. The device's own
// stat/.../POWER echo still arrives shortly after and confirms (or, rarely,
// corrects) this.
//
// Fails fast if the broker connection isn't actually open, rather than
// letting paho accept the call. paho.mqtt.golang's IsConnected() reports
// true during its own auto-reconnect retry loop, not just once truly
// connected - a QoS-1 Publish() made during that window is silently queued
// client-side and its token never completes until the broker comes back
// (see paho's client.go Publish, the "reconnecting" case). Without this
// check, every click during a broker outage became a request that hung
// until reconnect and a message that queued up behind it - a backlog that,
// observed live, flushed in a burst once the broker returned and left the
// client in a state where nothing published again without a restart.
func (c *Client) SetState(ctx context.Context, topic, idx string, on bool) error {
	c.mu.RLock()
	cli := c.cli
	c.mu.RUnlock()
	if cli == nil {
		return fmt.Errorf("mqtt: not configured")
	}
	if !cli.IsConnectionOpen() {
		return fmt.Errorf("mqtt: broker not connected")
	}

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	token := cli.Publish(commandTopic(topic, idx), 1, false, onOffPayload(on))
	select {
	case <-token.Done():
	case <-ctx.Done():
		return fmt.Errorf("mqtt: publish timed out: %w", ctx.Err())
	}
	if err := token.Error(); err != nil {
		return err
	}
	c.setCachedOn(topic, idx, on)
	return nil
}

func (c *Client) setCachedOn(topic, idx string, on bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := stateKey(topic, idx)
	ps := c.state[key]
	stampMeta(&ps, topic, idx, c.relayMetaLocked(topic, idx))
	ps.On = on
	c.state[key] = ps
}
