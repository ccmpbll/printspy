package mqttplug

import (
	"testing"

	"github.com/ccmpbll/printspy/models"
)

func TestParseTopic(t *testing.T) {
	cases := []struct {
		in                  string
		topic, kind, suffix string
		ok                  bool
	}{
		{"stat/testplug/POWER", "testplug", "stat", "POWER", true},
		{"stat/testplug/POWER2", "testplug", "stat", "POWER2", true},
		{"tele/testplug/SENSOR", "testplug", "tele", "SENSOR", true},
		{"garbage", "", "", "", false},
	}
	for _, c := range cases {
		topic, kind, suffix, ok := parseTopic(c.in)
		if ok != c.ok || topic != c.topic || kind != c.kind || suffix != c.suffix {
			t.Errorf("parseTopic(%q) = (%q,%q,%q,%v), want (%q,%q,%q,%v)",
				c.in, topic, kind, suffix, ok, c.topic, c.kind, c.suffix, c.ok)
		}
	}
}

func TestCommandTopic(t *testing.T) {
	if got := commandTopic("testplug", "1"); got != "cmnd/testplug/Power" {
		t.Errorf("commandTopic idx=1 = %q, want cmnd/testplug/Power", got)
	}
	if got := commandTopic("testplug", "2"); got != "cmnd/testplug/Power2" {
		t.Errorf("commandTopic idx=2 = %q, want cmnd/testplug/Power2", got)
	}
	if got := commandTopic("testplug", ""); got != "cmnd/testplug/Power" {
		t.Errorf("commandTopic idx=\"\" = %q, want cmnd/testplug/Power", got)
	}
}

func TestApplyPowerAndEnergy(t *testing.T) {
	c := New()
	c.subs = map[string]topicSubs{
		"testplug": {relays: map[string]relayMeta{"1": {Label: "Printer"}}},
	}

	c.applyPower("testplug", "POWER", true)
	ps, ok := c.GetState("testplug", "1")
	if !ok || !ps.On || ps.Label != "Printer" || ps.ID != "mqtt:testplug:1" {
		t.Fatalf("applyPower: got %+v, ok=%v", ps, ok)
	}

	c.applyEnergy("testplug", []byte(`{"ENERGY":{"Power":12.3,"Voltage":120,"Current":0.1,"Total":0.5}}`))
	ps, ok = c.GetState("testplug", "1")
	if !ok || ps.Watts != 12.3 || ps.Voltage != 120 || ps.Current != 0.1 || ps.TotalKWh != 0.5 {
		t.Fatalf("applyEnergy: got %+v", ps)
	}
	// On stays true - energy telemetry shouldn't clobber the last known
	// power state, they arrive on separate topics.
	if !ps.On {
		t.Errorf("On = false after applyEnergy, want true (unaffected)")
	}
}

func TestApplyPowerBareSuffixIsRelay1(t *testing.T) {
	c := New()
	c.applyPower("testplug", "POWER", true)
	if _, ok := c.GetState("testplug", "1"); !ok {
		t.Error("bare POWER suffix should key to idx \"1\"")
	}
}

func TestSyncDedupesByTopic(t *testing.T) {
	c := New()
	plugs := []models.SmartPlug{
		{MQTTTopic: "shared", Idx: "1", Label: "A"},
		{MQTTTopic: "shared", Idx: "2", Label: "B"},
		{MQTTTopic: "other", Idx: "1", Label: "C"},
		{MQTTTopic: "", IP: "1.2.3.4", Idx: "1", Label: "HTTP-mode, ignored"},
	}
	c.Sync(plugs)
	if len(c.subs) != 2 {
		t.Fatalf("subs = %d topics, want 2", len(c.subs))
	}
	if len(c.subs["shared"].relays) != 2 {
		t.Fatalf("shared topic relays = %d, want 2", len(c.subs["shared"].relays))
	}
}

func TestGetStateUnknown(t *testing.T) {
	c := New()
	if _, ok := c.GetState("nope", "1"); ok {
		t.Error("GetState on unknown topic:idx should return ok=false")
	}
}
