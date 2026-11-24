package digestauth

import (
	"net/http"
	"strings"
	"testing"
)

func fixedCNonce(c string) func() {
	old := newCNonce
	newCNonce = func() string { return c }
	return func() { newCNonce = old }
}

// RFC 2617 section 3.5 example.
func TestBuildHeaderRFC2617(t *testing.T) {
	defer fixedCNonce("0a4f113b")()
	got := BuildHeader("Mufasa", "Circle Of Life", "GET", "/dir/index.html",
		`Digest realm="testrealm@host.com", qop="auth,auth-int", nonce="dcd98b7102dd2f0e8b11d0f600bfb0c093", opaque="5ccc069c403ebaf9f0171e9517f40e41"`)
	for _, want := range []string{`response="6629fae49393a05397450978507c4ef1"`, `qop=auth,`, `opaque="5ccc069c403ebaf9f0171e9517f40e41"`} {
		if !strings.Contains(got, want) {
			t.Errorf("header %s\nmissing %s", got, want)
		}
	}
}

// RFC 7616 section 3.9.1 example, both algorithms.
func TestBuildHeaderRFC7616(t *testing.T) {
	defer fixedCNonce("f2/wE4q74E6zIJEtWaHKaf5wv/H5QzzpXusqGemxURZJ")()
	const ch = `Digest realm="http-auth@example.org", qop="auth, auth-int", nonce="7ypf/xlj9XXwfDPEoM4URrv/xwf94BcCAzFZH4GiTo0v", opaque="FQhe/qaU925kfnzjCev0ciny7QMkPqMAFRtzCUYo5tdS"`
	for alg, resp := range map[string]string{
		"MD5":     "8ca523f5e9506fed4657c9700eebdbec",
		"SHA-256": "753927fa0e85d155564e2e272a28d1802ca10daf4496794697cf8db5856cb6c1",
	} {
		got := BuildHeader("Mufasa", "Circle of Life", "GET", "/dir/index.html", ch+`, algorithm=`+alg)
		if !strings.Contains(got, `response="`+resp+`"`) || !strings.Contains(got, `algorithm=`+alg) {
			t.Errorf("%s: header %s\nwant response %s and algorithm echoed", alg, got, resp)
		}
	}
}

// A server offering only auth-int must not be answered with qop=auth it
// never offered: without the body PrintSpy cannot do auth-int, so it falls
// back to the RFC 2069 form (no qop).
func TestBuildHeaderAuthIntOnly(t *testing.T) {
	got := BuildHeader("u", "p", "GET", "/", `Digest realm="r", nonce="n", qop="auth-int"`)
	if strings.Contains(got, "qop=") {
		t.Errorf("answered qop for an auth-int-only challenge: %s", got)
	}
}

// With a Basic challenge sent before the Digest one, the Digest one is used.
func TestChallengePicksDigest(t *testing.T) {
	h := http.Header{}
	h.Add("WWW-Authenticate", `Basic realm="x"`)
	h.Add("WWW-Authenticate", `Digest realm="r", nonce="n"`)
	if got := Challenge(h); got != `Digest realm="r", nonce="n"` {
		t.Errorf("Challenge = %q", got)
	}
	h2 := http.Header{}
	h2.Add("WWW-Authenticate", `Basic realm="x"`)
	if got := Challenge(h2); got != "" {
		t.Errorf("Challenge without Digest = %q, want empty", got)
	}
}
