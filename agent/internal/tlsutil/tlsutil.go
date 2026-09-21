// Package tlsutil generates and loads the agent's own TLS material.
//
// The API certificate is self-signed and carries the VPS's public IP as an
// iPAddress SAN. The Pelican plugin receives the PEM inside the VPS code and
// trusts it as its own certificate authority (Guzzle "verify" => that PEM), so
// the connection is both encrypted and bound to this one certificate, without
// anybody having to own a domain name. The SPKI digest is shipped alongside so
// a plugin that prefers public-key pinning can use that instead.
//
// No Let's Encrypt: the VPS is reached by IP, and ACME cannot issue for a bare
// address.
package tlsutil

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"time"

	"github.com/finnwastakenwastaken/pelican-auto-proxy/agent/internal/state"
)

// Lifetime is how long a generated certificate is valid. Ten years, because
// nothing renews it: an expiry inside the lifetime of a self-hosted VPS would
// break the panel's connection one morning with no warning and no renewal
// process to blame.
const Lifetime = 10 * 365 * 24 * time.Hour

// Material is a freshly generated certificate and its key.
type Material struct {
	CertPEM []byte
	KeyPEM  []byte
}

// Generate makes a self-signed ECDSA P-256 certificate for the given IP
// addresses and DNS names.
//
// IsCA is set so the certificate can serve as its own trust anchor when the
// plugin passes it as a CA bundle.
func Generate(ips []net.IP, dnsNames []string, now time.Time) (Material, error) {
	if len(ips) == 0 && len(dnsNames) == 0 {
		return Material{}, fmt.Errorf("a certificate needs at least one IP address or DNS name")
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return Material{}, fmt.Errorf("generate key: %w", err)
	}
	serialMax := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, serialMax)
	if err != nil {
		return Material{}, fmt.Errorf("generate serial: %w", err)
	}

	cn := "autoproxy-agent"
	if len(dnsNames) > 0 {
		cn = dnsNames[0]
	} else if len(ips) > 0 {
		cn = ips[0].String()
	}

	tmpl := x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: cn, Organization: []string{"Pelican Auto Proxy"}},
		NotBefore:    now.Add(-1 * time.Hour), // tolerate a little clock skew on the panel
		NotAfter:     now.Add(Lifetime),
		// DigitalSignature only. IsCA is set so the plugin can hand this one
		// certificate to OpenSSL as its whole trust store, but the key never
		// signs another certificate, so KeyCertSign would be a capability
		// with no use. Verified against OpenSSL-backed curl: a client given
		// only this PEM as --cacert accepts the connection either way.
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		IPAddresses:           ips,
		DNSNames:              dnsNames,
	}

	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		return Material{}, fmt.Errorf("create certificate: %w", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return Material{}, fmt.Errorf("marshal key: %w", err)
	}
	return Material{
		CertPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		KeyPEM:  pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}),
	}, nil
}

// Write stores the material. The key is 0600: it is the one file on the box
// that lets somebody impersonate this API to the panel.
func (m Material) Write(certPath, keyPath string) error {
	if err := state.WriteFileAtomic(certPath, m.CertPEM, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", certPath, err)
	}
	if err := state.WriteFileAtomic(keyPath, m.KeyPEM, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", keyPath, err)
	}
	return nil
}

// SPKISHA256 returns the base64 SHA-256 of the certificate's
// SubjectPublicKeyInfo, which is the value curl expects after "sha256//" in
// CURLOPT_PINNEDPUBLICKEY.
func SPKISHA256(certPEM []byte) (string, error) {
	block, _ := pem.Decode(certPEM)
	if block == nil || block.Type != "CERTIFICATE" {
		return "", fmt.Errorf("not a PEM certificate")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return "", fmt.Errorf("parse certificate: %w", err)
	}
	sum := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	return base64.StdEncoding.EncodeToString(sum[:]), nil
}

// LoadPEM reads a certificate file from disk.
func LoadPEM(path string) ([]byte, error) { return os.ReadFile(path) }
