package connection

import (
	"fmt"
	netHTTP "net/http"
	"net/url"

	"github.com/flanksource/commons/http"
	"github.com/flanksource/commons/http/middlewares"
	"k8s.io/client-go/rest"

	dutyKubernetes "github.com/flanksource/commons-db/kubernetes"
)

// A proxy:// connection URL resolves to an apiserver services/proxy or
// pods/proxy URL and registers the kubeconfig identity it was resolved with
// (dutyKubernetes.RegisterAPIServerProxy). Requests to such a URL authenticate
// to the apiserver with that identity, never with the connection's own
// credentials: the apiserver deletes the Authorization header once it has
// authenticated a request, so basic, bearer or OAuth credentials for the
// backend cannot reach it and are refused instead of being dropped.

// apiServerProxy returns the apiserver identity registered for the
// connection URL, or nil when the URL is not an apiserver proxy URL.
func (h HTTPConnection) apiServerProxy() (*dutyKubernetes.APIServerProxy, error) {
	u, err := url.Parse(h.URL)
	if err != nil {
		return nil, fmt.Errorf("connection url: %w", err)
	}
	return dutyKubernetes.APIServerProxyFor(u)
}

// checkAPIServerProxy refuses connection settings an apiserver proxy hop
// cannot honour.
func (h HTTPConnection) checkAPIServerProxy(target string) error {
	if !h.HTTPBasicAuth.IsEmpty() || !h.Bearer.IsEmpty() || !h.OAuth.IsEmpty() {
		return fmt.Errorf("%s is an apiserver proxy URL: the apiserver authenticates the request with the kubeconfig identity and deletes the Authorization header before proxying, "+
			"so the connection's basic, bearer or oauth credentials cannot reach the backend; pass backend credentials as a query parameter in the URL, "+
			"or reach the backend with svc://, host:// or portforward:// instead", target)
	}
	if !h.TLS.IsEmpty() {
		return fmt.Errorf("%s is an apiserver proxy URL: TLS settings would apply to the apiserver hop, which the kubeconfig configures; remove them from the connection", target)
	}
	return nil
}

func (h HTTPConnection) roundTripAPIServerProxy(proxy *dutyKubernetes.APIServerProxy, req *netHTTP.Request) (*netHTTP.Response, error) {
	if err := h.checkAPIServerProxy(req.URL.String()); err != nil {
		return nil, err
	}
	transport, err := proxy.Transport()
	if err != nil {
		return nil, err
	}
	return transport.RoundTrip(req)
}

// apiServerProxyClient builds a commons HTTP client that authenticates to the
// apiserver with the proxy's rest config: its CA and client certificate on
// the TLS transport, its token, basic or exec credentials as a middleware.
func apiServerProxyClient(conn HTTPConnection, proxy *dutyKubernetes.APIServerProxy) (*http.Client, error) {
	if err := conn.checkAPIServerProxy(conn.URL); err != nil {
		return nil, err
	}
	config := rest.CopyConfig(proxy.Config())
	if err := rest.LoadTLSFiles(config); err != nil {
		return nil, fmt.Errorf("apiserver proxy %s: load kubeconfig TLS files: %w", conn.URL, err)
	}
	if config.ServerName != "" {
		return nil, fmt.Errorf("apiserver proxy %s: the kubeconfig sets tls-server-name %q, which this HTTP client cannot apply; use the connection's Transport()", conn.URL, config.ServerName)
	}
	if _, err := rest.HTTPWrappersForConfig(config, netHTTP.DefaultTransport); err != nil {
		return nil, fmt.Errorf("apiserver proxy %s: kubeconfig credentials: %w", conn.URL, err)
	}
	client := http.NewClient()
	if _, err := client.TLSConfig(http.TLSConfig{
		CA: string(config.CAData), Cert: string(config.CertData), Key: string(config.KeyData), InsecureSkipVerify: config.Insecure,
	}); err != nil {
		return nil, fmt.Errorf("apiserver proxy %s: kubeconfig TLS: %w", conn.URL, err)
	}
	client.Use(func(next netHTTP.RoundTripper) netHTTP.RoundTripper {
		wrapped, err := rest.HTTPWrappersForConfig(config, next)
		if err != nil {
			return middlewares.RoundTripperFunc(func(*netHTTP.Request) (*netHTTP.Response, error) { return nil, err })
		}
		return wrapped
	})
	return client, nil
}
