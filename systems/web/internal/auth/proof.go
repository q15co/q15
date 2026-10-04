package auth

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	workerCookie   = "__Host-q15w"
	proofHeader    = "Q15-Proof"
	proofProtocol  = "q15-proof."
	proofLifetime  = time.Minute
	proofClockSkew = 5 * time.Second
	maxProofs      = 256
)

func parseSessionPublicKey(encoded string) ([]byte, error) {
	data, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(data) != 91 {
		return nil, errors.New("invalid session public key")
	}
	parsed, err := x509.ParsePKIXPublicKey(data)
	key, ok := parsed.(*ecdsa.PublicKey)
	if err != nil || !ok || key.Curve != elliptic.P256() {
		return nil, errors.New("invalid session public key")
	}
	return data, nil
}

func requestProof(r *http.Request) string {
	headers := r.Header.Values(proofHeader)
	protocols := r.Header.Values("Sec-WebSocket-Protocol")
	if r.URL.Path == "/ws" && strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		if len(headers) != 0 || len(protocols) != 1 {
			return ""
		}
		offered := strings.Split(protocols[0], ",")
		if len(offered) != 2 || strings.TrimSpace(offered[0]) != "q15-auth" ||
			!strings.HasPrefix(strings.TrimSpace(offered[1]), proofProtocol) {
			return ""
		}
		return strings.TrimPrefix(strings.TrimSpace(offered[1]), proofProtocol)
	}
	if len(headers) == 0 && len(protocols) == 0 && r.Method == http.MethodGet &&
		r.URL.Path == "/sw.js" {
		cookies := r.CookiesNamed(workerCookie)
		if len(cookies) == 1 {
			return cookies[0].Value
		}
	}
	if len(headers) != 1 || len(protocols) != 0 {
		return ""
	}
	return headers[0]
}

func proofMessage(origin, binding string, r *http.Request, stamp, nonce string) string {
	transport := "http"
	if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		transport = "ws"
	}
	return strings.Join(
		[]string{
			"q15-proof-v1",
			origin,
			binding,
			transport,
			r.Method,
			r.URL.RequestURI(),
			stamp,
			nonce,
		},
		"\n",
	)
}

// verifyProof consumes the nonce durably before dispatch. Keeping reuse state in
// the session record prevents a server restart from making captured proofs valid.
// The caller holds mu across verification, consumption and session lookup.
func (a *Authenticator) verifyProof(key string, r *http.Request) bool {
	s := a.state.Sessions[key]
	proof := requestProof(r)
	if len(proof) > 160 {
		return false
	}
	parts := strings.Split(proof, ".")
	if len(parts) != 3 {
		return false
	}
	stamp, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || strconv.FormatInt(stamp, 10) != parts[0] {
		return false
	}
	issued := time.Unix(stamp, 0)
	now := a.now()
	if !now.Before(issued.Add(proofLifetime)) || issued.After(now.Add(proofClockSkew)) {
		return false
	}
	nonce, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || len(nonce) != 16 || credentialID(nonce) != parts[1] {
		return false
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(signature) != 64 || credentialID(signature) != parts[2] {
		return false
	}
	parsed, err := x509.ParsePKIXPublicKey(s.PublicKey)
	publicKey, ok := parsed.(*ecdsa.PublicKey)
	if err != nil || !ok {
		return false
	}
	hash := sha256.Sum256([]byte(proofMessage(a.origin, s.Binding, r, parts[0], parts[1])))
	if !ecdsa.Verify(
		publicKey,
		hash[:],
		new(big.Int).SetBytes(signature[:32]),
		new(big.Int).SetBytes(signature[32:]),
	) {
		return false
	}
	used := make(map[string]time.Time, len(s.Used)+1)
	for nonce, expires := range s.Used {
		if now.Before(expires) {
			used[nonce] = expires
		}
	}
	if _, replayed := used[parts[1]]; replayed || len(used) >= maxProofs {
		return false
	}
	used[parts[1]] = issued.Add(proofLifetime)
	s.Used = used
	next := a.nextState()
	next.Sessions[key] = s
	return a.commit(next) == nil
}

func (a *Authenticator) workerBootstrap(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Proof string `json:"proof"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 512)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF ||
		len(input.Proof) > 160 {
		a.unauthorized(w, r)
		return
	}
	// Browser worker registration cannot set headers. Carry its independently
	// signed, single-use GET proof in an HttpOnly cookie for this one fetch.
	cookie(w, workerCookie, input.Proof, proofLifetime)
	w.WriteHeader(http.StatusNoContent)
}
