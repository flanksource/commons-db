package kubernetes

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// DefaultForwardIdleTimeout disables time-based closure. A resolved URL does not
// carry a lease, so elapsed time since hydration cannot prove that no database
// pool is still using the tunnel. Capacity eviction, forwarder completion, and
// CloseAll provide bounded lifecycle controls without closing active clients.
const (
	DefaultForwardIdleTimeout = time.Duration(0)
	DefaultMaxForwards        = 64
)

// establisher opens a tunnel and returns the local port and a stop channel that tears it down.
// It matches PortForward and is a field on ForwardManager so tests can inject a stub without a
// real cluster (SPDY port-forward cannot run against a fake clientset).
type establisher func(ctx context.Context, k8s kubernetes.Interface, restConfig *rest.Config, opts PortForwardOptions) (*forwardSession, error)

type forwardSession struct {
	localPort int
	stop      chan struct{}
	done      <-chan struct{}
}

// ForwardManager owns live port-forward tunnels and reuses them across hydrations and
// connections. Tunnels are keyed by cluster + target so concurrent callers requesting the same
// workload share one tunnel, and a reaper closes tunnels left idle past idleTimeout.
type ForwardManager struct {
	mu          sync.Mutex
	tunnels     map[string]*tunnel
	idleTimeout time.Duration
	maxForwards int
	establish   establisher
	inflight    singleflight.Group
}

type tunnel struct {
	localPort  int
	stop       chan struct{}
	done       <-chan struct{}
	lastAccess time.Time
}

// alive reports whether the tunnel's stop channel is still open (the forward goroutine running).
func (t *tunnel) alive() bool {
	select {
	case <-t.stop:
		return false
	default:
	}
	select {
	case <-t.done:
		return false
	default:
		return true
	}
}

// NewForwardManager returns a manager with an optional idle timeout. A zero
// timeout disables time-based reaping.
func NewForwardManager(idleTimeout time.Duration) *ForwardManager {
	if idleTimeout < 0 {
		idleTimeout = 0
	}
	m := &ForwardManager{
		tunnels:     make(map[string]*tunnel),
		idleTimeout: idleTimeout,
		maxForwards: DefaultMaxForwards,
		establish:   establishManagedPortForward,
	}
	if idleTimeout > 0 {
		go m.reap()
	}
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
	if opts.Name != "" {
		// Name takes precedence during target resolution; dropping an unused
		// selector also prevents equivalent requests creating duplicate tunnels.
		opts.LabelSelector = ""
	}
	key := forwardKey(restConfig, opts)
	if port, ok := m.reuse(key); ok {
		return port, nil
	}

	value, err, _ := m.inflight.Do(key, func() (any, error) {
		if port, ok := m.reuse(key); ok {
			return port, nil
		}
		m.mu.Lock()
		capacityErr := m.ensureCapacityLocked()
		m.mu.Unlock()
		if capacityErr != nil {
			return 0, capacityErr
		}
		session, err := m.establish(ctx, k8s, restConfig, opts)
		if err != nil {
			return 0, fmt.Errorf("port-forward to %s/%s: %w", opts.Namespace, forwardTarget(opts), err)
		}

		m.mu.Lock()
		defer m.mu.Unlock()
		if err := m.ensureCapacityLocked(); err != nil {
			select {
			case <-session.stop:
			default:
				close(session.stop)
			}
			return 0, err
		}
		m.tunnels[key] = &tunnel{
			localPort: session.localPort, stop: session.stop, done: session.done, lastAccess: time.Now(),
		}
		return session.localPort, nil
	})
	if err != nil {
		return 0, err
	}
	return value.(int), nil
}

func (m *ForwardManager) reuse(key string) (int, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tunnels[key]
	if !ok {
		return 0, false
	}
	if !t.alive() {
		m.closeTunnelLocked(key, t)
		return 0, false
	}
	t.lastAccess = time.Now()
	return t.localPort, true
}

// reap closes and drops tunnels idle past idleTimeout.
func (m *ForwardManager) reap() {
	if m.idleTimeout <= 0 {
		return
	}
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
			m.closeTunnelLocked(key, t)
			continue
		}
		if now.Sub(t.lastAccess) >= m.idleTimeout {
			m.closeTunnelLocked(key, t)
		}
	}
}

func (m *ForwardManager) ensureCapacityLocked() error {
	for key, candidate := range m.tunnels {
		if !candidate.alive() {
			m.closeTunnelLocked(key, candidate)
		}
	}
	if m.maxForwards <= 0 || len(m.tunnels) < m.maxForwards {
		return nil
	}
	return fmt.Errorf("port-forward capacity reached (%d live tunnels)", m.maxForwards)
}

func (m *ForwardManager) closeTunnelLocked(key string, t *tunnel) {
	select {
	case <-t.stop:
	default:
		close(t.stop)
	}
	delete(m.tunnels, key)
}

// CloseAll terminates every tunnel owned by the manager. It is safe to call
// repeatedly and gives servers/tests an explicit lifecycle boundary.
func (m *ForwardManager) CloseAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for key, t := range m.tunnels {
		m.closeTunnelLocked(key, t)
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
