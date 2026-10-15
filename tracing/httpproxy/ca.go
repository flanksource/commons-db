package httpproxy

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
	"time"
)

// caValidity bounds the generated authority. A capture is minutes long, so an
// hour is generous; the short window means a certificate that escapes the
// capture stops being usable almost at once.
const caValidity = time.Hour

// caSkew backdates the certificate so a process whose clock runs slightly
// behind the host's does not reject a CA that was valid when it was issued.
const caSkew = 5 * time.Minute

// authority is the ephemeral CA a mitm capture signs its per-host
// certificates with. It is never added to a system trust store: its private
// key lives only in this process, and only its certificate is written to disk
// for the processes the capture observes to trust.
type authority struct {
	cert    tls.Certificate
	certPEM []byte
}

// newAuthority generates an ECDSA P-256 authority: goproxy derives each host
// key from the CA key, and a 2048-bit RSA generation per host is slow enough
// to show as latency.
func newAuthority() (*authority, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate ca key: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, fmt.Errorf("generate ca serial: %w", err)
	}
	now := time.Now()
	template := x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{Organization: []string{"Flanksource"}, CommonName: "commons-db http_proxy capture (ephemeral)"},
		NotBefore:             now.Add(-caSkew),
		NotAfter:              now.Add(caValidity),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, key.Public(), key)
	if err != nil {
		return nil, fmt.Errorf("create ca certificate: %w", err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("parse ca certificate: %w", err)
	}
	return &authority{
		// Leaf is set so goproxy's signer does not reparse the DER for every host.
		cert:    tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf},
		certPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
	}, nil
}

// writeCert writes the authority's certificate, and only it, as ca.pem in a
// new temporary directory the capture removes once it ends.
func (a *authority) writeCert() (dir, path string, err error) {
	dir, err = os.MkdirTemp("", "http-proxy-ca-")
	if err != nil {
		return "", "", fmt.Errorf("ca directory: %w", err)
	}
	path = filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(path, a.certPEM, 0o644); err != nil {
		return "", "", errors.Join(fmt.Errorf("write ca %s: %w", path, err), os.RemoveAll(dir))
	}
	return dir, path, nil
}

// upstreamRoots are the authorities the proxy verifies a real server against
// under mitm: the system roots (nil), plus those in the PEM file at path.
func upstreamRoots(path string) (*x509.CertPool, error) {
	if path == "" {
		return nil, nil
	}
	roots, err := x509.SystemCertPool()
	if err != nil {
		return nil, fmt.Errorf("system roots: %w", err)
	}
	pemBytes, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("upstreamCA: %w", err)
	}
	if !roots.AppendCertsFromPEM(pemBytes) {
		return nil, fmt.Errorf("upstreamCA %s holds no PEM certificate", path)
	}
	return roots, nil
}

// trustEnv points a process's TLS stack at the CA at path. No one variable
// works everywhere, hence the list, and nothing guarantees a given binary
// reads any of them. SSL_CERT_FILE and CURL_CA_BUNDLE replace the system
// roots rather than adding to them, as mitm intends: every connection a
// process makes goes through the proxy, so every certificate it sees is this
// CA's.
func trustEnv(path string) [][2]string {
	return [][2]string{
		{"SSL_CERT_FILE", path},       // OpenSSL, Go, Python's ssl
		{"CURL_CA_BUNDLE", path},      // curl built against OpenSSL/GnuTLS
		{"NODE_EXTRA_CA_CERTS", path}, // Node: additive, unlike the others
		{"REQUESTS_CA_BUNDLE", path},  // python-requests
		{"GIT_SSL_CAINFO", path},
		{"AWS_CA_BUNDLE", path},
	}
}
