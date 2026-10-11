package proxy

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const ociTestKeyIDPrefix = "ocid1.tenancy.oc1..aaaatenant/ocid1.user.oc1..aaaauser/"

var ociTestNow = time.Date(2026, time.October, 11, 18, 42, 0, 0, time.UTC)

// newTestOCIIdentity returns a fresh RSA key with the PEM and key id a policy would carry.
func newTestOCIIdentity(t *testing.T) (*rsa.PrivateKey, string, string) {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa.GenerateKey() error = %v", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("x509.MarshalPKCS8PrivateKey() error = %v", err)
	}
	pemText := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))

	return key, pemText, ociTestKeyIDPrefix + ociFingerprintMD5(&key.PublicKey)
}

func TestOCISigningStringMatchesTheCLIScheme(t *testing.T) {
	tests := []struct {
		name   string
		method string
		path   string
		names  []string
		values map[string]string
		want   string
	}{
		{
			name:   "get",
			method: "GET",
			path:   "/20160918/instances?compartmentId=ocid1.compartment.oc1..aaaa",
			names:  []string{"date", "(request-target)", "host"},
			values: map[string]string{
				"date": "Sun, 11 Oct 2026 18:42:00 GMT",
				"host": "iaas.eu-frankfurt-1.oraclecloud.com",
			},
			want: "date: Sun, 11 Oct 2026 18:42:00 GMT\n" +
				"(request-target): get /20160918/instances?compartmentId=ocid1.compartment.oc1..aaaa\n" +
				"host: iaas.eu-frankfurt-1.oraclecloud.com",
		},
		{
			name:   "post signs the body headers too",
			method: "POST",
			path:   "/20160918/instances",
			names: []string{
				"date", "(request-target)", "host",
				"content-length", "content-type", "x-content-sha256",
			},
			values: map[string]string{
				"date":             "Sun, 11 Oct 2026 18:42:00 GMT",
				"host":             "iaas.eu-frankfurt-1.oraclecloud.com",
				"content-length":   "17",
				"content-type":     "application/json",
				"x-content-sha256": "Zm9v",
			},
			want: "date: Sun, 11 Oct 2026 18:42:00 GMT\n" +
				"(request-target): post /20160918/instances\n" +
				"host: iaas.eu-frankfurt-1.oraclecloud.com\n" +
				"content-length: 17\n" +
				"content-type: application/json\n" +
				"x-content-sha256: Zm9v",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := ociSigningString(test.names, test.values, test.method, test.path)
			if got != test.want {
				t.Fatalf("ociSigningString() =\n%q\nwant\n%q", got, test.want)
			}
		})
	}
}

func TestOCISignsBodyOnlyForBodyMethods(t *testing.T) {
	tests := map[string]bool{
		"GET":     false,
		"HEAD":    false,
		"DELETE":  false,
		"get":     false,
		"PUT":     true,
		"POST":    true,
		"PATCH":   true,
		" post ":  true,
		"OPTIONS": false,
	}
	for method, want := range tests {
		if got := ociSignsBody(method); got != want {
			t.Fatalf("ociSignsBody(%q) = %v, want %v", method, got, want)
		}
	}
}

func TestOCIAuthorizationHeaderFormat(t *testing.T) {
	got := ociAuthorizationHeader(
		"ocid1.tenancy.oc1..t/ocid1.user.oc1..u/12:34:56:78",
		[]string{"date", "(request-target)", "host"},
		"c2ln",
	)
	want := `Signature algorithm="rsa-sha256",` +
		`headers="date (request-target) host",` +
		`keyId="ocid1.tenancy.oc1..t/ocid1.user.oc1..u/12:34:56:78",` +
		`signature="c2ln",version="1"`
	if got != want {
		t.Fatalf("ociAuthorizationHeader() = %q, want %q", got, want)
	}
}

