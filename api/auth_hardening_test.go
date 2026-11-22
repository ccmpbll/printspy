package api

import (
	"crypto/tls"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
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

func TestMeReturnsSignedInUser(t *testing.T) {
	h, d := newTestHandler(t)
	if err := d.CreateSession("tok", "alice", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/api/me", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "tok"})
	rec := httptest.NewRecorder()
	h.handleMe(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"username":"alice"`) {
		t.Errorf("signed in: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(h.handleMe, "GET", "/api/me", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("no session = %d, want 401", rec.Code)
	}
	if rec := do(h.handleMe, "POST", "/api/me", ""); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST = %d, want 405", rec.Code)
	}
}

// signedIn makes a user row plus a session cookie and returns a request builder.
func signedIn(t *testing.T, d interface {
	CreateUser(string, string) (int64, error)
	CreateSession(string, string, time.Time) error
}, name string) func(method, path, body string) *http.Request {
	t.Helper()
	if _, err := d.CreateUser(name, "hash"); err != nil {
		t.Fatal(err)
	}
	if err := d.CreateSession("tok-"+name, name, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	return func(method, path, body string) *http.Request {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "tok-" + name})
		return req
	}
}

func TestPreferencesRejectBadInput(t *testing.T) {
	h, d := newTestHandler(t)
	as := signedIn(t, d, "alice")
	for name, body := range map[string]string{
		"unknown pattern":   `{"background":{"pattern":"glow","strength":6,"fade":true}}`,
		"strength too low":  `{"background":{"pattern":"grid","strength":1,"fade":true}}`,
		"strength too high": `{"background":{"pattern":"grid","strength":17,"fade":true}}`,
		"no pattern":        `{"background":{"strength":6}}`,
		"not json":          `nope`,
	} {
		rec := httptest.NewRecorder()
		h.handleMePreferences(rec, as("PUT", "/api/me/preferences", body))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: got %d, want 400", name, rec.Code)
		}
	}
	// nothing bad was stored
	rec := httptest.NewRecorder()
	h.handleMe(rec, as("GET", "/api/me", ""))
	if strings.Contains(rec.Body.String(), "background") {
		t.Errorf("rejected input was stored: %s", rec.Body.String())
	}
	// auth and method
	if rec := do(h.handleMePreferences, "PUT", "/api/me/preferences", `{}`); rec.Code != http.StatusUnauthorized {
		t.Errorf("no session = %d, want 401", rec.Code)
	}
	rec = httptest.NewRecorder()
	h.handleMePreferences(rec, as("POST", "/api/me/preferences", `{}`))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST = %d, want 405", rec.Code)
	}
}

func TestPreferencesPerUser(t *testing.T) {
	h, d := newTestHandler(t)
	alice := signedIn(t, d, "alice")
	bob := signedIn(t, d, "bob")

	rec := httptest.NewRecorder()
	h.handleMePreferences(rec, alice("PUT", "/api/me/preferences", `{"background":{"pattern":"hex","strength":9,"fade":false}}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("save: %d %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	h.handleMe(rec, alice("GET", "/api/me", ""))
	if !strings.Contains(rec.Body.String(), `"pattern":"hex"`) || !strings.Contains(rec.Body.String(), `"strength":9`) || !strings.Contains(rec.Body.String(), `"fade":false`) {
		t.Errorf("alice did not get her choice back: %s", rec.Body.String())
	}
	rec = httptest.NewRecorder()
	h.handleMe(rec, bob("GET", "/api/me", ""))
	if strings.Contains(rec.Body.String(), "hex") || !strings.Contains(rec.Body.String(), `"preferences":{}`) {
		t.Errorf("bob must keep the defaults: %s", rec.Body.String())
	}
}

func TestPreferencesUnreadableStoredValueMeansDefaults(t *testing.T) {
	h, d := newTestHandler(t)
	as := signedIn(t, d, "alice")
	for _, stored := range []string{`{broken`, `{"background":{"pattern":"glow","strength":6,"fade":true}}`} {
		if err := d.SetUserPreferences("alice", stored); err != nil {
			t.Fatal(err)
		}
		rec := httptest.NewRecorder()
		h.handleMe(rec, as("GET", "/api/me", ""))
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"preferences":{}`) {
			t.Errorf("stored %q: %d %s", stored, rec.Code, rec.Body.String())
		}
	}
}

func TestPreferencesForMissingUserIs404(t *testing.T) {
	h, d := newTestHandler(t)
	// a session for a name that has no users row (deleted account)
	if err := d.CreateSession("tok", "ghost", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("PUT", "/api/me/preferences", strings.NewReader(`{"background":{"pattern":"grid","strength":6,"fade":true}}`))
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "tok"})
	rec := httptest.NewRecorder()
	h.handleMePreferences(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("missing user = %d, want 404", rec.Code)
	}
}

func TestPreferencesProgressValidationAndRoundTrip(t *testing.T) {
	h, d := newTestHandler(t)
	as := signedIn(t, d, "alice")
	put := func(body string) int {
		rec := httptest.NewRecorder()
		h.handleMePreferences(rec, as("PUT", "/api/me/preferences", body))
		return rec.Code
	}
	for name, body := range map[string]string{
		"bad style":        `{"progress":{"color":"","style":"zigzag","stripe":4,"keep_amber":true}}`,
		"short hex":        `{"progress":{"color":"#fff","style":"solid","stripe":4,"keep_amber":true}}`,
		"named colour":     `{"progress":{"color":"red","style":"solid","stripe":4,"keep_amber":true}}`,
		"css injection":    `{"progress":{"color":"#ffffff;background:url(x)","style":"solid","stripe":4,"keep_amber":true}}`,
		"stripe too low":   `{"progress":{"color":"","style":"striped","stripe":0,"keep_amber":true}}`,
		"stripe too high":  `{"progress":{"color":"","style":"striped","stripe":9,"keep_amber":true}}`,
		"missing style":    `{"progress":{"color":"#aabbcc","stripe":4}}`,
		"bad bg, good bar": `{"background":{"pattern":"glow","strength":6,"fade":true},"progress":{"color":"","style":"solid","stripe":4,"keep_amber":true}}`,
	} {
		if code := put(body); code != http.StatusBadRequest {
			t.Errorf("%s: got %d, want 400", name, code)
		}
	}

	if code := put(`{"progress":{"color":"#A855F7","style":"striped","stripe":6,"keep_amber":false}}`); code != http.StatusOK {
		t.Fatalf("valid save = %d", code)
	}
	rec := httptest.NewRecorder()
	h.handleMe(rec, as("GET", "/api/me", ""))
	body := rec.Body.String()
	for _, want := range []string{`"color":"#a855f7"`, `"style":"striped"`, `"stripe":6`, `"keep_amber":false`} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %s in %s", want, body)
		}
	}
	// default colour ("" = theme green) is a valid choice
	if code := put(`{"progress":{"color":"","style":"gradient","stripe":4,"keep_amber":true}}`); code != http.StatusOK {
		t.Errorf("empty colour = %d, want 200", code)
	}
}

func TestPreferencesInvalidStoredProgressMeansDefaults(t *testing.T) {
	h, d := newTestHandler(t)
	as := signedIn(t, d, "alice")
	if err := d.SetUserPreferences("alice", `{"progress":{"color":"javascript:x","style":"solid","stripe":4,"keep_amber":true}}`); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.handleMe(rec, as("GET", "/api/me", ""))
	if strings.Contains(rec.Body.String(), "javascript") || !strings.Contains(rec.Body.String(), `"preferences":{}`) {
		t.Errorf("bad stored colour leaked: %s", rec.Body.String())
	}
}
