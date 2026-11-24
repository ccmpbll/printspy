package digestauth

import (
	"crypto/md5"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"net/http"
	"slices"
	"strings"
)

// newCNonce makes the client nonce; swappable in tests.
var newCNonce = rand.Text

// Challenge returns the Digest challenge among a response's
// WWW-Authenticate headers ("" if none), so a Basic challenge sent first
// does not hide it.
func Challenge(h http.Header) string {
	for _, v := range h.Values("WWW-Authenticate") {
		if len(v) >= 7 && strings.EqualFold(v[:7], "Digest ") {
			return v
		}
	}
	return ""
}

// BuildHeader answers a Digest challenge (RFC 7616: MD5 or SHA-256, not
// the -sess variants, which PrusaLink does not use; qop=auth, or the RFC 2069 form when the server offers
// no qop PrintSpy can do - auth-int needs the body, which callers do not
// pass).
func BuildHeader(username, password, method, uri, wwwAuth string) string {
	params := parseChallenge(wwwAuth)
	realm := params["realm"]
	nonce := params["nonce"]
	alg, hasAlg := params["algorithm"]
	qopAuth := slices.ContainsFunc(strings.Split(params["qop"], ","), func(q string) bool {
		return strings.TrimSpace(q) == "auth"
	})

	newHash := md5.New
	if strings.HasPrefix(strings.ToUpper(alg), "SHA-256") {
		newHash = sha256.New
	}
	h := func(s string) string { return hashHex(newHash, s) }

	cnonce := newCNonce()
	nc := "00000001"
	ha1 := h(username + ":" + realm + ":" + password)
	ha2 := h(method + ":" + uri)

	var response string
	if qopAuth {
		response = h(ha1 + ":" + nonce + ":" + nc + ":" + cnonce + ":auth:" + ha2)
	} else {
		response = h(ha1 + ":" + nonce + ":" + ha2)
	}

	auth := fmt.Sprintf(`Digest username="%s", realm="%s", nonce="%s", uri="%s", response="%s"`,
		username, realm, nonce, uri, response)
	if hasAlg {
		auth += ", algorithm=" + alg
	}
	if qopAuth {
		auth += fmt.Sprintf(`, qop=auth, nc=%s, cnonce="%s"`, nc, cnonce)
	}
	if opaque, ok := params["opaque"]; ok {
		auth += fmt.Sprintf(`, opaque="%s"`, opaque)
	}
	return auth
}

func parseChallenge(header string) map[string]string {
	params := make(map[string]string)
	header = strings.TrimPrefix(header, "Digest ")

	inQuote := false
	start := 0
	for i := 0; i < len(header); i++ {
		if header[i] == '"' {
			inQuote = !inQuote
		} else if header[i] == ',' && !inQuote {
			parseParam(header[start:i], params)
			start = i + 1
		}
	}
	if start < len(header) {
		parseParam(header[start:], params)
	}
	return params
}

func parseParam(s string, params map[string]string) {
	s = strings.TrimSpace(s)
	eq := strings.IndexByte(s, '=')
	if eq < 0 {
		return
	}
	key := strings.TrimSpace(s[:eq])
	val := strings.TrimSpace(s[eq+1:])
	val = strings.Trim(val, `"`)
	params[key] = val
}

func hashHex(newHash func() hash.Hash, s string) string {
	hh := newHash()
	hh.Write([]byte(s))
	return hex.EncodeToString(hh.Sum(nil))
}