func TestOCISignedHeaderValuesCollectsTheRequestValues(t *testing.T) {
	tests := []struct {
		name        string
		method      string
		body        string
		header      map[string]string
		wantNames   []string
		wantValues  map[string]string
		wantDateSet bool
	}{
		{
			name:      "get signs the request line, date and host",
			method:    http.MethodGet,
			header:    map[string]string{"Date": "Sun, 11 Oct 2026 18:42:00 GMT"},
			wantNames: []string{"date", "(request-target)", "host"},
			wantValues: map[string]string{
				"date": "Sun, 11 Oct 2026 18:42:00 GMT",
				"host": "iaas.eu-frankfurt-1.oraclecloud.com",
			},
		},
		{
			name:   "post signs the body headers",
			method: http.MethodPost,
			body:   `{"availabilityDomain":"EU-FRANKFURT-1-AD-1"}`,
			header: map[string]string{
				"Content-Type":     "application/json",
				"X-Content-Sha256": "Zm9vYmFy",
			},
			wantNames: []string{
				"date", "(request-target)", "host",
				"content-length", "content-type", "x-content-sha256",
			},
			wantValues: map[string]string{
				"host":             "iaas.eu-frankfurt-1.oraclecloud.com",
				"content-length":   "auto",
				"content-type":     "application/json",
				"x-content-sha256": "Zm9vYmFy",
				"date":             ociTestNow.Format(http.TimeFormat),
			},
			wantDateSet: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest(
				test.method,
				"https://iaas.eu-frankfurt-1.oraclecloud.com/20160918/instances",
				strings.NewReader(test.body),
			)
			for name, value := range test.header {
				req.Header.Set(name, value)
			}

			names, values, err := ociSignedHeaderValues(req, ociTestNow)
			if err != nil {
				t.Fatalf("ociSignedHeaderValues() error = %v", err)
			}
			if strings.Join(names, " ") != strings.Join(test.wantNames, " ") {
				t.Fatalf("names = %v, want %v", names, test.wantNames)
			}
			if len(values) != len(test.wantValues) {
				t.Fatalf("values = %v, want %v", values, test.wantValues)
			}
			for name, want := range test.wantValues {
				if want == "auto" {
					want = strconv.Itoa(len(test.body))
				}
				if got := values[name]; got != want {
					t.Fatalf("values[%q] = %q, want %q", name, got, want)
				}
			}
			if test.wantDateSet && req.Header.Get("Date") != ociTestNow.Format(http.TimeFormat) {
				t.Fatalf("expected the proxy to set a Date header, got %q", req.Header.Get("Date"))
			}
		})
	}
}

func TestOCISignedHeaderValueRejectsUnsignableRequests(t *testing.T) {
	tests := []struct {
		name    string
		header  string
		mutate  func(*http.Request)
		wantErr string
	}{
		{
			name:    "host",
			header:  "host",
			mutate:  func(req *http.Request) { req.Host = ""; req.URL.Host = "" },
			wantErr: "no host header",
		},
		{
			name:    "content-type",
			header:  "content-type",
			wantErr: "no content type",
		},
		{
			name:    "x-content-sha256",
			header:  "x-content-sha256",
			wantErr: "no x-content-sha256 header",
		},
		{
			name:    "content-length",
			header:  "content-length",
			mutate:  func(req *http.Request) { req.ContentLength = -1 },
			wantErr: "no content length",
		},
		{
			name:    "unknown header",
			header:  "accept",
			wantErr: "unsupported signed header",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest(
				http.MethodPost,
				"https://iaas.eu-frankfurt-1.oraclecloud.com/20160918/instances",
				strings.NewReader("{}"),
			)
			req.ContentLength = 2
			if test.mutate != nil {
				test.mutate(req)
			}

			_, err := ociSignedHeaderValue(req, test.header, ociTestNow)
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf(
					"ociSignedHeaderValue(%q) error = %v, want %q",
					test.header,
					err,
					test.wantErr,
				)
			}
		})
	}
}

