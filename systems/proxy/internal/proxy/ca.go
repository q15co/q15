package proxy

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type generatedCA struct {
	TLSCert      tls.Certificate
	CertPEM      []byte
	CertHostPath string
	KeyHostPath  string
}

func loadOrCreateCA(stateDir string) (*generatedCA, error) {
	stateDir = filepath.Clean(strings.TrimSpace(stateDir))
	if stateDir == "." || stateDir == "" {
		return nil, errors.New("state dir is required")
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return nil, fmt.Errorf("create state dir %q: %w", stateDir, err)
	}

	certPath := filepath.Join(stateDir, "ca.crt")
	keyPath := filepath.Join(stateDir, "ca.key")
	certPEM, keyPEM, err := loadOrCreateCAFiles(certPath, keyPath)
	if err != nil {
		return nil, err
	}

	tlsCert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("build tls.Certificate from CA keypair: %w", err)
	}
	return &generatedCA{
		TLSCert:      tlsCert,
		CertPEM:      certPEM,
		CertHostPath: certPath,
		KeyHostPath:  keyPath,
	}, nil
}

func loadOrCreateCAFiles(certPath string, keyPath string) ([]byte, []byte, error) {
	certPEM, certErr := os.ReadFile(certPath)
	keyPEM, keyErr := os.ReadFile(keyPath)
	switch {
	case certErr == nil && keyErr == nil:
		return certPEM, keyPEM, nil
	case certErr != nil && !errors.Is(certErr, os.ErrNotExist):
		return nil, nil, fmt.Errorf("read CA cert %q: %w", certPath, certErr)
	case keyErr != nil && !errors.Is(keyErr, os.ErrNotExist):
		return nil, nil, fmt.Errorf("read CA key %q: %w", keyPath, keyErr)
	}

	now := time.Now().UTC()

	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generate CA private key: %w", err)
	}

	serialNumber, err := randSerialNumber()
	if err != nil {
		return nil, nil, fmt.Errorf("generate CA serial number: %w", err)
	}

	template := &x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			CommonName:   "q15-proxy CA",
			Organization: []string{"q15"},
		},
		NotBefore:             now.Add(-1 * time.Hour),
		NotAfter:              now.Add(365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            0,
		MaxPathLenZero:        true,
		SubjectKeyId:          []byte{0x71, 0x31, 0x35},
	}

	der, err := x509.CreateCertificate(rand.Reader, template, template, priv.Public(), priv)
	if err != nil {
		return nil, nil, fmt.Errorf("create CA certificate: %w", err)
	}

	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if len(certPEM) == 0 {
		return nil, nil, fmt.Errorf("encode CA certificate PEM: empty output")
	}

	keyDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal CA private key: %w", err)
	}
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	if len(keyPEM) == 0 {
		return nil, nil, fmt.Errorf("encode CA private key PEM: empty output")
	}

	if err := os.WriteFile(certPath, certPEM, 0o644); err != nil {
		return nil, nil, fmt.Errorf("write CA certificate file %q: %w", certPath, err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		return nil, nil, fmt.Errorf("write CA private key file %q: %w", keyPath, err)
	}
	return certPEM, keyPEM, nil
}

func randSerialNumber() (*big.Int, error) {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	n, err := rand.Int(rand.Reader, limit)
	if err != nil {
		return nil, err
	}
	if n.Sign() == 0 {
		return big.NewInt(1), nil
	}
	return n, nil
}
