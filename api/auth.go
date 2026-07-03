package api

import (
	"crypto/rand"
	"encoding/base32"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const (
	loginMaxAttempts = 5
	loginWindow      = 15 * time.Minute
	minPasswordLen   = 8

	sessionCookieName = "printspy_session"
	sessionDuration   = 30 * 24 * time.Hour
)

func hashPassword(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	return string(hash), err
}

func checkPassword(hash, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

func newSessionToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b), nil
}

// createUser hashes password and inserts a new user row.
func (h *Handler) createUser(username, password string) (int64, error) {
	hash, err := hashPassword(password)
	if err != nil {
		return 0, err
	}
	return h.db.CreateUser(username, hash)
}

// RequireAuth gates every request behind a login. Users bootstrap through
// /setup on first run; after that /login issues a session cookie.
func (h *Handler) RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/setup", "/login", "/style.css", "/logo.png":
			next.ServeHTTP(w, r)
			return
		}

		n, err := h.db.CountUsers()
		if err != nil {
			jsonError(w, "database error", http.StatusInternalServerError)
			return
		}
		if n == 0 {
			http.Redirect(w, r, "/setup", http.StatusFound)
			return
		}

		if _, ok := h.currentUser(r); !ok {
			if strings.HasPrefix(r.URL.Path, "/api/") {
				jsonError(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func (h *Handler) currentUser(r *http.Request) (string, bool) {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil {
		return "", false
	}
	username, err := h.db.GetSessionUser(cookie.Value)
	if err != nil || username == "" {
		return "", false
	}
	return username, true
}

func (h *Handler) startSession(w http.ResponseWriter, username string) error {
	token, err := newSessionToken()
	if err != nil {
		return err
	}
	expiresAt := time.Now().Add(sessionDuration)
	if err := h.db.CreateSession(token, username, expiresAt); err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		Expires:  expiresAt,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	return nil
}

// rateLimited reports whether key has failed to log in loginMaxAttempts times
// within loginWindow, pruning stale attempts as it goes.
func (h *Handler) rateLimited(key string) bool {
	h.loginMu.Lock()
	defer h.loginMu.Unlock()
	now := time.Now()
	attempts := h.loginFails[key]
	fresh := attempts[:0]
	for _, t := range attempts {
		if now.Sub(t) < loginWindow {
			fresh = append(fresh, t)
		}
	}
	h.loginFails[key] = fresh
	return len(fresh) >= loginMaxAttempts
}

func (h *Handler) recordLoginFailure(key string) {
	h.loginMu.Lock()
	defer h.loginMu.Unlock()
	h.loginFails[key] = append(h.loginFails[key], time.Now())
}

func (h *Handler) clearLoginFailures(key string) {
	h.loginMu.Lock()
	defer h.loginMu.Unlock()
	delete(h.loginFails, key)
}

func (h *Handler) handleSetup(w http.ResponseWriter, r *http.Request) {
	n, err := h.db.CountUsers()
	if err != nil {
		jsonError(w, "database error", http.StatusInternalServerError)
		return
	}
	if n > 0 {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}

	switch r.Method {
	case http.MethodGet:
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(setupPageHTML))

	case http.MethodPost:
		if err := r.ParseForm(); err != nil {
			http.Redirect(w, r, "/setup?error=1", http.StatusFound)
			return
		}
		username := strings.TrimSpace(r.FormValue("username"))
		password := r.FormValue("password")
		if username == "" || len(password) < minPasswordLen {
			http.Redirect(w, r, "/setup?error=1", http.StatusFound)
			return
		}

		// Single mutex around check-then-insert closes the race between two
		// concurrent /setup POSTs both seeing zero users.
		h.setupMu.Lock()
		defer h.setupMu.Unlock()

		n, err := h.db.CountUsers()
		if err != nil || n > 0 {
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}

		if _, err := h.createUser(username, password); err != nil {
			http.Redirect(w, r, "/setup?error=1", http.StatusFound)
			return
		}
		if err := h.startSession(w, username); err != nil {
			jsonError(w, "failed to start session", http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, "/", http.StatusFound)

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (h *Handler) handleLogin(w http.ResponseWriter, r *http.Request) {
	n, err := h.db.CountUsers()
	if err != nil {
		jsonError(w, "database error", http.StatusInternalServerError)
		return
	}
	if n == 0 {
		http.Redirect(w, r, "/setup", http.StatusFound)
		return
	}

	switch r.Method {
	case http.MethodGet:
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(loginPageHTML))

	case http.MethodPost:
		if err := r.ParseForm(); err != nil {
			http.Redirect(w, r, "/login?error=1", http.StatusFound)
			return
		}
		username := strings.TrimSpace(r.FormValue("username"))
		password := r.FormValue("password")

		if h.rateLimited(username) {
			http.Redirect(w, r, "/login?error=ratelimit", http.StatusFound)
			return
		}

		user, err := h.db.GetUserByUsername(username)
		if err != nil || !checkPassword(user.PasswordHash, password) {
			h.recordLoginFailure(username)
			http.Redirect(w, r, "/login?error=1", http.StatusFound)
			return
		}

		h.clearLoginFailures(username)
		if err := h.startSession(w, username); err != nil {
			jsonError(w, "failed to start session", http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, "/", http.StatusFound)

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (h *Handler) handleLogout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(sessionCookieName); err == nil {
		h.db.DeleteSession(cookie.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Value: "", Path: "/", MaxAge: -1})
	http.Redirect(w, r, "/login", http.StatusFound)
}

func (h *Handler) handleUsers(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		users, err := h.db.ListUsers()
		if err != nil {
			jsonError(w, "database error", http.StatusInternalServerError)
			return
		}
		jsonResponse(w, users)

	case http.MethodPost:
		var req struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			jsonError(w, "invalid request body", http.StatusBadRequest)
			return
		}
		req.Username = strings.TrimSpace(req.Username)
		if req.Username == "" {
			jsonError(w, "username is required", http.StatusBadRequest)
			return
		}
		if len(req.Password) < minPasswordLen {
			jsonError(w, "password must be at least 8 characters", http.StatusBadRequest)
			return
		}
		id, err := h.createUser(req.Username, req.Password)
		if err != nil {
			jsonError(w, "username already exists", http.StatusConflict)
			return
		}
		jsonResponse(w, map[string]int64{"id": id})

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (h *Handler) handleUserByID(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	idStr := strings.TrimPrefix(r.URL.Path, "/api/users/")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		jsonError(w, "invalid user id", http.StatusBadRequest)
		return
	}

	target, err := h.db.GetUser(id)
	if err != nil {
		jsonError(w, "user not found", http.StatusNotFound)
		return
	}

	currentUsername, _ := h.currentUser(r)
	if target.Username == currentUsername {
		jsonError(w, "cannot delete your own account", http.StatusConflict)
		return
	}

	n, err := h.db.CountUsers()
	if err != nil {
		jsonError(w, "database error", http.StatusInternalServerError)
		return
	}
	if n <= 1 {
		jsonError(w, "cannot delete the last user", http.StatusConflict)
		return
	}

	if err := h.db.DeleteUser(id); err != nil {
		jsonError(w, "failed to delete user", http.StatusInternalServerError)
		return
	}
	h.db.DeleteSessionsForUser(target.Username)
	w.WriteHeader(http.StatusNoContent)
}

const authPageHead = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>PrintSpy</title>
<link rel="icon" href="/logo.png">
<link rel="stylesheet" href="/style.css">
<style>
.auth-page { display:flex; align-items:center; justify-content:center; min-height:100vh; }
.auth-card { width:100%; max-width:360px; background:var(--bg-primary); border:1px solid var(--border); border-radius:var(--radius-lg); padding:2rem; }
.auth-card .logo { display:block; width:100%; max-width:280px; height:auto; margin:0 auto 1.5rem; }
.auth-card h1 { font-size:1.25rem; margin-bottom:1.5rem; }
.auth-error { background:var(--error-bg); color:var(--error); padding:0.5rem 0.75rem; border-radius:var(--radius); font-size:0.875rem; margin-bottom:1rem; display:none; }
</style>
</head>
<body>
<div class="auth-page"><div class="auth-card">
<img class="logo" src="/logo.png" alt="">
<h1>PrintSpy</h1>
<div class="auth-error" id="auth-error"></div>
`

const authPageFoot = `
<script>
const params = new URLSearchParams(location.search);
if (params.get('error')) {
    const el = document.getElementById('auth-error');
    el.textContent = params.get('error') === 'ratelimit'
        ? 'Too many attempts. Try again later.'
        : 'Incorrect username or password.';
    el.style.display = 'block';
}
</script>
</div></div>
</body>
</html>`

const loginPageHTML = authPageHead + `
<form method="POST" action="/login">
    <div class="form-group">
        <label for="username">Username</label>
        <input type="text" id="username" name="username" autofocus required>
    </div>
    <div class="form-group">
        <label for="password">Password</label>
        <input type="password" id="password" name="password" required>
    </div>
    <div class="form-actions">
        <button type="submit" class="btn btn-primary">Log in</button>
    </div>
</form>` + authPageFoot

const setupPageHTML = authPageHead + `
<p style="color:var(--text-secondary);font-size:0.875rem;margin-bottom:1rem">Create the first account to secure this PrintSpy instance.</p>
<form method="POST" action="/setup">
    <div class="form-group">
        <label for="username">Username</label>
        <input type="text" id="username" name="username" autofocus required>
    </div>
    <div class="form-group">
        <label for="password">Password</label>
        <input type="password" id="password" name="password" required minlength="8">
    </div>
    <div class="form-actions">
        <button type="submit" class="btn btn-primary">Create account</button>
    </div>
</form>` + authPageFoot
