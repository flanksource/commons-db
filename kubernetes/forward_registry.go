package kubernetes

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// DefaultForwardIdleTimeout is how long an idle tunnel is kept before the reaper closes it.
// It MUST stay >= the connection hydration cache TTL (30m): while a hydrated URL is cached its
// tunnel must stay up, since re-establishing would allocate a different local port and the
// cached localhost:<port> would be dead.
const DefaultForwardIdleTimeout = 30 * time.Minute

// establisher opens a tunnel and returns the local port and a stop channel that tears it down.
// It matches PortForward and is a field on ForwardManager so tests can inject a stub without a
// real cluster (SPDY port-forward cannot run against a fake clientset).
type establisher func(ctx context.Context, k8s kubernetes.Interface, restConfig *rest.Config, opts PortForwardOptions) (int, chan struct{}, error)

// ForwardManager owns live port-forward tunnels and reuses them across hydrations and
// connections. Tunnels are keyed by cluster + target so concurrent callers requesting the same
// workload share one tunnel, and a reaper closes tunnels left idle past idleTimeout.
type ForwardManager struct {
	mu          sync.Mutex
	tunnels     map[string]*tunnel
	idleTimeout time.Duration
	establish   establisher
}

type tunnel struct {
	localPort  int
	stop       chan struct{}
	lastAccess time.Time
}

// alive reports whether the tunnel's stop channel is still open (the forward goroutine running).
func (t *tunnel) alive() bool {
	select {
	case <-t.stop:
		return false
	default:
		return true
	}
}

// NewForwardManager returns a manager with the given idle timeout, defaulting to
// DefaultForwardIdleTimeout, and starts its reaper using PortForward as the establisher.
func NewForwardManager(idleTimeout time.Duration) *ForwardManager {
	if idleTimeout <= 0 {
		idleTimeout = DefaultForwardIdleTimeout
	}
	m := &ForwardManager{
		tunnels:     make(map[string]*tunnel),
		idleTimeout: idleTimeout,
		establish:   PortForward,
	}
	go m.reap()
	return m
}

var (
	defaultForwardManager     *ForwardManager
	defaultForwardManagerOnce sync.Once
)

// DefaultForwardManager returns the process-wide forward manager, creating it on first use.
func DefaultForwardManager() *ForwardManager {
	defaultForwardManagerOnce.Do(func() {
		defaultForwardManager = NewForwardManager(DefaultForwardIdleTimeout)
	})
	return defaultForwardManager
}

// Forward returns a local port that tunnels to the workload identified by opts, reusing a live
// tunnel when one already exists for the same cluster + target and (re)establishing otherwise.
func (m *ForwardManager) Forward(ctx context.Context, k8s kubernetes.Interface, restConfig *rest.Config, opts PortForwardOptions) (int, error) {
	key := forwardKey(restConfig, opts)

	m.mu.Lock()
	defer m.mu.Unlock()

	if t, ok := m.tunnels[key]; ok {
		if t.alive() {
			t.lastAccess = time.Now()
			return t.localPort, nil
		}
		delete(m.tunnels, key)
	}

	localPort, stop, err := m.establish(ctx, k8s, restConfig, opts)
	if err != nil {
		return 0, fmt.Errorf("port-forward to %s/%s: %w", opts.Namespace, forwardTarget(opts), err)
	}

	m.tunnels[key] = &tunnel{localPort: localPort, stop: stop, lastAccess: time.Now()}
	return localPort, nil
}

// reap closes and drops tunnels idle past idleTimeout.
func (m *ForwardManager) reap() {
	ticker := time.NewTicker(m.idleTimeout / 2)
	defer ticker.Stop()
	for range ticker.C {
		m.reapOnce(time.Now())
	}
}

// reapOnce closes tunnels last accessed before now-idleTimeout, or already dead. Split out so
// reaping is unit-testable without waiting on the ticker.
func (m *ForwardManager) reapOnce(now time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for key, t := range m.tunnels {
		if !t.alive() {
			delete(m.tunnels, key)
			continue
		}
		if now.Sub(t.lastAccess) >= m.idleTimeout {
			close(t.stop)
			delete(m.tunnels, key)
		}
	}
}

// forwardKey identifies a tunnel by cluster fingerprint and target coordinates so identical
// requests share a tunnel and requests to different clusters/workloads do not collide.
func forwardKey(restConfig *rest.Config, opts PortForwardOptions) string {
	return strings.Join([]string{
		RestConfigFingerprint(restConfig),
		opts.Namespace,
		opts.Kind,
		opts.Name,
		opts.LabelSelector,
		fmt.Sprintf("%d", opts.RemotePort),
	}, "|")
}

func forwardTarget(opts PortForwardOptions) string {
	if opts.Name != "" {
		return fmt.Sprintf("%s/%s", opts.Kind, opts.Name)
	}
	return fmt.Sprintf("%s[%s]", opts.Kind, opts.LabelSelector)
}
