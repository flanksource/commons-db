package connection

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	netHTTP "net/http"
	"strings"
	"time"

	"github.com/flanksource/commons/http"
	"github.com/flanksource/commons/http/middlewares"
	"github.com/labstack/echo/v4"

	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/types"
)

// +kubebuilder:object:generate=true
type TLSConfig struct {
	// InsecureSkipVerify controls whether a client verifies the server's
	// certificate chain and host name
	InsecureSkipVerify bool `json:"insecureSkipVerify,omitempty" yaml:"insecureSkipVerify,omitempty"`
	// HandshakeTimeout defaults to 10 seconds
	HandshakeTimeout time.Duration `json:"handshakeTimeout,omitempty" yaml:"handshakeTimeout,omitempty"`
	// PEM encoded certificate of the CA to verify the server certificate
	CA types.EnvVar `json:"ca,omitempty" yaml:"ca,omitempty"`
	// PEM encoded client certificate
	Cert types.EnvVar `json:"cert,omitempty" yaml:"cert,omitempty"`
	// PEM encoded client private key
	Key types.EnvVar `json:"key,omitempty" yaml:"key,omitempty"`
}

func (t TLSConfig) IsEmpty() bool {
	return !t.InsecureSkipVerify && t.HandshakeTimeout == 0 && t.CA.IsEmpty() && t.Cert.IsEmpty() && t.Key.IsEmpty()
}

// +kubebuilder:object:generate=true
type HTTPConnection struct {
	ConnectionName      string `json:"connection,omitempty" yaml:"connection,omitempty"`
	types.HTTPBasicAuth `json:",inline"`
	URL                 string       `json:"url,omitempty" yaml:"url,omitempty"`
	Bearer              types.EnvVar `json:"bearer,omitempty" yaml:"bearer,omitempty"`
	OAuth               types.OAuth  `json:"oauth,omitempty" yaml:"oauth,omitempty"`
	TLS                 TLSConfig    `json:"tls,omitempty" yaml:"tls,omitempty"`
}

func (t *HTTPConnection) FromModel(connection models.Connection) error {
	t.URL = connection.URL
	t.TLS.InsecureSkipVerify = connection.InsecureTLS
	return t.applyModelAuthentication(connection)
}

func (t *HTTPConnection) applyModelAuthentication(connection models.Connection) error {
	authType := strings.ToLower(strings.TrimSpace(connection.Properties["authType"]))
	loadBasic := authType == "basic" || authType == ""
	loadOAuth := authType == "oauth" || authType == ""
	loadMTLS := authType == "mtls" || authType == ""

	if loadBasic {
		username := connection.Properties["username"]
		if username == "" {
			username = connection.Username
		}
		password := connection.Properties["password"]
		if password == "" {
			password = connection.Password
		}
		if err := t.HTTPBasicAuth.Username.Scan(username); err != nil {
			return fmt.Errorf("error scanning username: %w", err)
		}
		if err := t.HTTPBasicAuth.Password.Scan(password); err != nil {
			return fmt.Errorf("error scanning password: %w", err)
		}
	}

	if authType == "" {
		if bearer := connection.Properties["bearer"]; bearer != "" {
			if err := t.Bearer.Scan(bearer); err != nil {
				return fmt.Errorf("error scanning bearer: %w", err)
			}
		}
	}

	if loadOAuth {
		if clientID := connection.Properties["clientID"]; clientID != "" {
			if err := t.OAuth.ClientID.Scan(clientID); err != nil {
				return fmt.Errorf("error scanning oauth client_id: %w", err)
			}
		}
		if clientSecret := connection.Properties["clientSecret"]; clientSecret != "" {
			if err := t.OAuth.ClientSecret.Scan(clientSecret); err != nil {
				return fmt.Errorf("error scanning oauth client_secret: %w", err)
			}
		}
		t.OAuth.TokenURL = connection.Properties["tokenURL"]
		if oauthParams := connection.Properties["params"]; oauthParams != "" {
			if err := json.Unmarshal([]byte(oauthParams), &t.OAuth.Params); err != nil {
				return fmt.Errorf("error unmarshaling params:%s in oauth: %w", oauthParams, err)
			}
		}
		if scopes := connection.Properties["scopes"]; scopes != "" {
			t.OAuth.Scopes = strings.Split(scopes, ",")
		}
	}

	if loadMTLS {
		if ca := connection.Properties["ca"]; ca != "" {
			if err := t.TLS.CA.Scan(ca); err != nil {
				return fmt.Errorf("error scanning TLS CA: %w", err)
			}
		} else if authType == "" && connection.Certificate != "" {
			if err := t.TLS.CA.Scan(connection.Certificate); err != nil {
				return fmt.Errorf("error scanning TLS certificate: %w", err)
			}
		}
		if cert := connection.Properties["cert"]; cert != "" {
			if err := t.TLS.Cert.Scan(cert); err != nil {
				return fmt.Errorf("error scanning TLS client certificate: %w", err)
			}
		}
		if key := connection.Properties["key"]; key != "" {
			if err := t.TLS.Key.Scan(key); err != nil {
				return fmt.Errorf("error scanning TLS client private key: %w", err)
			}
		}
	}

	return nil
}