func TestOCISignerApplyReplacesAuthorization(t *testing.T) {
	key, pemText, keyID := newTestOCIIdentity(t)
	signer, err := compileOCISigner(
		0,
		OCISign{Secret: "oci_api_key", KeyID: keyID},
		map[string]string{
			"oci_api_key": pemText,
		},
	)
	if err != nil {
		t.Fatalf("compileOCISigner() error = %v", err)
	}

	req := httptest.NewRequest(
		http.MethodGet,
		"https://iaas.eu-frankfurt-1.oraclecloud.com/20160918/instances",
		nil,
	)
	req.Header.Set("Authorization", "Bearer placeholder-from-the-sandbox")
	req.Header.Set("Date", ociTestNow.Format(http.TimeFormat))

	if err := signer.apply(req, ociTestNow); err != nil {
		t.Fatalf("apply() error = %v", err)
	}

	auth := req.Header.Get("Authorization")
	if strings.Contains(auth, "placeholder") || strings.HasPrefix(auth, "Bearer") {
		t.Fatalf("Authorization = %q, want the signature to replace the caller's value", auth)
	}
	verifyOCISignature(t, req, nil, &key.PublicKey, keyID)
}

func TestOCISignerApplyRejectsMissingInput(t *testing.T) {
	var nilSigner *compiledOCISigner
	if err := nilSigner.apply(httptest.NewRequest(http.MethodGet, "https://example.com/", nil), ociTestNow); err == nil {
		t.Fatal("expected an error for a signer without a key")
	}

	noKey := &compiledOCISigner{keyID: ociTestKeyIDPrefix + "12:34:56:78"}
	if err := noKey.apply(httptest.NewRequest(http.MethodGet, "https://example.com/", nil), ociTestNow); err == nil {
		t.Fatal("expected an error for a signer whose key failed to parse")
	}

	if err := noKey.apply(nil, ociTestNow); err == nil {
		t.Fatal("expected an error for a nil request")
	}

	broken := &compiledOCISigner{keyID: ociTestKeyIDPrefix + "12:34:56:78", key: unusableTestKey()}
	if err := broken.apply(httptest.NewRequest(http.MethodGet, "https://example.com/", nil), ociTestNow); err == nil {
		t.Fatal("expected an error rather than an unsigned request when the key cannot sign")
	}
}

func TestOCISignatureRejectsUnusableKeys(t *testing.T) {
	if _, err := ociSignature(nil, "message"); err == nil {
		t.Fatal("expected an error for a missing private key")
	}

	if _, err := ociSignature(unusableTestKey(), "message"); err == nil {
		t.Fatal("expected an error for a key too small to sign with")
	}
}

// unusableTestKey is a key this toolchain will not generate, but which a policy could still
// hand us: too small for RSA to sign a SHA-256 digest with.
func unusableTestKey() *rsa.PrivateKey {
	return &rsa.PrivateKey{
		PublicKey: rsa.PublicKey{N: big.NewInt(3), E: 65537},
		D:         big.NewInt(1),
		Primes:    []*big.Int{big.NewInt(3)},
	}
}

func TestRequestPathFallsBackToRoot(t *testing.T) {
	if got := requestPath(nil); got != "/" {
		t.Fatalf("requestPath(nil) = %q, want %q", got, "/")
	}
	if got := requestPath(&http.Request{}); got != "/" {
		t.Fatalf("requestPath(request without URL) = %q, want %q", got, "/")
	}
	req := httptest.NewRequest(http.MethodGet, "https://example.com/a/b?c=d", nil)
	if got := requestPath(req); got != "/a/b?c=d" {
		t.Fatalf("requestPath() = %q, want %q", got, "/a/b?c=d")
	}
}

