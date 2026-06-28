package digestauth

import (
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
)

func BuildHeader(username, password, method, uri, wwwAuth string) string {
	params := parseChallenge(wwwAuth)
	realm := params["realm"]
	nonce := params["nonce"]
	qop := params["qop"]

	ha1 := hash(username + ":" + realm + ":" + password)
	ha2 := hash(method + ":" + uri)

	cnonce := generateCNonce()
	nc := "00000001"

	var response string
	if strings.Contains(qop, "auth") {
		response = hash(ha1 + ":" + nonce + ":" + nc + ":" + cnonce + ":auth:" + ha2)
	} else {
		response = hash(ha1 + ":" + nonce + ":" + ha2)
	}

	auth := fmt.Sprintf(`Digest username="%s", realm="%s", nonce="%s", uri="%s", response="%s"`,
		username, realm, nonce, uri, response)
	if qop != "" {
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

func hash(s string) string {
	h := md5.Sum([]byte(s))
	return hex.EncodeToString(h[:])
}

func generateCNonce() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}