func (h HTTPConnection) GetEndpoint() string {
	return h.URL
}

func (h *HTTPConnection) Hydrate(ctx ConnectionContext, namespace string) (*HTTPConnection, error) {
	var err error
	if h.ConnectionName != "" {
		connection, err := ctx.HydrateConnectionByURL(h.ConnectionName)
		if err != nil {
			return h, fmt.Errorf("could not hydrate connection[%s]: %w", h.ConnectionName, err)
		}
		if connection == nil {
			return h, fmt.Errorf("connection[%s] not found", h.ConnectionName)
		}
		*h, err = NewHTTPConnection(ctx, *connection)
		if err != nil {
			return h, fmt.Errorf("error creating connection from model: %w", err)
		}
	}

	// URL can be an EnvVar string so we
	// typecase to EnvVar and scan it first
	var url types.EnvVar
	if err := url.Scan(h.URL); err != nil {
		return h, err
	}
	h.URL, err = ctx.GetEnvValueFromCache(url, namespace)
	if err != nil {
		return h, err
	}

	h.Authentication.Username.ValueStatic, err = ctx.GetEnvValueFromCache(h.Authentication.Username, namespace)
	if err != nil {
		return h, err
	}
	h.Authentication.Password.ValueStatic, err = ctx.GetEnvValueFromCache(h.Authentication.Password, namespace)
	if err != nil {
		return h, err
	}

	h.Bearer.ValueStatic, err = ctx.GetEnvValueFromCache(h.Bearer, namespace)
	if err != nil {
		return h, err
	}

	h.OAuth.ClientID.ValueStatic, err = ctx.GetEnvValueFromCache(h.OAuth.ClientID, namespace)
	if err != nil {
		return h, err
	}
	h.OAuth.ClientSecret.ValueStatic, err = ctx.GetEnvValueFromCache(h.OAuth.ClientSecret, namespace)
	if err != nil {
		return h, err
	}

	h.TLS.Key.ValueStatic, err = ctx.GetEnvValueFromCache(h.TLS.Key, namespace)
	if err != nil {
		return h, err
	}
	h.TLS.CA.ValueStatic, err = ctx.GetEnvValueFromCache(h.TLS.CA, namespace)
	if err != nil {
		return h, err
	}
	h.TLS.Cert.ValueStatic, err = ctx.GetEnvValueFromCache(h.TLS.Cert, namespace)
	if err != nil {
		return h, err
	}
	return h, nil
}

func (h HTTPConnection) Transport() netHTTP.RoundTripper {
	rt := &httpConnectionRoundTripper{
		HTTPConnection: h,
		Base:           &netHTTP.Transport{},
	}
	return rt
}

type httpConnectionRoundTripper struct {
	HTTPConnection
	Base netHTTP.RoundTripper
}

