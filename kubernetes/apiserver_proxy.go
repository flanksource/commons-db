package kubernetes

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"k8s.io/client-go/rest"
)

// APIServerProxy is the kubeconfig identity that authenticates requests to an
// apiserver proxy URL (/api/v1/namespaces/<ns>/{services,pods}/<target>/proxy).
//
// The apiserver deletes the Authorization header after authenticating a
// request, before proxying it, so a backend behind services/proxy or
// pods/proxy never sees the caller's Authorization header. Backend
// credentials have to travel another way (a query parameter, or a header the
// backend reads instead of Authorization).
//
// RBAC: create/get on services/proxy or pods/proxy in the target namespace.
type APIServerProxy struct {
	config *rest.Config

	mu          sync.Mutex
	fingerprint string
	transport   http.RoundTripper
}

// Config returns the rest config the proxy authenticates with.
func (p *APIServerProxy) Config() *rest.Config { return p.config }

// Transport returns an apiserver-authenticated transport (bearer, basic, exec,
// client certificate and CA from the rest config). It is rebuilt when the
// config's identity changes in place, e.g. a refreshed token.
func (p *APIServerProxy) Transport() (http.RoundTripper, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	fingerprint := RestConfigFingerprint(p.config)
	if p.transport != nil && fingerprint == p.fingerprint {
		return p.transport, nil
	}
	transport, err := rest.TransportFor(p.config)
	if err != nil {
		return nil, fmt.Errorf("apiserver transport for %s: %w", p.config.Host, err)
	}
	p.transport, p.fingerprint = transport, fingerprint
	return transport, nil
}

var apiServerProxies = struct {
	sync.Mutex
	byBase map[string][]*APIServerProxy
}{byBase: map[string][]*APIServerProxy{}}

// RegisterAPIServerProxy records that URLs under proxyBase authenticate to the
// apiserver as config. Registering the same config (or an identical one) again
// is a no-op; a second identity for the same base makes lookups ambiguous.
func RegisterAPIServerProxy(proxyBase string, config *rest.Config) error {
	u, err := url.Parse(proxyBase)
	if err != nil {
		return fmt.Errorf("apiserver proxy %q: %w", proxyBase, err)
	}
	base, ok := apiServerProxyBase(u)
	if !ok {
		return fmt.Errorf("%q is not an apiserver proxy URL (/api/v1/namespaces/<ns>/{services,pods}/<target>/proxy)", proxyBase)
	}
	if config == nil {
		return fmt.Errorf("apiserver proxy %s: rest config is required", base)
	}
	apiServerProxies.Lock()
	defer apiServerProxies.Unlock()
	fingerprint := RestConfigFingerprint(config)
	for _, existing := range apiServerProxies.byBase[base] {
		if existing.config == config || RestConfigFingerprint(existing.config) == fingerprint {
			return nil
		}
	}
	apiServerProxies.byBase[base] = append(apiServerProxies.byBase[base], &APIServerProxy{config: config})
	return nil
}

// APIServerProxyFor returns the identity registered for the proxy base u falls
// under, or nil when u is not under a registered apiserver proxy URL.
func APIServerProxyFor(u *url.URL) (*APIServerProxy, error) {
	base, ok := apiServerProxyBase(u)
	if !ok {
		return nil, nil
	}
	apiServerProxies.Lock()
	defer apiServerProxies.Unlock()
	switch proxies := apiServerProxies.byBase[base]; len(proxies) {
	case 0:
		return nil, nil
	case 1:
		return proxies[0], nil
	default:
		return nil, fmt.Errorf("apiserver proxy %s was resolved with %d kubeconfig identities in this process; cannot choose which one authenticates the request", base, len(proxies))
	}
}

// apiServerProxyBase returns scheme://host[/prefix]/api/v1/namespaces/<ns>/<services|pods>/<target>/proxy
// for a URL under an apiserver proxy path; the prefix covers apiservers served
// below a path (e.g. a cluster behind a management proxy).
func apiServerProxyBase(u *url.URL) (string, bool) {
	if u == nil || u.Host == "" {
		return "", false
	}
	parts := strings.Split(strings.TrimPrefix(u.Path, "/"), "/")
	for i := 0; i+7 <= len(parts); i++ {
		p := parts[i : i+7]
		if p[0] == "api" && p[1] == "v1" && p[2] == "namespaces" && p[3] != "" &&
			(p[4] == "services" || p[4] == "pods") && p[5] != "" && p[6] == "proxy" {
			return u.Scheme + "://" + canonicalHost(u) + "/" + strings.Join(parts[:i+7], "/"), true
		}
	}
	return "", false
}

// canonicalHost drops an empty or default port, so https://api, https://api:
// and https://api:443 name the same apiserver.
func canonicalHost(u *url.URL) string {
	port := u.Port()
	if port == "" || (u.Scheme == "https" && port == "443") || (u.Scheme == "http" && port == "80") {
		return u.Hostname()
	}
	return u.Host
}
