// Copyright 2020 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     https://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Ported from cloud.google.com/go/cloudsqlconn v1.22.1 (internal/cloudsql/refresh.go,
// instance.go and lazy.go, https://github.com/GoogleCloudPlatform/cloud-sql-go-connector).
// Modifications: the generated sqladmin client is replaced by cloud/gcp's
// SQLAdminClient, the refresh-ahead cache is replaced by the connector's lazy
// cache (refresh when the certificate is within refreshBuffer of expiry), MDX,
// SQLData and PSC support are dropped, and errors are plain fmt errors.

package cloudsql

import (
	"context"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"

	"github.com/flanksource/commons-db/cloud/gcp"
)

const (
	ipTypePublic  = "PUBLIC"
	ipTypePrivate = "PRIVATE"

	// refreshBuffer is how long before the client certificate expires a new one
	// is requested, leaving time to establish connections with the old one.
	refreshBuffer = 4 * time.Minute
)

// connectionInfo is everything needed to open TLS connections to an instance.
type connectionInfo struct {
	name          connName
	clientCert    tls.Certificate
	serverCAs     []*x509.Certificate
	serverName    string
	perInstanceCA bool
	version       string
	addrs         map[string]string
	expiration    time.Time
}

// addr returns the instance address for the IP type, or an error naming the
// missing type.
func (c connectionInfo) addr(ipType string) (string, error) {
	addr, ok := c.addrs[ipType]
	if !ok {
		return "", fmt.Errorf("cloudsql: %s: instance does not have a %s IP address", c.name, ipType)
	}
	return addr, nil
}

// tlsConfig builds the client TLS configuration: TLS 1.3, the ephemeral client
// certificate, and the connector's custom server verification.
func (c connectionInfo) tlsConfig() *tls.Config {
	pool := x509.NewCertPool()
	for _, ca := range c.serverCAs {
		pool.AddCert(ca)
	}
	config := &tls.Config{
		ServerName:   c.serverName,
		Certificates: []tls.Certificate{c.clientCert},
		RootCAs:      pool,
		MinVersion:   tls.VersionTLS13,
		// Per-instance CAs can issue CN-only certificates. Their chain and
		// instance identity are verified by the callback below.
		InsecureSkipVerify: c.perInstanceCA,
	}
	if c.perInstanceCA {
		config.VerifyPeerCertificate = verifyPeerCertificateFunc(c.serverName, c.name, pool)
	}
	return config
}

// instanceCache holds the connection info for one instance and refreshes it
// lazily when the certificate nears expiry.
type instanceCache struct {
	name   connName
	admin  *gcp.SQLAdminClient
	key    *rsa.PrivateKey
	tokens oauth2.TokenSource // sqlservice.login scope; embedded in the cert for IAM database auth

	mu           sync.Mutex
	cached       connectionInfo
	needsRefresh bool
}

// connectionInfo returns the cached info, refreshing it when it is missing,
// invalidated or within refreshBuffer of expiry.
func (c *instanceCache) connectionInfo(ctx context.Context) (connectionInfo, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.needsRefresh && time.Now().Before(c.cached.expiration.Add(-refreshBuffer)) {
		return c.cached, nil
	}
	info, err := c.fetch(ctx)
	if err != nil {
		return connectionInfo{}, err
	}
	c.cached, c.needsRefresh = info, false
	return info, nil
}

// invalidate makes the next connectionInfo call fetch fresh info.
func (c *instanceCache) invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.needsRefresh = true
}

// fetch retrieves the instance metadata and a fresh ephemeral certificate.
func (c *instanceCache) fetch(ctx context.Context) (connectionInfo, error) {
	ref := gcp.SQLInstanceRef{Project: c.name.project, Instance: c.name.name}
	settings, err := c.admin.GetConnectSettings(ctx, ref)
	if err != nil {
		return connectionInfo{}, fmt.Errorf("cloudsql: %s: failed to get instance metadata: %w", c.name, err)
	}
	info, err := c.infoFromSettings(settings)
	if err != nil {
		return connectionInfo{}, err
	}
	if err := supportsIAMAuthN(info.version); err != nil {
		return connectionInfo{}, fmt.Errorf("cloudsql: %s: %w", c.name, err)
	}
	cert, err := c.ephemeralCert(ctx, ref)
	if err != nil {
		return connectionInfo{}, err
	}
	info.clientCert, info.expiration = cert, cert.Leaf.NotAfter
	return info, nil
}

