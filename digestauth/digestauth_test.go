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

// No qop offered (what PrusaLink sends, checked 2026-11-24): the RFC 2069
// form, response = H(HA1:nonce:HA2), no qop/nc/cnonce.
func TestBuildHeaderNoQop(t *testing.T) {
	got := BuildHeader("Mufasa", "Circle Of Life", "GET", "/dir/index.html",
		`Digest realm="testrealm@host.com", nonce="dcd98b7102dd2f0e8b11d0f600bfb0c093"`)
	if !strings.Contains(got, `response="670fd8c2df070c60b045671b8b24ff02"`) || strings.Contains(got, "nc=") {
		t.Errorf("header %s\nwant the RFC 2069 response and no nc", got)
	}
}

// The nonce count is sent as given, and the response covers it.
func TestBuildHeaderNC(t *testing.T) {
	defer fixedCNonce("c")()
	ch := `Digest realm="r", nonce="n", qop="auth"`
	one, two := BuildHeaderNC("u", "p", "GET", "/", ch, 1), BuildHeaderNC("u", "p", "GET", "/", ch, 2)
	if !strings.Contains(two, "nc=00000002") || one == strings.Replace(two, "nc=00000002", "nc=00000001", 1) {
		t.Errorf("nc 2 not sent or not hashed:\n%s\n%s", one, two)
	}
}

// The scheme name is case-insensitive (RFC 7235).
func TestBuildHeaderLowerCaseScheme(t *testing.T) {
	got := BuildHeader("Mufasa", "Circle Of Life", "GET", "/dir/index.html",
		`digest realm="testrealm@host.com", nonce="dcd98b7102dd2f0e8b11d0f600bfb0c093"`)
	if !strings.Contains(got, `response="670fd8c2df070c60b045671b8b24ff02"`) {
		t.Errorf("lower-case scheme not parsed: %s", got)
	}
}

// An algorithm PrintSpy cannot do is not claimed in the answer.
func TestBuildHeaderUnknownAlgorithm(t *testing.T) {
	got := BuildHeader("u", "p", "GET", "/", `Digest realm="r", nonce="n", algorithm=MD5-sess`)
	if strings.Contains(got, "algorithm=") {
		t.Errorf("claimed an unsupported algorithm: %s", got)
	}
}
