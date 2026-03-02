package egressproxy

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"time"
)

type generatedCA struct {
	TLSCert      tls.Certificate
	CertPEM      []byte
	CertHostPath string
	tempDir      string
}

func createExportedCA() (*generatedCA, error) {
	now := time.Now().UTC()

	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate CA private key: %w", err)
	}

	serialNumber, err := randSerialNumber()
	if err != nil {
		return nil, fmt.Errorf("generate CA serial number: %w", err)
	}

	template := &x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			CommonName:   "q15 embedded egress proxy CA",
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
		return nil, fmt.Errorf("create CA certificate: %w", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if len(certPEM) == 0 {
		return nil, fmt.Errorf("encode CA certificate PEM: empty output")
	}

	keyDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		return nil, fmt.Errorf("marshal CA private key: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	if len(keyPEM) == 0 {
		return nil, fmt.Errorf("encode CA private key PEM: empty output")
	}

	tlsCert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("build tls.Certificate from CA keypair: %w", err)
	}

	tempDir, err := os.MkdirTemp("", "q15-egressproxy-ca-*")
	if err != nil {
		return nil, fmt.Errorf("create temp dir for CA export: %w", err)
	}
	certHostPath := filepath.Join(tempDir, "ca.crt")
	// The CA certificate is public material and must be readable from the sandbox mount.
	if err := os.WriteFile(certHostPath, certPEM, 0o644); err != nil {
		_ = os.RemoveAll(tempDir)
		return nil, fmt.Errorf("write CA certificate file %q: %w", certHostPath, err)
	}

	return &generatedCA{
		TLSCert:      tlsCert,
		CertPEM:      certPEM,
		CertHostPath: certHostPath,
		tempDir:      tempDir,
	}, nil
}

func (c *generatedCA) cleanup() error {
	if c == nil || c.tempDir == "" {
		return nil
	}
	return os.RemoveAll(c.tempDir)
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
