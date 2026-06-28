package context

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"

	dutyKubernetes "github.com/flanksource/commons-db/kubernetes"
	"github.com/flanksource/commons-db/models"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Workload URL schemes select how a connection URL stored as
// `<strategy>://<name>[.<namespace>][:<port>][/path]` is expanded to a concrete
// address at hydration time using the cluster client. The connection form's
// workload picker writes these; the application scheme (postgres, http, ...) is
// derived from the connection type at resolution, keeping the stored connection
// portable across clusters.
const (
	schemeSvc         = "svc"         // in-cluster Service DNS: <name>.<namespace>.svc.cluster.local
	schemeIP          = "ip"          // Service ClusterIP
	schemeProxy       = "proxy"       // apiserver service-proxy URL (HTTP backends only)
	schemeHost        = "host"        // first Ingress rule host
	schemePortForward = "portforward" // SPDY port-forward tunnel via localhost (any backend, incl. SQL)
)

// serviceRef is a parsed workload URL. kind and selector are only meaningful for the
// portforward strategy, which carries them as query params (?kind=service&selector=app%3Ddb).
type serviceRef struct {
	strategy  string
	name      string
	namespace string
	port      string
	path      string
	kind      string
	selector  string
}

// parseServiceRef parses a workload URL into its parts, reporting false for any
// other string so plain URLs/DSNs pass through untouched.
func parseServiceRef(raw string) (serviceRef, bool) {
	u, err := url.Parse(raw)
	if err != nil {
		return serviceRef{}, false
	}
	switch u.Scheme {
	case schemeSvc, schemeIP, schemeProxy, schemeHost, schemePortForward:
	default:
		return serviceRef{}, false
	}

	ref := serviceRef{strategy: u.Scheme, port: u.Port(), path: u.Path}
	host := u.Hostname()
	if i := strings.IndexByte(host, '.'); i >= 0 {
		ref.name, ref.namespace = host[:i], host[i+1:]
	} else {
		ref.name = host
	}

	q := u.Query()
	ref.kind = q.Get("kind")
	ref.selector = q.Get("selector")

	// portforward can target by name or by label selector; every other strategy needs a name.
	bySelector := ref.strategy == schemePortForward && ref.selector != ""
	if ref.name == "" && !bySelector {
		return serviceRef{}, false
	}
	return ref, true
}

