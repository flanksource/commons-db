package db

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func listenLoopback(t *testing.T) (net.Listener, uint32) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })
	return listener, uint32(listener.Addr().(*net.TCPAddr).Port) //nolint:gosec // a TCP port fits
}

// A loaded host can take longer than the probe's budget to resolve "localhost" (on hosts
// whose nsswitch asks systemd-resolved before /etc/hosts), and the probe then took the
// running shared server for a free port and started a second one on top of it. The probe
// dials an IP address, which needs no lookup.
func TestPortIsListening_DialsLoopbackIP(t *testing.T) {
	host, port, err := net.SplitHostPort(loopbackAddress(7432))
	require.NoError(t, err)
	assert.Equal(t, "7432", port)
	ip := net.ParseIP(host)
	require.NotNil(t, ip, "%q is a name that must be resolved", host)
	assert.True(t, ip.IsLoopback())

	_, listening := listenLoopback(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	found, err := portIsListening(ctx, listening)
	require.NoError(t, err)
	assert.True(t, found)
}

func TestPortIsListening_RefusedPortIsFree(t *testing.T) {
	listener, port := listenLoopback(t)
	require.NoError(t, listener.Close())

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	listening, err := portIsListening(ctx, port)
	require.NoError(t, err)
	assert.False(t, listening)
}

// A probe that can't finish says nothing about the port, so it must not be taken as
// free: starting a server then fails with a misleading "already listening".
func TestPortIsListening_UnfinishedProbeIsAnError(t *testing.T) {
	_, port := listenLoopback(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := portIsListening(ctx, port)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "can't tell whether port")
}
