package tlsutil

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestGenerateHasTheSANsAndUsagesThePluginNeeds(t *testing.T) {
	now := time.Unix(1700000000, 0)
	m, err := Generate([]net.IP{net.ParseIP("203.0.113.10")}, []string{"proxy.example.com"}, now)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(m.CertPEM)
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if len(cert.IPAddresses) != 1 || !cert.IPAddresses[0].Equal(net.ParseIP("203.0.113.10")) {
		// Without the iPAddress SAN, OpenSSL cannot match https://<ip>/ and
		// the plugin's connection fails with an unhelpful error.
		t.Fatalf("iPAddress SAN missing: %+v", cert.IPAddresses)
	}
	if len(cert.DNSNames) != 1 || cert.DNSNames[0] != "proxy.example.com" {
		t.Fatalf("DNS SAN missing: %+v", cert.DNSNames)
	}
	if !cert.IsCA || !cert.BasicConstraintsValid {
		// The plugin trusts this certificate as its own CA bundle.
		t.Fatalf("IsCA must be set: %+v", cert)
	}
	if cert.KeyUsage&x509.KeyUsageDigitalSignature == 0 {
		t.Fatal("DigitalSignature missing")
	}
	// This key never signs another certificate; granting CertSign would be a
	// capability with no use. Checked against OpenSSL-backed curl: trust as a
	// CA bundle works without it.
	if cert.KeyUsage&x509.KeyUsageCertSign != 0 {
		t.Fatal("KeyCertSign is not needed and should not be granted")
	}
	if len(cert.ExtKeyUsage) != 1 || cert.ExtKeyUsage[0] != x509.ExtKeyUsageServerAuth {
		t.Fatalf("ServerAuth missing: %+v", cert.ExtKeyUsage)
	}
	if cert.PublicKeyAlgorithm != x509.ECDSA {
		t.Fatalf("expected ECDSA, got %v", cert.PublicKeyAlgorithm)
	}
	// Ten years: nothing renews this, and an expiry mid-life would break the
	// panel one morning with no renewal process to blame.
	if years := cert.NotAfter.Sub(now).Hours() / 24 / 365; years < 9.9 {
		t.Fatalf("certificate is only valid for %.1f years", years)
	}
	if !cert.NotBefore.Before(now) {
		t.Fatal("NotBefore should allow a little clock skew on the panel")
	}
}

func TestGenerateNeedsAName(t *testing.T) {
	if _, err := Generate(nil, nil, time.Now()); err == nil {
		t.Fatal("a certificate with no SAN would match nothing; expected an error")
	}
}

func TestSPKISHA256IsStableAndKeyDependent(t *testing.T) {
	a, _ := Generate([]net.IP{net.ParseIP("203.0.113.10")}, nil, time.Now())
	b, _ := Generate([]net.IP{net.ParseIP("203.0.113.10")}, nil, time.Now())
	pinA, err := SPKISHA256(a.CertPEM)
	if err != nil {
		t.Fatal(err)
	}
	again, _ := SPKISHA256(a.CertPEM)
	if pinA != again {
		t.Fatal("the pin must be stable for one certificate")
	}
	pinB, _ := SPKISHA256(b.CertPEM)
	if pinA == pinB {
		t.Fatal("two certificates with different keys must not share a pin")
	}
	if _, err := SPKISHA256([]byte("not a pem")); err == nil {
		t.Fatal("expected an error for rubbish input")
	}
}

func TestWriteUsesRestrictivePermissionsOnTheKey(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "agent.crt")
	keyPath := filepath.Join(dir, "agent.key")
	m, _ := Generate([]net.IP{net.ParseIP("203.0.113.10")}, nil, time.Now())
	if err := m.Write(certPath, keyPath); err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(keyPath)
	if fi.Mode().Perm() != 0o600 {
		// This key is what lets somebody impersonate the API to the panel.
		t.Fatalf("key mode must be 0600, got %v", fi.Mode().Perm())
	}
}

// The whole point of the design: a client that is given only this certificate
// as its trust store must accept a TLS 1.3 connection to https://<ip>/ and
// reject the same server when it is not in the store.
func TestCertificateWorksAsItsOwnTrustAnchor(t *testing.T) {
	m, err := Generate([]net.IP{net.ParseIP("127.0.0.1")}, nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	pair, err := tls.X509KeyPair(m.CertPEM, m.KeyPEM)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("ok"))
	}))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS13}
	srv.StartTLS()
	defer srv.Close()

	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(m.CertPEM) {
		t.Fatal("could not add the certificate to a pool")
	}
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS13}}}
	url := strings.Replace(srv.URL, "https://127.0.0.1", "https://127.0.0.1", 1)
	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("a client trusting this PEM must connect: %v", err)
	}
	resp.Body.Close()
	if resp.TLS.Version != tls.VersionTLS13 {
		t.Fatalf("expected TLS 1.3, got %x", resp.TLS.Version)
	}

	// An empty trust store must fail: if this passed, the pinning would be
	// doing nothing at all.
	strict := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: x509.NewCertPool(), MinVersion: tls.VersionTLS13}}}
	if _, err := strict.Get(url); err == nil {
		t.Fatal("an untrusting client must refuse this certificate")
	}
}
