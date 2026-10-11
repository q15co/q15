package proxy

import (
	"crypto"
	"crypto/md5"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// OCI API hosts take no bearer token. Every request is signed with an RSA private key over a
// signing string built from the request line plus a fixed set of header values, so the only
// thing a caller can usefully place in the Authorization header is a signature that covers
// the request itself.
//
// The body is covered through the x-content-sha256 header that the caller already computed,
// not by hashing the body at signing time. That is what lets the proxy replace the header
// without reading or buffering the body.
//
// The scheme mirrors the OCI CLI's own signer (oci/_vendor/httpsig_cffi in oci-python):
// header names are lowercased, "(request-target)" expands to "<lowercased method> <path and
// query>", lines are joined with newlines, and the signature is RSA PKCS#1 v1.5 over SHA-256,
// base64 encoded, in
//
//	Signature algorithm="rsa-sha256",headers="...",keyId="<tenancy>/<user>/<fingerprint>",signature="...",version="1"
const (
	ociSignatureAlgorithm = "rsa-sha256"
	ociSignatureVersion   = "1"
	ociRequestTargetName  = "(request-target)"
)

// ociSignedHeaderNamesWithoutBody lists the headers signed for every request; PUT, POST and
// PATCH additionally sign the body headers.
var ociSignedHeaderNamesWithoutBody = []string{"date", ociRequestTargetName, "host"}

var ociSignedBodyHeaderNames = []string{"content-length", "content-type", "x-content-sha256"}

// OCISign configures request signing for one OCI API host.
type OCISign struct {
	// Secret names the proxy secret holding the PEM private key.
	Secret string
	// KeyID is the "<tenancy ocid>/<user ocid>/<key fingerprint>" triple the API keys are
	// registered under. The fingerprint half is the MD5 form OCI shows, and is checked against
	// the private key at startup.
	KeyID string
}

type compiledOCISigner struct {
	keyID string
	key   *rsa.PrivateKey
}

func compileOCISigner(
	ruleIndex int,
	sign OCISign,
	secretValues map[string]string,
) (*compiledOCISigner, error) {
	secretAlias := strings.ToLower(strings.TrimSpace(sign.Secret))
	secretValue := strings.TrimSpace(secretValues[secretAlias])
	if secretValue == "" {
		return nil, fmt.Errorf(
			"rule[%d].sign_oci.secret missing value for alias %q",
			ruleIndex,
			secretAlias,
		)
	}

	key, err := parseOCIPrivateKey(secretValue)
	if err != nil {
		return nil, fmt.Errorf("rule[%d].sign_oci.secret %q: %w", ruleIndex, secretAlias, err)
	}

	keyID := strings.TrimSpace(sign.KeyID)
	_, _, fingerprint, err := splitOCIKeyID(keyID)
	if err != nil {
		return nil, fmt.Errorf("rule[%d].sign_oci.key_id: %w", ruleIndex, err)
	}
	if !ociFingerprintMatches(&key.PublicKey, fingerprint) {
		return nil, fmt.Errorf(
			"rule[%d].sign_oci.key_id fingerprint %q does not match secret %q; "+
				"point the rule at the private key whose public half is registered under that fingerprint",
			ruleIndex,
			fingerprint,
			secretAlias,
		)
	}

	return &compiledOCISigner{keyID: keyID, key: key}, nil
}

// apply replaces any Authorization header on req with a signature made by the configured key.
func (s *compiledOCISigner) apply(req *http.Request, now time.Time) error {
	if req == nil {
		return errors.New("request is required")
	}
	if s == nil || s.key == nil {
		return errors.New("signer has no private key")
	}

	names, values, err := ociSignedHeaderValues(req, now)
	if err != nil {
		return err
	}

	signature, err := ociSignature(
		s.key,
		ociSigningString(names, values, req.Method, requestPath(req)),
	)
	if err != nil {
		return err
	}

	// Set replaces every value the caller sent, so a placeholder Authorization never survives.
	req.Header.Set("Authorization", ociAuthorizationHeader(s.keyID, names, signature))
	return nil
}

func ociSignedHeaderValues(req *http.Request, now time.Time) ([]string, map[string]string, error) {
	names := append([]string(nil), ociSignedHeaderNamesWithoutBody...)
	if ociSignsBody(req.Method) {
		names = append(names, ociSignedBodyHeaderNames...)
	}

	values := make(map[string]string, len(names))
	for _, name := range names {
		if name == ociRequestTargetName {
			continue
		}
		value, err := ociSignedHeaderValue(req, name, now)
		if err != nil {
			return nil, nil, err
		}
		values[name] = value
	}
	return names, values, nil
}

func ociSignedHeaderValue(req *http.Request, name string, now time.Time) (string, error) {
	switch name {
	case "date":
		if value := strings.TrimSpace(req.Header.Get("Date")); value != "" {
			return value, nil
		}
		value := now.UTC().Format(http.TimeFormat)
		req.Header.Set("Date", value)
		return value, nil
	case "host":
		host := strings.TrimSpace(req.Host)
		if host == "" && req.URL != nil {
			host = strings.TrimSpace(req.URL.Host)
		}
		if host == "" {
			return "", errors.New("request has no host header to sign")
		}
		return host, nil
	case "content-length":
		if req.ContentLength >= 0 {
			return strconv.FormatInt(req.ContentLength, 10), nil
		}
		return "", errors.New("request has no content length to sign")
	case "content-type":
		value := strings.TrimSpace(req.Header.Get("Content-Type"))
		if value == "" {
			return "", errors.New("request has no content type to sign")
		}
		return value, nil
	case "x-content-sha256":
		value := strings.TrimSpace(req.Header.Get("X-Content-Sha256"))
		if value == "" {
			return "", errors.New(
				"request has no x-content-sha256 header to sign; " +
					"the caller must send the body hash because the proxy never reads the body",
			)
		}
		return value, nil
	default:
		return "", fmt.Errorf("unsupported signed header %q", name)
	}
}

// ociSignsBody reports whether the method carries a body whose headers must be signed.
func ociSignsBody(method string) bool {
	switch strings.ToUpper(strings.TrimSpace(method)) {
	case http.MethodPut, http.MethodPost, http.MethodPatch:
		return true
	default:
		return false
	}
}

func ociSigningString(names []string, values map[string]string, method string, path string) string {
	lines := make([]string, 0, len(names))
	for _, name := range names {
		if name == ociRequestTargetName {
			lines = append(lines, name+": "+strings.ToLower(method)+" "+path)
			continue
		}
		lines = append(lines, name+": "+values[name])
	}
	return strings.Join(lines, "\n")
}

func ociAuthorizationHeader(keyID string, names []string, signature string) string {
	return fmt.Sprintf(
		`Signature algorithm="%s",headers="%s",keyId="%s",signature="%s",version="%s"`,
		ociSignatureAlgorithm,
		strings.Join(names, " "),
		keyID,
		signature,
		ociSignatureVersion,
	)
}

func ociSignature(key *rsa.PrivateKey, message string) (string, error) {
	if key == nil {
		return "", errors.New("private key is required")
	}
	sum := sha256.Sum256([]byte(message))
	signed, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		return "", fmt.Errorf("sign request: %w", err)
	}
	return base64.StdEncoding.EncodeToString(signed), nil
}