// expandServiceURL expands a workload URL (svc://, ip://, proxy://, host://) into
// a concrete `<scheme>://host[:port][/path]` using the cluster client; the
// application scheme is derived from connType. Non-workload URLs are returned
// unchanged. defaultNS is used when the reference omits a namespace.
func (k Context) expandServiceURL(raw, connType, defaultNS string) (string, error) {
	ref, ok := parseServiceRef(raw)
	if !ok {
		return raw, nil
	}

	ns := ref.namespace
	if ns == "" {
		ns = defaultNS
	}
	if ns == "" {
		ns = k.GetNamespace()
	}
	if ns == "" {
		return "", fmt.Errorf("workload url %q: namespace is required", raw)
	}

	client, err := k.LocalKubernetes()
	if err != nil {
		return "", fmt.Errorf("workload url %q: kubernetes client unavailable: %w", raw, err)
	}
	scheme := schemeForConnectionType(connType)

	switch ref.strategy {
	case schemeSvc:
		host := fmt.Sprintf("%s.%s.svc.cluster.local", ref.name, ns)
		return buildServiceURL(scheme, host, ref.port, ref.path), nil

	case schemeIP:
		svc, err := client.CoreV1().Services(ns).Get(k, ref.name, metav1.GetOptions{})
		if err != nil {
			return "", fmt.Errorf("workload url %q: get service %s/%s: %w", raw, ns, ref.name, err)
		}
		if svc.Spec.ClusterIP == "" || svc.Spec.ClusterIP == "None" {
			return "", fmt.Errorf("workload url %q: service %s/%s has no cluster IP", raw, ns, ref.name)
		}
		return buildServiceURL(scheme, svc.Spec.ClusterIP, ref.port, ref.path), nil

	case schemeHost:
		if scheme != "http" && scheme != "https" {
			return "", fmt.Errorf("workload url %q: host strategy only supports HTTP connections, not %q", raw, connType)
		}
		ing, err := client.NetworkingV1().Ingresses(ns).Get(k, ref.name, metav1.GetOptions{})
		if err != nil {
			return "", fmt.Errorf("workload url %q: get ingress %s/%s: %w", raw, ns, ref.name, err)
		}
		host := firstIngressHost(ing)
		if host == "" {
			return "", fmt.Errorf("workload url %q: ingress %s/%s has no host", raw, ns, ref.name)
		}
		if ingressHostHasTLS(ing, host) {
			scheme = "https"
		}
		return buildServiceURL(scheme, host, ref.port, ref.path), nil

	case schemeProxy:
		if scheme != "http" && scheme != "https" {
			return "", fmt.Errorf("workload url %q: proxy strategy only supports HTTP connections, not %q", raw, connType)
		}
		base := strings.TrimRight(client.RestConfig().Host, "/")
		if base == "" {
			return "", fmt.Errorf("workload url %q: apiserver host unknown", raw)
		}
		target := ref.name
		if ref.port != "" {
			target = fmt.Sprintf("%s:%s:%s", scheme, ref.name, ref.port)
		}
		return fmt.Sprintf("%s/api/v1/namespaces/%s/services/%s/proxy%s", base, ns, target, ref.path), nil

	case schemePortForward:
		kind := ref.kind
		if kind == "" {
			kind = "service"
		}
		remotePort := 0
		if ref.port != "" {
			p, err := strconv.Atoi(ref.port)
			if err != nil {
				return "", fmt.Errorf("workload url %q: invalid port %q: %w", raw, ref.port, err)
			}
			remotePort = p
		}
		localPort, err := dutyKubernetes.DefaultForwardManager().Forward(k, client, client.RestConfig(), dutyKubernetes.PortForwardOptions{
			Namespace:     ns,
			Name:          ref.name,
			LabelSelector: ref.selector,
			RemotePort:    remotePort,
			Kind:          kind,
		})
		if err != nil {
			return "", fmt.Errorf("workload url %q: %w", raw, err)
		}
		return buildServiceURL(scheme, "localhost", strconv.Itoa(localPort), ref.path), nil
	}

	return raw, nil
}

func buildServiceURL(scheme, host, port, path string) string {
	authority := host
	if port != "" {
		authority = host + ":" + port
	}
	return scheme + "://" + authority + path
}

func firstIngressHost(ing *networkingv1.Ingress) string {
	for _, rule := range ing.Spec.Rules {
		if rule.Host != "" {
			return rule.Host
		}
	}
	return ""
}

// ingressHostHasTLS reports whether the ingress terminates TLS for the host, so
// the resolved URL uses https. A TLS entry with no hosts is a catch-all.
func ingressHostHasTLS(ing *networkingv1.Ingress, host string) bool {
	for _, t := range ing.Spec.TLS {
		if len(t.Hosts) == 0 {
			return true
		}
		for _, h := range t.Hosts {
			if h == host {
				return true
			}
		}
	}
	return false
}

// schemeForConnectionType maps a connection type to the URL scheme its driver
// expects. HTTP-style and unknown backends default to http.
func schemeForConnectionType(connType string) string {
	switch connType {
	case models.ConnectionTypePostgres:
		return "postgres"
	case models.ConnectionTypeMySQL:
		return "mysql"
	case models.ConnectionTypeSQLServer:
		return "sqlserver"
	case models.ConnectionTypeClickHouse:
		return "clickhouse"
	case models.ConnectionTypeMongo:
		return "mongodb"
	case models.ConnectionTypeRedis:
		return "redis"
	default:
		return "http"
	}
}
