package context

import (
	"cmp"
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
// `<strategy>://<name>[.<namespace>][:<port>][/path][?kind=&selector=&port=]` is
// expanded to a concrete address at hydration time using the cluster client.
// The connection form's workload picker writes these; the application scheme
// (postgres, http, ...) is derived from the connection type at resolution,
// keeping the stored connection portable across clusters.
//
// proxy:// and portforward:// take `kind` (service when absent, pod,
// deployment, statefulset, daemonset, replicaset) and `selector` (selects the
// pods when no name is given); `port` names a port the authority cannot carry
// (`?port=http`). Any other query parameter is kept on the expanded URL.
const (
	schemeSvc         = "svc"         // in-cluster Service DNS: <name>.<namespace>.svc.cluster.local
	schemeIP          = "ip"          // Service ClusterIP
	schemeProxy       = "proxy"       // apiserver services/proxy or pods/proxy URL (HTTP backends only)
	schemeHost        = "host"        // first Ingress rule host
	schemePortForward = "portforward" // SPDY port-forward tunnel via localhost (any backend, incl. SQL)
)

// workloadQueryParams are consumed by expansion and never reach the backend.
var workloadQueryParams = []string{"kind", "selector", "port"}

// serviceRef is a parsed workload URL.
type serviceRef struct {
	strategy  string
	name      string
	namespace string
	port      string
	path      string
	kind      string
	selector  string
	query     url.Values
	// portConflict is set when the authority and ?port= name different ports.
	portConflict bool
}

func (r serviceRef) kindOrService() string {
	if r.kind == "" {
		return "service"
	}
	return r.kind
}

// workloadURL is one expansion request: the parsed reference with its
// namespace resolved, the application scheme and the cluster client.
type workloadURL struct {
	raw    string
	ref    serviceRef
	scheme string
	client *dutyKubernetes.Client
}

// workloadURLStrategies expands each workload URL scheme.
var workloadURLStrategies = map[string]func(k Context, w workloadURL) (string, error){
	schemeSvc:         expandSvcURL,
	schemeIP:          expandIPURL,
	schemeHost:        expandHostURL,
	schemeProxy:       expandProxyURL,
	schemePortForward: expandPortForwardURL,
}

// parseServiceRef parses a workload URL into its parts, reporting false for any
// other string so plain URLs/DSNs pass through untouched.
func parseServiceRef(raw string) (serviceRef, bool) {
	u, err := url.Parse(raw)
	if err != nil {
		return serviceRef{}, false
	}
	if _, ok := workloadURLStrategies[u.Scheme]; !ok {
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
	ref.kind, ref.selector = q.Get("kind"), q.Get("selector")
	if p := q.Get("port"); p != "" {
		ref.portConflict = ref.port != "" && ref.port != p
		ref.port = p
	}
	for _, param := range workloadQueryParams {
		q.Del(param)
	}
	ref.query = q

	// portforward and proxy can target pods by label selector; every other strategy needs a name.
	bySelector := (ref.strategy == schemePortForward || ref.strategy == schemeProxy) && ref.selector != ""
	if ref.name == "" && !bySelector {
		return serviceRef{}, false
	}
	return ref, true
}

// expandServiceURL expands a workload URL (svc://, ip://, proxy://, host://,
// portforward://) into a concrete `<scheme>://host[:port][/path][?query]` using
// the cluster client; the application scheme is derived from connType.
// Non-workload URLs are returned unchanged. defaultNS is used when the
// reference omits a namespace.
func (k Context) expandServiceURL(raw, connType, defaultNS string) (string, error) {
	ref, ok := parseServiceRef(raw)
	if !ok {
		return raw, nil
	}
	if ref.portConflict {
		return "", fmt.Errorf("workload url %q: the authority port and ?port= differ; give the port once", raw)
	}
	if ref.namespace = cmp.Or(ref.namespace, defaultNS, k.GetNamespace()); ref.namespace == "" {
		return "", fmt.Errorf("workload url %q: namespace is required", raw)
	}

	client, err := k.LocalKubernetes()
	if err != nil {
		return "", fmt.Errorf("workload url %q: kubernetes client unavailable: %w", raw, err)
	}
	expanded, err := workloadURLStrategies[ref.strategy](k, workloadURL{raw: raw, ref: ref, scheme: schemeForConnectionType(connType), client: client})
	if err != nil {
		return "", err
	}
	if len(ref.query) == 0 {
		return expanded, nil
	}
	return expanded + "?" + ref.query.Encode(), nil
}

func expandSvcURL(_ Context, w workloadURL) (string, error) {
	host := fmt.Sprintf("%s.%s.svc.cluster.local", w.ref.name, w.ref.namespace)
	return buildServiceURL(w.scheme, host, w.ref.port, w.ref.path), nil
}

func expandIPURL(k Context, w workloadURL) (string, error) {
	svc, err := w.client.CoreV1().Services(w.ref.namespace).Get(k, w.ref.name, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("workload url %q: get service %s/%s: %w", w.raw, w.ref.namespace, w.ref.name, err)
	}
	if svc.Spec.ClusterIP == "" || svc.Spec.ClusterIP == "None" {
		return "", fmt.Errorf("workload url %q: service %s/%s has no cluster IP", w.raw, w.ref.namespace, w.ref.name)
	}
	return buildServiceURL(w.scheme, svc.Spec.ClusterIP, w.ref.port, w.ref.path), nil
}

func expandHostURL(k Context, w workloadURL) (string, error) {
	if w.scheme != "http" && w.scheme != "https" {
		return "", fmt.Errorf("workload url %q: host strategy only supports HTTP connections", w.raw)
	}
	ing, err := w.client.NetworkingV1().Ingresses(w.ref.namespace).Get(k, w.ref.name, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("workload url %q: get ingress %s/%s: %w", w.raw, w.ref.namespace, w.ref.name, err)
	}
	host := firstIngressHost(ing)
	if host == "" {
		return "", fmt.Errorf("workload url %q: ingress %s/%s has no host", w.raw, w.ref.namespace, w.ref.name)
	}
	scheme := w.scheme
	if ingressHostHasTLS(ing, host) {
		scheme = "https"
	}
	return buildServiceURL(scheme, host, w.ref.port, w.ref.path), nil
}

// expandProxyURL builds an apiserver proxy URL: services/proxy for kind
// service, else pods/proxy to the Ready pod the workload resolves to. It
// registers the client's rest config for the URL, so connection transports
// authenticate to the apiserver (dutyKubernetes.APIServerProxyFor).
//
// RBAC: create/get on services/proxy or pods/proxy, and for workload kinds
// get on the kind plus list on pods.
func expandProxyURL(k Context, w workloadURL) (string, error) {
	if w.scheme != "http" && w.scheme != "https" {
		return "", fmt.Errorf("workload url %q: proxy strategy only supports HTTP connections", w.raw)
	}
	restConfig := w.client.RestConfig()
	base := strings.TrimRight(restConfig.Host, "/")
	if base == "" {
		return "", fmt.Errorf("workload url %q: apiserver host unknown", w.raw)
	}
	resource, target, err := proxyTarget(k, w)
	if err != nil {
		return "", fmt.Errorf("workload url %q: %w", w.raw, err)
	}
	proxyBase := fmt.Sprintf("%s/api/v1/namespaces/%s/%s/%s/proxy", base, w.ref.namespace, resource, target)
	if err := dutyKubernetes.RegisterAPIServerProxy(proxyBase, restConfig); err != nil {
		return "", fmt.Errorf("workload url %q: %w", w.raw, err)
	}
	return proxyBase + w.ref.path, nil
}

// proxyTarget returns the proxy resource (services or pods) and its
// `[scheme:]name[:port]` segment.
func proxyTarget(k Context, w workloadURL) (string, string, error) {
	if w.ref.kindOrService() == "service" {
		if w.ref.name == "" {
			return "", "", fmt.Errorf("a service proxy needs a service name; select pods with a workload kind")
		}
		if w.ref.port == "" {
			return "services", w.ref.name, nil
		}
		return "services", fmt.Sprintf("%s:%s:%s", w.scheme, w.ref.name, w.ref.port), nil
	}
	pod, err := dutyKubernetes.ResolvePodPort(k, w.client, dutyKubernetes.WorkloadTarget{
		Namespace: w.ref.namespace, Kind: w.ref.kind, Name: w.ref.name, LabelSelector: w.ref.selector, Port: w.ref.port,
	})
	if err != nil {
		return "", "", err
	}
	return "pods", fmt.Sprintf("%s:%s:%d", w.scheme, pod.Pod, pod.Port), nil
}

func expandPortForwardURL(k Context, w workloadURL) (string, error) {
	remotePort := 0
	if w.ref.port != "" {
		p, err := strconv.Atoi(w.ref.port)
		if err != nil {
			return "", fmt.Errorf("workload url %q: invalid port %q: %w", w.raw, w.ref.port, err)
		}
		remotePort = p
	}
	localPort, err := dutyKubernetes.DefaultForwardManager().Forward(k, w.client, w.client.RestConfig(), dutyKubernetes.PortForwardOptions{
		Namespace:     w.ref.namespace,
		Name:          w.ref.name,
		LabelSelector: w.ref.selector,
		RemotePort:    remotePort,
		Kind:          w.ref.kindOrService(),
	})
	if err != nil {
		return "", fmt.Errorf("workload url %q: %w", w.raw, err)
	}
	return buildServiceURL(w.scheme, "localhost", strconv.Itoa(localPort), w.ref.path), nil
}

// ResolveConnectionURL resolves a literal/EnvVar URL and then expands workload
// references (svc://, ip://, proxy://, host://, portforward://) using namespace.
// It is the shared inline-connection counterpart of HydrateConnection. A
// proxy:// result needs an apiserver-authenticated transport: connection
// HTTP transports find it with dutyKubernetes.APIServerProxyFor.
func (k Context) ResolveConnectionURL(raw, connType, namespace string) (string, error) {
	resolved, err := GetEnvStringFromCache(k, raw, namespace)
	if err != nil {
		return "", err
	}
	return k.expandServiceURL(strings.TrimSpace(resolved), connType, namespace)
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