func requestPath(req *http.Request) string {
	if req == nil || req.URL == nil {
		return "/"
	}
	// URL.RequestURI never returns an empty string, so this is the whole fallback.
	return req.URL.RequestURI()
}

func parseOCIPrivateKey(pemText string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(pemText))
	if block == nil {
		return nil, errors.New("secret is not a PEM encoded private key")
	}
	if block.Type == "ENCRYPTED PRIVATE KEY" {
		return nil, errors.New("passphrase protected keys are not supported")
	}

	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}

	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse private key: %w", err)
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("private key is %T, want RSA", parsed)
	}
	return key, nil
}

// ociKeyIDPart is the alphabet an OCID is built from. The key id is interpolated into the
// Authorization header, so nothing outside it is allowed through.
var ociKeyIDPart = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// splitOCIKeyID splits "<tenancy>/<user>/<fingerprint>" and validates its shape.
func splitOCIKeyID(keyID string) (string, string, string, error) {
	keyID = strings.TrimSpace(keyID)
	parts := strings.Split(keyID, "/")
	if len(parts) != 3 {
		return "", "", "", fmt.Errorf(
			"key id %q must be <tenancy ocid>/<user ocid>/<key fingerprint>",
			keyID,
		)
	}
	tenancy := strings.TrimSpace(parts[0])
	user := strings.TrimSpace(parts[1])
	fingerprint := strings.TrimSpace(parts[2])
	if !ociKeyIDPart.MatchString(tenancy) || !ociKeyIDPart.MatchString(user) ||
		fingerprint == "" {
		return "", "", "", fmt.Errorf(
			"key id %q must be <tenancy ocid>/<user ocid>/<key fingerprint>, "+
				"with ocids of letters, digits, dots, dashes and underscores",
			keyID,
		)
	}
	if !ociFingerprintShape(fingerprint) {
		return "", "", "", fmt.Errorf(
			"key id fingerprint %q must be the 16 colon separated hex pairs of an MD5 fingerprint",
			fingerprint,
		)
	}
	return tenancy, user, fingerprint, nil
}

func ociFingerprintShape(fingerprint string) bool {
	normalized := normalizeOCIFingerprint(fingerprint)
	if len(normalized) != 32 {
		return false
	}
	for _, r := range normalized {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'f':
		default:
			return false
		}
	}
	return true
}

// ociFingerprintMatches compares the configured fingerprint with the MD5 digest of the DER
// encoded public key: the form OCI computes and shows, and the one
// `openssl rsa -pubout -outform DER | openssl md5 -c` prints. Accepting a SHA-256 digest as
// well would let a key id through that boots cleanly but that OCI cannot resolve.
func ociFingerprintMatches(pub *rsa.PublicKey, fingerprint string) bool {
	if pub == nil {
		return false
	}
	want := normalizeOCIFingerprint(fingerprint)
	if want == "" {
		return false
	}
	return normalizeOCIFingerprint(ociFingerprintMD5(pub)) == want
}

// An RSA public key always marshals, so the fingerprint helper has no error to report.
func ociFingerprintMD5(pub *rsa.PublicKey) string {
	sum := md5.Sum(ociPublicKeyDER(pub))
	return colonSeparatedHex(sum[:])
}

func ociPublicKeyDER(pub *rsa.PublicKey) []byte {
	// MarshalPKIXPublicKey cannot fail for an RSA key. If it ever did, the fingerprint would
	// not match the configured one and the rule would refuse to start.
	der, _ := x509.MarshalPKIXPublicKey(pub)
	return der
}

func colonSeparatedHex(sum []byte) string {
	parts := make([]string, 0, len(sum))
	for _, b := range sum {
		parts = append(parts, fmt.Sprintf("%02X", b))
	}
	return strings.Join(parts, ":")
}

func normalizeOCIFingerprint(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, ":", "")
	value = strings.ReplaceAll(value, " ", "")
	return value
}
