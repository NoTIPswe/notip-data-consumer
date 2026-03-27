//go:build integration

package integration

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type testCerts struct {
	CACert     string
	ServerCert string
	ServerKey  string
	ClientCert string // CN=data-consumer — must match authorization.users in nats.conf
	ClientKey  string
}

func generateCerts(dir string, extraIPs []net.IP) (*testCerts, error) {
	// CA
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate CA key: %w", err)
	}
	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "notip-test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		return nil, fmt.Errorf("create CA cert: %w", err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		return nil, fmt.Errorf("parse CA cert: %w", err)
	}

	// Server cert — SAN covers both "localhost" and 127.0.0.1 so TLS verification
	// passes regardless of how testcontainers resolves the container address.
	serverKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate server key: %w", err)
	}
	serverTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "nats-server"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
		IPAddresses:  append([]net.IP{net.ParseIP("127.0.0.1"), net.IPv6loopback}, extraIPs...),
	}
	serverDER, err := x509.CreateCertificate(rand.Reader, serverTemplate, caCert, &serverKey.PublicKey, caKey)
	if err != nil {
		return nil, fmt.Errorf("create server cert: %w", err)
	}

	// Client cert — CN must exactly match the "user" entry in the NATS authorization
	// block so that verify_and_map grants data-consumer full permissions.
	clientKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate client key: %w", err)
	}
	clientTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(3),
		Subject:      pkix.Name{CommonName: "data-consumer"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	clientDER, err := x509.CreateCertificate(rand.Reader, clientTemplate, caCert, &clientKey.PublicKey, caKey)
	if err != nil {
		return nil, fmt.Errorf("create client cert: %w", err)
	}

	c := &testCerts{
		CACert:     filepath.Join(dir, "ca.pem"),
		ServerCert: filepath.Join(dir, "server-cert.pem"),
		ServerKey:  filepath.Join(dir, "server-key.pem"),
		ClientCert: filepath.Join(dir, "client-cert.pem"),
		ClientKey:  filepath.Join(dir, "client-key.pem"),
	}
	if err := writeCertPEM(c.CACert, caDER); err != nil {
		return nil, err
	}
	if err := writeCertPEM(c.ServerCert, serverDER); err != nil {
		return nil, err
	}
	if err := writeECKeyPEM(c.ServerKey, serverKey); err != nil {
		return nil, err
	}
	if err := writeCertPEM(c.ClientCert, clientDER); err != nil {
		return nil, err
	}
	if err := writeECKeyPEM(c.ClientKey, clientKey); err != nil {
		return nil, err
	}
	return c, nil
}

func writeCertPEM(path string, der []byte) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return pem.Encode(f, &pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func writeECKeyPEM(path string, key *ecdsa.PrivateKey) error {
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return pem.Encode(f, &pem.Block{Type: "EC PRIVATE KEY", Bytes: der})
}

// TestNATSMTLSConnectsWithValidClientCert verifies that a client presenting the
// data-consumer certificate connects successfully (verify_and_map resolves it to
// the authorized user entry with full permissions).
func TestNATSMTLSConnectsWithValidClientCert(t *testing.T) {
	nc, err := nats.Connect(
		sharedNATSURL,
		nats.RootCAs(sharedNATSCerts.CACert),
		nats.ClientCert(sharedNATSCerts.ClientCert, sharedNATSCerts.ClientKey),
	)
	require.NoError(t, err)
	defer nc.Close()
	assert.True(t, nc.IsConnected())
}

// TestNATSMTLSRejectsClientWithoutCert verifies that verify: true on the NATS
// server rejects a client that presents no certificate.
func TestNATSMTLSRejectsClientWithoutCert(t *testing.T) {
	_, err := nats.Connect(
		sharedNATSURL,
		nats.RootCAs(sharedNATSCerts.CACert),
		// No ClientCert — TLS handshake must fail.
	)
	require.Error(t, err, "expected TLS rejection with no client cert")
}
