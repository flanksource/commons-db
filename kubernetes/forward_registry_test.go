package kubernetes

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/onsi/gomega"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// stubEstablisher hands out increasing local ports and records the stop channel of every tunnel
// it opens, so tests can drive reuse/re-establish/reap without a real cluster.
type stubEstablisher struct {
	mu       sync.Mutex
	calls    int
	nextPort int
	stops    []chan struct{}
}

func (s *stubEstablisher) establish(_ context.Context, _ kubernetes.Interface, _ *rest.Config, _ PortForwardOptions) (int, chan struct{}, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	s.nextPort++
	stop := make(chan struct{}, 1)
	s.stops = append(s.stops, stop)
	return 10000 + s.nextPort, stop, nil
}

func newTestManager(stub *stubEstablisher, idle time.Duration) *ForwardManager {
	return &ForwardManager{
		tunnels:     make(map[string]*tunnel),
		idleTimeout: idle,
		establish:   stub.establish,
	}
}

func opts(name string) PortForwardOptions {
	return PortForwardOptions{Namespace: "prod", Kind: "service", Name: name, RemotePort: 5432}
}

func TestForwardManager_ReusesLiveTunnel(t *testing.T) {
	g := gomega.NewWithT(t)
	stub := &stubEstablisher{}
	m := newTestManager(stub, time.Hour)
	rc := &rest.Config{Host: "https://api.k8s.local"}

	p1, err := m.Forward(context.Background(), nil, rc, opts("db"))
	g.Expect(err).ToNot(gomega.HaveOccurred())

	p2, err := m.Forward(context.Background(), nil, rc, opts("db"))
	g.Expect(err).ToNot(gomega.HaveOccurred())

	g.Expect(p2).To(gomega.Equal(p1), "same target should reuse the tunnel's local port")
	g.Expect(stub.calls).To(gomega.Equal(1), "tunnel should be established only once")
}

func TestForwardManager_DistinctTargetsDistinctTunnels(t *testing.T) {
	g := gomega.NewWithT(t)
	stub := &stubEstablisher{}
	m := newTestManager(stub, time.Hour)
	rc := &rest.Config{Host: "https://api.k8s.local"}

	p1, err := m.Forward(context.Background(), nil, rc, opts("db"))
	g.Expect(err).ToNot(gomega.HaveOccurred())
	p2, err := m.Forward(context.Background(), nil, rc, opts("cache"))
	g.Expect(err).ToNot(gomega.HaveOccurred())

	g.Expect(p2).ToNot(gomega.Equal(p1))
	g.Expect(stub.calls).To(gomega.Equal(2))
}

func TestForwardManager_ReestablishesDeadTunnel(t *testing.T) {
	g := gomega.NewWithT(t)
	stub := &stubEstablisher{}
	m := newTestManager(stub, time.Hour)
	rc := &rest.Config{Host: "https://api.k8s.local"}

	p1, err := m.Forward(context.Background(), nil, rc, opts("db"))
	g.Expect(err).ToNot(gomega.HaveOccurred())

	// Simulate the forward goroutine dying by closing its stop channel.
	close(stub.stops[0])

	p2, err := m.Forward(context.Background(), nil, rc, opts("db"))
	g.Expect(err).ToNot(gomega.HaveOccurred())
	g.Expect(p2).ToNot(gomega.Equal(p1), "a dead tunnel should be replaced with a fresh one")
	g.Expect(stub.calls).To(gomega.Equal(2))
}

func TestForwardManager_ReapsIdleTunnel(t *testing.T) {
	g := gomega.NewWithT(t)
	stub := &stubEstablisher{}
	m := newTestManager(stub, 30*time.Minute)
	rc := &rest.Config{Host: "https://api.k8s.local"}

	_, err := m.Forward(context.Background(), nil, rc, opts("db"))
	g.Expect(err).ToNot(gomega.HaveOccurred())

	// Not yet idle: a reap one minute later keeps it.
	m.reapOnce(time.Now().Add(time.Minute))
	g.Expect(m.tunnels).To(gomega.HaveLen(1))

	// Past the idle timeout: the tunnel is closed and dropped.
	m.reapOnce(time.Now().Add(31 * time.Minute))
	g.Expect(m.tunnels).To(gomega.BeEmpty())
	g.Expect(stub.stops[0]).To(gomega.BeClosed(), "reaped tunnel's stop channel should be closed")
}