// infoFromSettings validates connect settings and extracts what the dialer needs.
func (c *instanceCache) infoFromSettings(s *gcp.ConnectSettings) (connectionInfo, error) {
	if s.Region != c.name.region {
		return connectionInfo{}, fmt.Errorf("cloudsql: %s: provided region was mismatched - got %s, want %s", c.name, c.name.region, s.Region)
	}
	if s.BackendType != "SECOND_GEN" {
		return connectionInfo{}, fmt.Errorf("cloudsql: %s: unsupported instance - only Second Generation instances are supported", c.name)
	}
	addrs := map[string]string{}
	for _, ip := range s.IPAddresses {
		switch ip.Type {
		case "PRIMARY":
			addrs[ipTypePublic] = ip.IPAddress
		case "PRIVATE":
			addrs[ipTypePrivate] = ip.IPAddress
		}
	}
	if len(addrs) == 0 {
		return connectionInfo{}, fmt.Errorf("cloudsql: %s: cannot connect to instance - it has no supported IP addresses", c.name)
	}
	if s.ServerCaCert == nil {
		return connectionInfo{}, fmt.Errorf("cloudsql: %s: instance does not have a server CA certificate", c.name)
	}
	cas, err := parseCertificates(s.ServerCaCert.Cert)
	if err != nil {
		return connectionInfo{}, fmt.Errorf("cloudsql: %s: server CA certificate: %w", c.name, err)
	}
	serverName := s.DNSName
	if len(s.DNSNames) > 0 && s.DNSNames[0].Name != "" {
		serverName = s.DNSNames[0].Name
	}
	info := connectionInfo{name: c.name, serverCAs: cas, serverName: serverName, version: s.DatabaseVersion, addrs: addrs}
	switch s.ServerCaMode {
	case "", "CA_MODE_UNSPECIFIED", "GOOGLE_MANAGED_INTERNAL_CA":
		info.perInstanceCA = true
	case "GOOGLE_MANAGED_CAS_CA", "CUSTOMER_MANAGED_CAS_CA":
		if serverName == "" {
			return connectionInfo{}, fmt.Errorf("cloudsql: %s: %s requires a server DNS name", c.name, s.ServerCaMode)
		}
	default:
		return connectionInfo{}, fmt.Errorf("cloudsql: %s: unknown server CA mode %q", c.name, s.ServerCaMode)
	}
	return info, nil
}

func parseCertificates(pemData string) ([]*x509.Certificate, error) {
	var certs []*x509.Certificate
	for block, rest := pem.Decode([]byte(pemData)); block != nil; block, rest = pem.Decode(rest) {
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("failed to parse as X.509 certificate: %w", err)
		}
		certs = append(certs, cert)
	}
	if len(certs) == 0 {
		return nil, fmt.Errorf("failed to decode valid PEM cert")
	}
	return certs, nil
}

// ephemeralCert requests a client certificate for the dialer's key. The IAM
// login token rides in the request so the database authenticates the IAM user
// from the certificate, and the certificate is treated as expiring with the token.
func (c *instanceCache) ephemeralCert(ctx context.Context, ref gcp.SQLInstanceRef) (tls.Certificate, error) {
	publicKey, err := x509.MarshalPKIXPublicKey(&c.key.PublicKey)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("cloudsql: %s: encoding public key: %w", c.name, err)
	}
	token, err := c.tokens.Token()
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("cloudsql: %s: failed to retrieve Oauth2 token: %w", c.name, err)
	}
	issued, err := c.admin.GenerateEphemeralCert(ctx, gcp.EphemeralCertRequest{
		SQLInstanceRef: ref,
		PublicKeyPEM:   string(pem.EncodeToMemory(&pem.Block{Bytes: publicKey, Type: "RSA PUBLIC KEY"})),
		AccessToken:    token.AccessToken,
	})
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("cloudsql: %s: create ephemeral cert failed: %w", c.name, err)
	}
	block, _ := pem.Decode([]byte(issued.CertPEM))
	if block == nil {
		return tls.Certificate{}, fmt.Errorf("cloudsql: %s: failed to decode valid PEM cert", c.name)
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("cloudsql: %s: failed to parse as X.509 certificate: %w", c.name, err)
	}
	if !token.Expiry.IsZero() && token.Expiry.Before(leaf.NotAfter) {
		leaf.NotAfter = token.Expiry
	}
	return tls.Certificate{Certificate: [][]byte{leaf.Raw}, PrivateKey: c.key, Leaf: leaf}, nil
}

// supportsIAMAuthN checks that the engine supports automatic IAM database authentication.
func supportsIAMAuthN(version string) error {
	if strings.HasPrefix(version, "POSTGRES") || strings.HasPrefix(version, "MYSQL") {
		return nil
	}
	return fmt.Errorf("%s does not support Auto IAM DB Authentication", version)
}