func TestParseOCIPrivateKey(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa.GenerateKey() error = %v", err)
	}
	pkcs8, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("MarshalPKCS8PrivateKey() error = %v", err)
	}
	pkcs1 := x509.MarshalPKCS1PrivateKey(key)

	ecPrivate, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("ecdsa.GenerateKey() error = %v", err)
	}
	ecKey, err := x509.MarshalPKCS8PrivateKey(ecPrivate)
	if err != nil {
		t.Fatalf("x509.MarshalPKCS8PrivateKey(EC) error = %v", err)
	}

	tests := []struct {
		name    string
		pemText string
		wantErr string
	}{
		{
			name:    "pkcs8",
			pemText: string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8})),
		},
		{
			name:    "pkcs1",
			pemText: string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: pkcs1})),
		},
		{
			name:    "not pem",
			pemText: "hunter2",
			wantErr: "not a PEM encoded private key",
		},
		{
			name: "encrypted",
			pemText: string(
				pem.EncodeToMemory(&pem.Block{Type: "ENCRYPTED PRIVATE KEY", Bytes: pkcs8}),
			),
			wantErr: "passphrase protected keys are not supported",
		},
		{
			name:    "ec key",
			pemText: string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: ecKey})),
			wantErr: "want RSA",
		},
		{
			name: "garbage payload",
			pemText: string(
				pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("nope")}),
			),
			wantErr: "parse private key",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			parsed, err := parseOCIPrivateKey(test.pemText)
			if test.wantErr == "" {
				if err != nil {
					t.Fatalf("parseOCIPrivateKey() error = %v", err)
				}
				if !parsed.Equal(key) {
					t.Fatal("parsed key does not match the generated key")
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("parseOCIPrivateKey() error = %v, want %q", err, test.wantErr)
			}
		})
	}
}

func TestCompileOCISignerValidatesTheKeyAndIdentity(t *testing.T) {
	key, pemText, keyID := newTestOCIIdentity(t)
	_, _, otherKeyID := newTestOCIIdentity(t)
	_, _, otherFingerprint, err := splitOCIKeyID(otherKeyID)
	if err != nil {
		t.Fatalf("splitOCIKeyID() error = %v", err)
	}

	tests := []struct {
		name       string
		sign       OCISign
		secrets    map[string]string
		wantErr    string
		wantKeyID  string
		wantPubicK *rsa.PublicKey
	}{
		{
			name:       "valid",
			sign:       OCISign{Secret: "oci_api_key", KeyID: keyID},
			secrets:    map[string]string{"oci_api_key": pemText},
			wantKeyID:  keyID,
			wantPubicK: &key.PublicKey,
		},
		{
			name:    "missing secret value",
			sign:    OCISign{Secret: "oci_api_key", KeyID: keyID},
			secrets: map[string]string{},
			wantErr: `sign_oci.secret missing value for alias "oci_api_key"`,
		},
		{
			name:    "not a key",
			sign:    OCISign{Secret: "oci_api_key", KeyID: keyID},
			secrets: map[string]string{"oci_api_key": "hunter2"},
			wantErr: "not a PEM encoded private key",
		},
		{
			name:    "malformed key id",
			sign:    OCISign{Secret: "oci_api_key", KeyID: "not-a-key-id"},
			secrets: map[string]string{"oci_api_key": pemText},
			wantErr: "must be <tenancy ocid>/<user ocid>/<key fingerprint>",
		},
		{
			name:    "fingerprint belongs to another key",
			sign:    OCISign{Secret: "oci_api_key", KeyID: ociTestKeyIDPrefix + otherFingerprint},
			secrets: map[string]string{"oci_api_key": pemText},
			wantErr: "does not match secret",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			signer, err := compileOCISigner(0, test.sign, test.secrets)
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("compileOCISigner() error = %v, want %q", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("compileOCISigner() error = %v", err)
			}
			if signer.keyID != test.wantKeyID {
				t.Fatalf("keyID = %q, want %q", signer.keyID, test.wantKeyID)
			}
			if !signer.key.PublicKey.Equal(test.wantPubicK) {
				t.Fatal("compiled key does not match the configured key")
			}
		})
	}

}

