package api

import (
	"crypto/tls"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func fromIP(r *http.Request, ip string) *http.Request {
	r.RemoteAddr = ip + ":1234"
	return r
}

func TestLoginFailuresAreScopedToClientAndUsername(t *testing.T) {
	h, _ := newTestHandler(t)
	attacker := loginKey(fromIP(httptest.NewRequest("POST", "/login", nil), "10.0.0.66"), "Admin")
	owner := loginKey(fromIP(httptest.NewRequest("POST", "/login", nil), "10.0.0.5"), "admin")
	for i := 0; i < loginMaxAttempts; i++ {
		h.recordLoginFailure(attacker)
	}
	if !h.rateLimited(attacker) {
		t.Error("attacker should be limited")
	}
	if h.rateLimited(owner) {
		t.Error("another client logging in as the same user was locked out")
	}
}

func TestLoginFailureTableIsBounded(t *testing.T) {
	h, _ := newTestHandler(t)
	for i := 0; i < loginMaxKeys*2; i++ {
		h.recordLoginFailure(fmt.Sprintf("10.0.0.1|user%d", i))
	}
	if n := len(h.loginFails); n > loginMaxKeys {
		t.Errorf("loginFails grew to %d, want <= %d", n, loginMaxKeys)
	}
}

func TestUnknownUserLoginFailsAndIsCounted(t *testing.T) {
	h, d := newTestHandler(t)
	if _, err := h.createUser("real", "correct-password"); err != nil {
		t.Fatal(err)
	}
	_ = d
	form := url.Values{"username": {"nobody"}, "password": {"whatever1"}}
	req := httptest.NewRequest("POST", "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.handleLogin(rec, fromIP(req, "10.0.0.9"))
	if loc := rec.Header().Get("Location"); loc != "/login?error=1" {
		t.Errorf("redirect = %q, want /login?error=1", loc)
	}
	if len(h.loginFails) != 1 {
		t.Errorf("failure not recorded: %v", h.loginFails)
	}
}

func TestStatusEndpointKeyHandling(t *testing.T) {
	h, d := newTestHandler(t)
	const key = "0123456789abcdef0123456789abcdef"
	if err := d.SetSetting("status_api_key", key); err != nil {
		t.Fatal(err)
	}
	call := func(ip, apiKey string) int {
		req := fromIP(httptest.NewRequest("GET", "/api/status", nil), ip)
		req.Header.Set("X-Api-Key", apiKey)
		rec := httptest.NewRecorder()
		h.handleStatus(rec, req)
		return rec.Code
	}
	if c := call("10.0.0.1", key); c != http.StatusOK {
		t.Fatalf("valid key = %d, want 200", c)
	}
	for i := 0; i < loginMaxAttempts; i++ {
		if c := call("10.0.0.2", "wrong"); c != http.StatusUnauthorized {
			t.Fatalf("wrong key = %d, want 401", c)
		}
	}
	if c := call("10.0.0.2", key); c != http.StatusTooManyRequests {
		t.Errorf("guessing client after limit = %d, want 429", c)
	}
	if c := call("10.0.0.3", key); c != http.StatusOK {
		t.Errorf("different client = %d, want 200", c)
	}
}

func TestStatusAPIKeyMinLength(t *testing.T) {
	if _, err := validateSetting("status_api_key", "short"); err == nil {
		t.Error("short key accepted")
	}
	if v, err := validateSetting("status_api_key", " 0123456789abcdef "); err != nil || v != "0123456789abcdef" {
		t.Errorf("valid key = %q, %v", v, err)
	}
	if _, err := validateSetting("status_api_key", ""); err != nil {
		t.Errorf("empty (disable) rejected: %v", err)
	}
}

func TestLoginFloodDoesNotEvictLiveEntries(t *testing.T) {
	h, _ := newTestHandler(t)
	victim := "10.0.0.9|admin"
	for i := 0; i < loginMaxAttempts; i++ {
		h.recordLoginFailure(victim)
	}
	for i := 0; i < loginMaxKeys*2; i++ {
		k := fmt.Sprintf("10.0.0.1|flood%d", i)
		h.rateLimited(k)
		h.recordLoginFailure(k)
	}
	if !h.rateLimited(victim) {
		t.Error("flooding unique usernames wiped the victim's failure record")
	}
	if !h.rateLimited("10.0.0.2|never-seen") {
		t.Error("table full of live entries should fail closed for unseen keys")
	}
}

func TestSessionCookieSecureOnlyOverHTTPS(t *testing.T) {
	h, _ := newTestHandler(t)
	cases := []struct {
		name   string
		mod    func(*http.Request)
		secure bool
	}{
		{"plain http", func(r *http.Request) {}, false},
		{"proxy https", func(r *http.Request) { r.Header.Set("X-Forwarded-Proto", "https") }, true},
		{"proxy chain https first", func(r *http.Request) { r.Header.Set("X-Forwarded-Proto", "HTTPS, http") }, true},
		{"proxy http", func(r *http.Request) { r.Header.Set("X-Forwarded-Proto", "http") }, false},
		{"direct tls", func(r *http.Request) { r.TLS = &tls.ConnectionState{} }, true},
	}
	for i, c := range cases {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/login", nil)
		c.mod(req)
		if err := h.startSession(rec, req, fmt.Sprintf("u%d", i)); err != nil {
			t.Fatal(err)
		}
		ck := rec.Result().Cookies()
		if len(ck) != 1 || ck[0].Secure != c.secure || !ck[0].HttpOnly {
			t.Errorf("%s: cookie=%+v, want Secure=%v HttpOnly", c.name, ck, c.secure)
		}
	}
}
