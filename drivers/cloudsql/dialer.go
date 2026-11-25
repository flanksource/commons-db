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

// Ported from cloud.google.com/go/cloudsqlconn v1.22.1 (dialer.go,
// connectInstanceIP, https://github.com/GoogleCloudPlatform/cloud-sql-go-connector).
// Modifications: only the IP-based path with IAM database authentication is
// kept; metrics, tracing, MDX, SQLData, DNS-name instances and the fallback
// connection are dropped.

package cloudsql

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"fmt"
	"net"
	"sync"
	"time"

	"golang.org/x/oauth2"

	"github.com/flanksource/commons-db/cloud/gcp"
)

const (
	// serverProxyPort is the port of the Cloud SQL server-side proxy.
	serverProxyPort = "3307"

	defaultTCPKeepAlive = 30 * time.Second
	rsaKeyBits          = 2048
)

// dialerConfig is what a dialer needs from its environment.
type dialerConfig struct {
	// Admin is the Cloud SQL Admin API client.
	Admin *gcp.SQLAdminClient
	// LoginTokens yields tokens scoped for IAM database login (sqlservice.login).
	LoginTokens oauth2.TokenSource
	// DialContext opens the TCP connection to the instance's proxy port; a
	// net.Dialer's DialContext when nil.
	DialContext func(ctx context.Context, network, addr string) (net.Conn, error)
}

// dialer opens IAM-authenticated TLS connections to Cloud SQL instances. It
// shares one RSA key across instances, as generating it is expensive.
type dialer struct {
	cfg dialerConfig

	keyOnce sync.Once
	key     *rsa.PrivateKey
	keyErr  error

	mu    sync.Mutex
	cache map[connName]*instanceCache
}

func newDialer(cfg dialerConfig) *dialer {
	if cfg.DialContext == nil {
		cfg.DialContext = (&net.Dialer{}).DialContext
	}
	return &dialer{cfg: cfg, cache: map[connName]*instanceCache{}}
}

// dialOptions selects the instance and which of its IP addresses to use.
type dialOptions struct {
	InstanceConnectionName string
	PrivateIP              bool
}

func (d *dialer) rsaKey() (*rsa.PrivateKey, error) {
	d.keyOnce.Do(func() {
		d.key, d.keyErr = rsa.GenerateKey(rand.Reader, rsaKeyBits)
	})
	if d.keyErr != nil {
		return nil, fmt.Errorf("cloudsql: generating RSA key: %w", d.keyErr)
	}
	return d.key, nil
}

func (d *dialer) instance(name connName) (*instanceCache, error) {
	key, err := d.rsaKey()
	if err != nil {
		return nil, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if cached, ok := d.cache[name]; ok {
		return cached, nil
	}
	cached := &instanceCache{name: name, admin: d.cfg.Admin, key: key, tokens: d.cfg.LoginTokens}
	d.cache[name] = cached
	return cached, nil
}

// Dial opens a TLS connection to the instance's server-side proxy, presenting
// the ephemeral client certificate that carries the IAM identity.
func (d *dialer) Dial(ctx context.Context, opts dialOptions) (net.Conn, error) {
	name, err := parseConnName(opts.InstanceConnectionName)
	if err != nil {
		return nil, err
	}
	cache, err := d.instance(name)
	if err != nil {
		return nil, err
	}
	info, err := cache.connectionInfo(ctx)
	if err != nil {
		return nil, err
	}
	ipType := ipTypePublic
	if opts.PrivateIP {
		ipType = ipTypePrivate
	}
	addr, err := info.addr(ipType)
	if err != nil {
		return nil, err
	}
	return d.connect(ctx, cache, info, net.JoinHostPort(addr, serverProxyPort))
}

func (d *dialer) connect(ctx context.Context, cache *instanceCache, info connectionInfo, addr string) (net.Conn, error) {
	conn, err := d.cfg.DialContext(ctx, "tcp", addr)
	if err != nil {
		// The metadata may be stale (e.g. the instance moved); refetch next time.
		cache.invalidate()
		return nil, fmt.Errorf("cloudsql: %s: failed to dial %s: %w", info.name, addr, err)
	}
	if tcp, ok := conn.(*net.TCPConn); ok {
		if err := configureKeepAlive(tcp); err != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("cloudsql: %s: %w", info.name, err)
		}
	}
	tlsConn := tls.Client(conn, info.tlsConfig())
	if err := tlsConn.HandshakeContext(ctx); err != nil {
		// A failed handshake usually means a stale certificate or CA: refetch.
		cache.invalidate()
		_ = tlsConn.Close()
		return nil, fmt.Errorf("cloudsql: %s: handshake failed: %w", info.name, err)
	}
	return tlsConn, nil
}

func configureKeepAlive(conn *net.TCPConn) error {
	if err := conn.SetKeepAlive(true); err != nil {
		return fmt.Errorf("failed to set keep-alive: %w", err)
	}
	if err := conn.SetKeepAlivePeriod(defaultTCPKeepAlive); err != nil {
		return fmt.Errorf("failed to set keep-alive period: %w", err)
	}
	return nil
}