func TestOCIFingerprintsAndMatching(t *testing.T) {
	key, _, _ := newTestOCIIdentity(t)

	md5Print := ociFingerprintMD5(&key.PublicKey)

	if got := len(strings.Split(md5Print, ":")); got != 16 {
		t.Fatalf("MD5 fingerprint has %d pairs, want 16 (%q)", got, md5Print)
	}
	if md5Print != strings.ToUpper(md5Print) {
		t.Fatalf("fingerprint %q is not upper case hex", md5Print)
	}

	tests := []struct {
		name        string
		fingerprint string
		want        bool
	}{
		{name: "md5 colon form", fingerprint: md5Print, want: true},
		{name: "md5 lower case", fingerprint: strings.ToLower(md5Print), want: true},
		{
			name:        "md5 without colons",
			fingerprint: strings.ReplaceAll(md5Print, ":", ""),
			want:        true,
		},
		{
			name:        "a sha256 digest of the same key is not the fingerprint OCI registers",
			fingerprint: colonSeparatedHex(sha256Sum(ociPublicKeyDER(&key.PublicKey))),
			want:        false,
		},
		{name: "one nibble off", fingerprint: flipFirstNibble(md5Print), want: false},
		{name: "empty", fingerprint: "", want: false},
		{name: "wrong length", fingerprint: "12:34:56", want: false},
		{name: "not hex", fingerprint: strings.Repeat("zz:", 15) + "zz", want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := ociFingerprintMatches(&key.PublicKey, test.fingerprint); got != test.want {
				t.Fatalf(
					"ociFingerprintMatches(%q) = %v, want %v",
					test.fingerprint,
					got,
					test.want,
				)
			}
		})
	}

	if ociFingerprintMatches(nil, md5Print) {
		t.Fatal("expected a nil public key to match nothing")
	}
}

func TestSplitOCIKeyID(t *testing.T) {
	_, _, generatedKeyID := newTestOCIIdentity(t)
	parts := strings.Split(generatedKeyID, "/")
	if len(parts) != 3 {
		t.Fatalf("test key id %q is not a triple", generatedKeyID)
	}
	fingerprint := parts[2]

	tests := []struct {
		name    string
		keyID   string
		wantErr string
	}{
		{name: "valid", keyID: "ocid1.tenancy.oc1..t/ocid1.user.oc1..u/" + fingerprint},
		{name: "valid without ocids", keyID: "t/u/" + fingerprint},
		{
			name:    "two parts",
			keyID:   "ocid1.tenancy.oc1..t/" + fingerprint,
			wantErr: "must be <tenancy ocid>/<user ocid>/<key fingerprint>",
		},
		{
			name:    "four parts",
			keyID:   "a/b/c/" + fingerprint,
			wantErr: "must be <tenancy ocid>/<user ocid>/<key fingerprint>",
		},
		{
			name:    "empty user",
			keyID:   "ocid1.tenancy.oc1..t/ /" + fingerprint,
			wantErr: "must be <tenancy ocid>/<user ocid>/<key fingerprint>",
		},
		{
			name:    "short fingerprint",
			keyID:   "t/u/12:34:56",
			wantErr: "must be the 16 colon separated hex pairs of an MD5 fingerprint",
		},
		{
			name:    "fingerprint of the right length that is not hex",
			keyID:   "t/u/" + strings.Repeat("z", 32),
			wantErr: "must be the 16 colon separated hex pairs of an MD5 fingerprint",
		},
		{
			name:    "a sha256 sized fingerprint",
			keyID:   "t/u/" + strings.Repeat("aa:", 31) + "aa",
			wantErr: "must be the 16 colon separated hex pairs of an MD5 fingerprint",
		},
		{
			name:    "an ocid outside the ocid alphabet",
			keyID:   "ocid1.tenancy x/ocid1.u/" + fingerprint,
			wantErr: "must be <tenancy ocid>/<user ocid>/<key fingerprint>",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tenancy, user, got, err := splitOCIKeyID(test.keyID)
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("splitOCIKeyID() error = %v, want %q", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("splitOCIKeyID() error = %v", err)
			}
			if tenancy == "" || user == "" || got != fingerprint {
				t.Fatalf("splitOCIKeyID() = (%q, %q, %q), want a full triple", tenancy, user, got)
			}
		})
	}
}