func (rt *httpConnectionRoundTripper) RoundTrip(req *netHTTP.Request) (*netHTTP.Response, error) {
	conn := rt.HTTPConnection
	base := rt.Base
	if !conn.TLS.IsEmpty() {
		transport, ok := base.(*netHTTP.Transport)
		if !ok {
			return nil, fmt.Errorf("cannot configure TLS on transport %T", base)
		}
		tlsConfig, err := conn.TLS.transportConfig()
		if err != nil {
			return nil, err
		}
		transport = transport.Clone()
		transport.TLSClientConfig = tlsConfig
		transport.TLSHandshakeTimeout = conn.TLS.HandshakeTimeout
		base = transport
	}
	if !conn.HTTPBasicAuth.IsEmpty() {
		req.SetBasicAuth(conn.HTTPBasicAuth.GetUsername(), conn.HTTPBasicAuth.GetPassword())
	} else if !conn.Bearer.IsEmpty() {
		req.Header.Set(echo.HeaderAuthorization, "Bearer "+conn.Bearer.ValueStatic)
	} else if !conn.OAuth.IsEmpty() {
		oauthTransport := middlewares.NewOauthTransport(middlewares.OauthConfig{
			ClientID:     conn.OAuth.ClientID.String(),
			ClientSecret: conn.OAuth.ClientSecret.String(),
			TokenURL:     conn.OAuth.TokenURL,
			Params:       conn.OAuth.Params,
			Scopes:       conn.OAuth.Scopes,
		})
		base = oauthTransport.RoundTripper(base)
	}

	return base.RoundTrip(req)
}

func (t TLSConfig) transportConfig() (*tls.Config, error) {
	config := &tls.Config{InsecureSkipVerify: t.InsecureSkipVerify} //nolint:gosec // explicit per-connection opt-in
	if !t.CA.IsEmpty() {
		roots, err := x509.SystemCertPool()
		if err != nil || roots == nil {
			roots = x509.NewCertPool()
		}
		if !roots.AppendCertsFromPEM([]byte(t.CA.ValueStatic)) {
			return nil, fmt.Errorf("invalid TLS CA certificate")
		}
		config.RootCAs = roots
	}
	if t.Cert.IsEmpty() != t.Key.IsEmpty() {
		return nil, fmt.Errorf("mTLS requires both a client certificate and private key")
	}
	if !t.Cert.IsEmpty() {
		certificate, err := tls.X509KeyPair([]byte(t.Cert.ValueStatic), []byte(t.Key.ValueStatic))
		if err != nil {
			return nil, fmt.Errorf("invalid mTLS client certificate or private key: %w", err)
		}
		config.Certificates = []tls.Certificate{certificate}
	}
	return config, nil
}

// CreateHTTPClient requires a hydrated connection
func CreateHTTPClient(ctx ConnectionContext, conn HTTPConnection) (*http.Client, error) {
	client := http.NewClient()
	if !conn.HTTPBasicAuth.IsEmpty() {
		client.Auth(conn.GetUsername(), conn.GetPassword())
		client.Digest(conn.Digest)
		client.NTLM(conn.NTLM)
		client.NTLMV2(conn.NTLMV2)
	} else if !conn.Bearer.IsEmpty() {
		client.Header(echo.HeaderAuthorization, "Bearer "+conn.Bearer.ValueStatic)
	} else if !conn.OAuth.IsEmpty() {
		client.OAuth(middlewares.OauthConfig{
			ClientID:     conn.OAuth.ClientID.ValueStatic,
			ClientSecret: conn.OAuth.ClientSecret.ValueStatic,
			TokenURL:     conn.OAuth.TokenURL,
			Params:       conn.OAuth.Params,
			Scopes:       conn.OAuth.Scopes,
		})
	}

	if !conn.TLS.IsEmpty() {
		_, err := client.TLSConfig(http.TLSConfig{
			CA:                 conn.TLS.CA.ValueStatic,
			Cert:               conn.TLS.Cert.ValueStatic,
			Key:                conn.TLS.Key.ValueStatic,
			InsecureSkipVerify: conn.TLS.InsecureSkipVerify,
			HandshakeTimeout:   conn.TLS.HandshakeTimeout,
		})
		if err != nil {
			return nil, fmt.Errorf("error setting tls config: %w", err)
		}
	}

	return client, nil
}

func NewHTTPConnection(ctx ConnectionContext, conn models.Connection) (HTTPConnection, error) {
	var httpConn HTTPConnection
	switch conn.Type {
	case models.ConnectionTypeHTTP, models.ConnectionTypePrometheus,
		models.ConnectionTypeOpenSearch, models.ConnectionTypeLoki, models.ConnectionTypeJaeger:
		if err := httpConn.FromModel(conn); err != nil {
			return httpConn, err
		}

		if _, err := httpConn.Hydrate(ctx, conn.Namespace); err != nil {
			return httpConn, fmt.Errorf("error hydrating connection: %w", err)
		}

	default:
		return httpConn, fmt.Errorf("invalid connection type: %s", conn.Type)
	}

	return httpConn, nil
}