func TestStart_SignsOCIRequestThroughTheProxy(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	key, pemText, keyID := newTestOCIIdentity(t)

	var captured atomic.Value
	captured.Store(&capturedOCIRequest{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		captured.Store(&capturedOCIRequest{
			method: r.Method,
			path:   r.URL.RequestURI(),
			host:   r.Host,
			header: r.Header.Clone(),
			body:   body,
		})
		_, _ = io.WriteString(w, "{}")
	}))
	defer upstream.Close()

	upstreamURL, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatalf("url.Parse(upstream.URL) error = %v", err)
	}

	proxy, err := startTestProxy(ctx, t, Config{
		SecretValues: map[string]string{"oci_api_key": pemText},
		Rules: []Rule{
			{
				Name:       "oci-api",
				MatchHosts: []string{upstreamURL.Host},
				SignOCI:    &OCISign{Secret: "oci_api_key", KeyID: keyID},
			},
		},
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	req, err := http.NewRequest(
		http.MethodGet,
		upstream.URL+"/20160918/instances?compartmentId=ocid1.compartment.oc1..aaaa",
		nil,
	)
	if err != nil {
		t.Fatalf("http.NewRequest() error = %v", err)
	}
	req.Header.Set("Authorization", "Bearer placeholder-from-the-sandbox")

	resp, err := newProxyClient(t, proxy).Do(req)
	if err != nil {
		t.Fatalf("client.Do() error = %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	seen := captured.Load().(*capturedOCIRequest)
	verifyOCISignature(t, seen.request(t), seen.body, &key.PublicKey, keyID)
	if got := seen.header.Get("Date"); got == "" {
		t.Fatal("expected a Date header on the signed request")
	}
	if strings.Contains(strings.Join(seen.header.Values("Authorization"), " "), "placeholder") {
		t.Fatal("the caller's placeholder Authorization reached the upstream")
	}
	if got := seen.path; got != "/20160918/instances?compartmentId=ocid1.compartment.oc1..aaaa" {
		t.Fatalf("path = %q, want the query preserved", got)
	}
}

func TestStart_SignsOCIRequestBodyRequests(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	key, pemText, keyID := newTestOCIIdentity(t)

	var captured atomic.Value
	captured.Store(&capturedOCIRequest{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		captured.Store(&capturedOCIRequest{
			method: r.Method,
			path:   r.URL.RequestURI(),
			host:   r.Host,
			header: r.Header.Clone(),
			body:   body,
		})
		_, _ = io.WriteString(w, "{}")
	}))
	defer upstream.Close()

	upstreamURL, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatalf("url.Parse(upstream.URL) error = %v", err)
	}

	proxy, err := startTestProxy(ctx, t, Config{
		SecretValues: map[string]string{"oci_api_key": pemText},
		Rules: []Rule{
			{
				Name:       "oci-api",
				MatchHosts: []string{upstreamURL.Host},
				SignOCI:    &OCISign{Secret: "oci_api_key", KeyID: keyID},
			},
		},
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	payload := []byte(`{"availabilityDomain":"EU-FRANKFURT-1-AD-1"}`)
	bodyHash := sha256.Sum256(payload)
	req, err := http.NewRequest(
		http.MethodPost,
		upstream.URL+"/20160918/volumes",
		strings.NewReader(string(payload)),
	)
	if err != nil {
		t.Fatalf("http.NewRequest() error = %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Content-Sha256", base64.StdEncoding.EncodeToString(bodyHash[:]))

	resp, err := newProxyClient(t, proxy).Do(req)
	if err != nil {
		t.Fatalf("client.Do() error = %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	seen := captured.Load().(*capturedOCIRequest)
	verifyOCISignature(t, seen.request(t), seen.body, &key.PublicKey, keyID)
	if got := seen.header.Get("X-Content-Sha256"); got != req.Header.Get("X-Content-Sha256") {
		t.Fatalf(
			"upstream saw x-content-sha256 = %q, want the caller's %q",
			got,
			req.Header.Get("X-Content-Sha256"),
		)
	}
	if !strings.Contains(
		seen.header.Get("Authorization"),
		"content-length content-type x-content-sha256",
	) {
		t.Fatalf(
			"Authorization = %q, want the body headers in the signed list",
			seen.header.Get("Authorization"),
		)
	}
}

func TestStart_FailsClosedWhenABodyRequestCannotBeSigned(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	_, pemText, keyID := newTestOCIIdentity(t)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "{}")
	}))
	defer upstream.Close()

	upstreamURL, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatalf("url.Parse(upstream.URL) error = %v", err)
	}

	proxy, err := startTestProxy(ctx, t, Config{
		SecretValues: map[string]string{"oci_api_key": pemText},
		Rules: []Rule{
			{
				Name:       "oci-api",
				MatchHosts: []string{upstreamURL.Host},
				SignOCI:    &OCISign{Secret: "oci_api_key", KeyID: keyID},
			},
		},
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	// No x-content-sha256: the proxy must refuse rather than sign a body it cannot vouch for.
	req, err := http.NewRequest(
		http.MethodPost,
		upstream.URL+"/20160918/volumes",
		strings.NewReader(`{"a":1}`),
	)
	if err != nil {
		t.Fatalf("http.NewRequest() error = %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := newProxyClient(t, proxy).Do(req)
	if err != nil {
		t.Fatalf("client.Do() error = %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusBadGateway)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("io.ReadAll(resp.Body) error = %v", err)
	}
	if !strings.Contains(string(body), "proxy could not sign the request") {
		t.Fatalf("body = %q, want the signing failure named", body)
	}
	if !strings.Contains(string(body), "x-content-sha256") {
		t.Fatalf("body = %q, want the missing header named", body)
	}
}

func TestStart_LeavesUnmatchedHostUnsigned(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	_, pemText, keyID := newTestOCIIdentity(t)

	var sawAuth atomic.Value
	sawAuth.Store("")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth.Store(r.Header.Get("Authorization"))
		_, _ = io.WriteString(w, "ok")
	}))
	defer upstream.Close()

	proxy, err := startTestProxy(ctx, t, Config{
		SecretValues: map[string]string{"oci_api_key": pemText},
		Rules: []Rule{
			{
				Name:       "oci-api",
				MatchHosts: []string{"iaas.eu-frankfurt-1.oraclecloud.com"},
				SignOCI:    &OCISign{Secret: "oci_api_key", KeyID: keyID},
			},
		},
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	resp, err := newProxyClient(t, proxy).Get(upstream.URL + "/anything")
	if err != nil {
		t.Fatalf("client.Get() error = %v", err)
	}
	defer resp.Body.Close()

	if got := sawAuth.Load().(string); got != "" {
		t.Fatalf("Authorization = %q, want an unsigned request for an unmatched host", got)
	}
}

func TestStart_SignsOnlyAfterEveryMatchingRuleHasRun(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	key, pemText, keyID := newTestOCIIdentity(t)

	var captured atomic.Value
	captured.Store(&capturedOCIRequest{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		captured.Store(&capturedOCIRequest{
			method: r.Method,
			path:   r.URL.RequestURI(),
			host:   r.Host,
			header: r.Header.Clone(),
			body:   body,
		})
		_, _ = io.WriteString(w, "{}")
	}))
	defer upstream.Close()

	upstreamURL, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatalf("url.Parse(upstream.URL) error = %v", err)
	}

	// The second rule rewrites a header the signature covers. The signature must therefore be
	// made after the whole rule loop, or the upstream receives a Date the signature does not
	// match and OCI answers 401.
	const pinnedDate = "Sun, 11 Oct 2026 18:42:00 GMT"
	proxy, err := startTestProxy(ctx, t, Config{
		SecretValues: map[string]string{"oci_api_key": pemText},
		Rules: []Rule{
			{
				Name:       "oci-api",
				MatchHosts: []string{upstreamURL.Host},
				SignOCI:    &OCISign{Secret: "oci_api_key", KeyID: keyID},
			},
			{
				Name:       "pin-date",
				MatchHosts: []string{upstreamURL.Host},
				SetHeader:  map[string]string{"Date": pinnedDate},
			},
		},
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	resp, err := newProxyClient(t, proxy).Get(upstreamURL.String() + "/20160918/instances")
	if err != nil {
		t.Fatalf("client.Get() error = %v", err)
	}
	defer resp.Body.Close()

	seen := captured.Load().(*capturedOCIRequest)
	if got := seen.header.Get("Date"); got != pinnedDate {
		t.Fatalf("upstream Date = %q, want the later rule's %q", got, pinnedDate)
	}
	verifyOCISignature(t, seen.request(t), seen.body, &key.PublicKey, keyID)
}

// verifyOCISignature rebuilds the signing string from the request an upstream received, the
// way an OCI endpoint does, and checks the signature with the public key.
func verifyOCISignature(
	t *testing.T,
	req *http.Request,
	body []byte,
	pub *rsa.PublicKey,
	wantKeyID string,
) {
	t.Helper()

	fields := parseOCIAuthorizationHeader(t, req.Header.Get("Authorization"))
	// Literals on purpose: comparing against the constants under test would let a rename or a
	// changed scheme value keep passing.
	if fields["algorithm"] != "rsa-sha256" {
		t.Fatalf("algorithm = %q, want %q", fields["algorithm"], "rsa-sha256")
	}
	if fields["version"] != "1" {
		t.Fatalf("version = %q, want %q", fields["version"], "1")
	}
	if fields["keyId"] != wantKeyID {
		t.Fatalf("keyId = %q, want %q", fields["keyId"], wantKeyID)
	}

	names := strings.Split(fields["headers"], " ")
	if len(names) == 0 {
		t.Fatal("expected a signed header list")
	}
	lines := make([]string, 0, len(names))
	for _, name := range names {
		switch name {
		case ociRequestTargetName:
			lines = append(lines, name+": "+strings.ToLower(req.Method)+" "+req.URL.RequestURI())
		case "host":
			lines = append(lines, name+": "+req.Host)
		case "content-length":
			length := strings.TrimSpace(req.Header.Get("Content-Length"))
			if length == "" {
				length = strconv.Itoa(len(body))
			}
			lines = append(lines, name+": "+length)
		default:
			value := strings.TrimSpace(req.Header.Get(name))
			if value == "" {
				t.Fatalf("signed header %q is missing from the request", name)
			}
			lines = append(lines, name+": "+value)
		}
	}

	signature, err := base64.StdEncoding.DecodeString(fields["signature"])
	if err != nil {
		t.Fatalf("decode signature: %v", err)
	}
	sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, sum[:], signature); err != nil {
		t.Fatalf("signature does not verify over %q: %v", strings.Join(lines, "\n"), err)
	}
}

var ociAuthorizationFieldRE = regexp.MustCompile(`([A-Za-z]+)="([^"]*)"`)

func parseOCIAuthorizationHeader(t *testing.T, value string) map[string]string {
	t.Helper()

	if !strings.HasPrefix(value, "Signature ") {
		t.Fatalf("Authorization = %q, want a Signature header", value)
	}
	fields := make(map[string]string)
	for _, match := range ociAuthorizationFieldRE.FindAllStringSubmatch(value, -1) {
		fields[match[1]] = match[2]
	}
	for _, required := range []string{"algorithm", "headers", "keyId", "signature", "version"} {
		if fields[required] == "" {
			t.Fatalf("Authorization = %q, missing %q", value, required)
		}
	}
	return fields
}

type capturedOCIRequest struct {
	method string
	path   string
	host   string
	header http.Header
	body   []byte
}

func (c *capturedOCIRequest) request(t *testing.T) *http.Request {
	t.Helper()

	parsed, err := url.Parse("http://" + c.host + c.path)
	if err != nil {
		t.Fatalf("url.Parse(captured) error = %v", err)
	}
	return &http.Request{
		Method: c.method,
		URL:    parsed,
		Host:   c.host,
		Header: c.header,
	}
}

func flipFirstNibble(fingerprint string) string {
	if fingerprint == "" {
		return fingerprint
	}
	flipped := []rune(fingerprint)
	if flipped[0] == 'A' {
		flipped[0] = 'B'
	} else {
		flipped[0] = 'A'
	}
	return string(flipped)
}

func sha256Sum(payload []byte) []byte {
	sum := sha256.Sum256(payload)
	return sum[:]
}
