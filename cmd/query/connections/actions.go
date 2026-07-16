package connections

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	dbconnection "github.com/flanksource/commons-db/connection"
	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/models"
)

// connectionActionsHandler powers the connection form's "Test" split-button:
//
//   - POST {prefix}/connection/resolve -> hydrates the draft (expanding
//     secret://, configmap:// and svc:// / ip:// / proxy:// / host:// workload
//     URLs against the cluster, plus templating) and returns the resolved values
//     with secrets masked, so the operator can see what the connection resolves to.
//   - POST {prefix}/connection/test    -> resolves as above, then probes
//     reachability of the resolved URL (TCP connect, plus an HTTP request for
//     http/https URLs).
//
// Both act on the unsaved form body, so they work before the connection is saved.
type connectionActionsHandler struct {
	prefix string
	ctx    dbcontext.Context
	next   http.Handler
}

func newConnectionActionsHandler(prefix string, ctx dbcontext.Context, next http.Handler) *connectionActionsHandler {
	return &connectionActionsHandler{prefix: strings.TrimRight(prefix, "/"), ctx: ctx, next: next}
}

// resolvedConnection is the masked, hydrated view returned by /connection/resolve.
type resolvedConnection struct {
	Type        string            `json:"type"`
	Namespace   string            `json:"namespace,omitempty"`
	URL         string            `json:"url,omitempty"`
	Username    string            `json:"username,omitempty"`
	Password    string            `json:"password,omitempty"`
	Certificate string            `json:"certificate,omitempty"`
	Properties  map[string]string `json:"properties,omitempty"`
}

// testResult is the outcome returned by /connection/test.
type testResult struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`
	URL     string `json:"url,omitempty"`
}

const allowPrivateConnectionProbeProperty = "connection.test.allow-private-addresses"

func (h *connectionActionsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rel := strings.Trim(strings.TrimPrefix(strings.TrimSuffix(r.URL.Path, "/"), h.prefix), "/")
	if r.Method != http.MethodPost || (rel != "connection/resolve" && rel != "connection/test") {
		h.next.ServeHTTP(w, r)
		return
	}

	conn, draftID, err := decodeConnectionDraft(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := h.mergeStoredDraftSecrets(draftID, conn); err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	if _, err := dbcontext.HydrateConnection(h.ctx, conn); err != nil {
		http.Error(w, fmt.Sprintf("resolve connection: %v", err), http.StatusUnprocessableEntity)
		return
	}

	if rel == "connection/resolve" {
		writeJSON(w, maskedConnection(conn))
		return
	}
	writeJSON(w, testConnection(h.ctx, conn))
}

// decodeConnectionDraft reads the request body into a Connection. Unlike the CRUD
// path it does not require a name (the operator may test before naming), but a
// type is needed to pick the URL scheme.
func decodeConnectionDraft(r *http.Request) (*models.Connection, string, error) {
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		return nil, "", fmt.Errorf("decode request body: %w", err)
	}
	draftID, _ := body["id"].(string)
	delete(body, "id")
	data, err := json.Marshal(body)
	if err != nil {
		return nil, "", fmt.Errorf("encode connection body: %w", err)
	}
	var c models.Connection
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, "", fmt.Errorf("invalid connection: %w", err)
	}
	if c.Type == "" {
		return nil, "", fmt.Errorf("connection type is required")
	}
	return &c, strings.TrimSpace(draftID), nil
}

// mergeStoredDraftSecrets mirrors update semantics for edit-form tests. The
// connection API never returns passwords or certificates, so an unchanged edit
// draft has blank secret fields even though the stored connection has values.
func (h *connectionActionsHandler) mergeStoredDraftSecrets(draftID string, draft *models.Connection) error {
	if draftID == "" || (draft.Password != "" && draft.Certificate != "") {
		return nil
	}
	existing, err := findConnection(h.ctx.DB(), draftID)
	if err != nil {
		return fmt.Errorf("load stored connection for test: %w", err)
	}
	if draft.Password == "" {
		draft.Password = existing.Password
	}
	if draft.Certificate == "" {
		draft.Certificate = existing.Certificate
	}
	return nil
}

// maskedConnection returns the hydrated connection with secrets mid-masked so
// resolved values are visible without exposing credentials.
func maskedConnection(c *models.Connection) resolvedConnection {
	return resolvedConnection{
		Type:        c.Type,
		Namespace:   c.Namespace,
		URL:         redactConnectionURL(c.URL),
		Username:    c.Username,
		Password:    MaskValue(c.Password),
		Certificate: MaskValue(c.Certificate),
		Properties:  redactConnectionProperties(c.Properties),
	}
}

// testConnection probes the resolved URL: a TCP connect proves the host:port is
// reachable, and http/https URLs additionally report the response status.
func testConnection(ctx dbcontext.Context, c *models.Connection) testResult {
	if c.URL == "" {
		return testResult{OK: false, Message: "connection has no URL to test"}
	}
	displayURL := redactConnectionURL(c.URL)
	host, scheme, ok := dialTarget(c.URL, c.Type)
	if !ok {
		return testResult{OK: false, Message: fmt.Sprintf("cannot determine host from url %q", displayURL), URL: displayURL}
	}

	dialCtx, cancel := ctx.WithTimeout(5 * time.Second)
	defer cancel()
	allowPrivate := ctx.Properties().On(false, allowPrivateConnectionProbeProperty)
	dialAddress, err := validatedProbeAddress(dialCtx, host, allowPrivate)
	if err != nil {
		return testResult{OK: false, Message: fmt.Sprintf("TCP connect to %s rejected: %v", host, err), URL: displayURL}
	}
	conn, err := (&net.Dialer{}).DialContext(dialCtx, "tcp", dialAddress)
	if err != nil {
		return testResult{OK: false, Message: fmt.Sprintf("TCP connect to %s failed: %v", host, err), URL: displayURL}
	}
	_ = conn.Close()

	if scheme == "http" || scheme == "https" {
		return httpProbe(ctx, c, displayURL)
	}
	return testResult{OK: true, Message: fmt.Sprintf("TCP connect to %s succeeded", host), URL: displayURL}
}

// dialTarget resolves a connection URL to a host:port for the TCP reachability
// probe. It understands both URL-form DSNs (scheme://host:port/...) and the
// ADO/key-value DSNs that SQL Server accepts
// (e.g. "server=host;port=1433;database=db;..."), which url.Parse cannot handle.
// connType seeds the default port when the DSN omits one.
func dialTarget(rawURL, connType string) (hostPort, scheme string, ok bool) {
	if u, err := url.Parse(rawURL); err == nil && u.Host != "" {
		port := u.Port()
		if port == "" {
			port = defaultPort(u.Scheme)
		}
		if port == "" {
			return u.Host, u.Scheme, true
		}
		return net.JoinHostPort(u.Hostname(), port), u.Scheme, true
	}

	if host, port := parseKeyValueDSN(rawURL); host != "" {
		if port == "" {
			port = defaultPort(connType)
		}
		if port == "" {
			return host, connType, true
		}
		return net.JoinHostPort(host, port), connType, true
	}
	return "", "", false
}

// parseKeyValueDSN extracts the host and port from a semicolon-delimited
// ADO/key-value connection string. An explicit "port" key takes precedence over
// a port embedded in the server value (e.g. "server=host,1433").
func parseKeyValueDSN(dsn string) (host, port string) {
	var serverPort, explicitPort string
	for _, part := range strings.Split(dsn, ";") {
		key, val, found := strings.Cut(part, "=")
		if !found {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		val = strings.TrimSpace(val)
		switch key {
		case "server", "data source", "address", "addr", "network address":
			host, serverPort = splitServerValue(val)
		case "port":
			explicitPort = val
		}
	}
	if explicitPort != "" {
		return host, explicitPort
	}
	return host, serverPort
}

// splitServerValue parses a SQL Server "server" value, stripping an optional
// "tcp:" prefix and a "\instance" suffix, and splitting a trailing ",port".
func splitServerValue(v string) (host, port string) {
	v = strings.TrimPrefix(strings.TrimPrefix(v, "tcp:"), "np:")
	if i := strings.LastIndex(v, ","); i >= 0 {
		host, port = v[:i], strings.TrimSpace(v[i+1:])
	} else {
		host = v
	}
	if i := strings.Index(host, "\\"); i >= 0 {
		host = host[:i]
	}
	return strings.TrimSpace(host), port
}

func httpProbe(ctx dbcontext.Context, c *models.Connection, displayURL string) testResult {
	target, err := validatedHTTPProbeTarget(c.URL)
	if err != nil {
		return testResult{OK: false, Message: err.Error(), URL: displayURL}
	}
	probeConnection := *c
	if !isHTTPAuthConnectionType(probeConnection.Type) {
		probeConnection.Type = models.ConnectionTypeHTTP
	}
	httpConnection, err := dbconnection.NewHTTPConnection(ctx, probeConnection)
	if err != nil {
		return testResult{OK: false, Message: fmt.Sprintf("HTTP authentication setup failed: %v", redactError(err, c.URL, displayURL)), URL: displayURL}
	}
	client := &http.Client{
		Transport: httpConnection.TransportWithBase(newHTTPProbeTransport(ctx.Properties().On(false, allowPrivateConnectionProbeProperty))),
		Timeout:   8 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	req, err := http.NewRequest(http.MethodGet, target.url.String(), nil)
	if err != nil {
		return testResult{OK: false, Message: fmt.Sprintf("HTTP request failed: %v", err), URL: displayURL}
	}
	req.Host = target.hostHeader
	// Every HTTP and OAuth-token dial passes through the address policy above,
	// which resolves once, rejects non-public destinations by default, and dials
	// the vetted IP directly. Redirects are disabled as a second boundary.
	// codeql[go/request-forgery]
	resp, err := client.Do(req)
	if err != nil {
		return testResult{OK: false, Message: fmt.Sprintf("HTTP request failed: %v", redactError(err, c.URL, displayURL)), URL: displayURL}
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return testResult{
			OK:      false,
			Message: fmt.Sprintf("HTTP %s: authentication failed", resp.Status),
			URL:     displayURL,
		}
	}
	return testResult{
		OK:      resp.StatusCode < 500,
		Message: fmt.Sprintf("HTTP %s", resp.Status),
		URL:     displayURL,
	}
}

type httpProbeTarget struct {
	url        *url.URL
	hostHeader string
}

func validatedHTTPProbeTarget(rawURL string) (*httpProbeTarget, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("invalid HTTP URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("HTTP probe requires http or https URL")
	}
	if u.Hostname() == "" {
		return nil, fmt.Errorf("HTTP probe requires a URL host")
	}
	port := u.Port()
	if port == "" {
		port = defaultPort(u.Scheme)
	}
	if port == "" {
		return nil, fmt.Errorf("HTTP probe requires a URL port")
	}

	probeURL := *u
	probeURL.User = nil
	return &httpProbeTarget{
		url:        &probeURL,
		hostHeader: u.Host,
	}, nil
}

func newHTTPProbeTransport(allowPrivate bool) *http.Transport {
	dialer := &net.Dialer{}
	return &http.Transport{
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			dialAddress, err := validatedProbeAddress(ctx, address, allowPrivate)
			if err != nil {
				return nil, err
			}
			return dialer.DialContext(ctx, network, dialAddress)
		},
		TLSHandshakeTimeout: 5 * time.Second,
	}
}

func validatedProbeAddress(ctx context.Context, address string, allowPrivate bool) (string, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil || host == "" || port == "" {
		return "", fmt.Errorf("invalid network address")
	}

	var addresses []netip.Addr
	if ip, parseErr := netip.ParseAddr(host); parseErr == nil {
		addresses = []netip.Addr{ip}
	} else {
		addresses, err = net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		if err != nil {
			return "", fmt.Errorf("resolve host: %w", err)
		}
	}
	if len(addresses) == 0 {
		return "", fmt.Errorf("host has no IP addresses")
	}
	for _, ip := range addresses {
		if err := validateProbeIP(ip, allowPrivate); err != nil {
			return "", fmt.Errorf("host resolves to a prohibited address: %w", err)
		}
	}
	return net.JoinHostPort(addresses[0].String(), port), nil
}

func validateProbeIP(ip netip.Addr, allowPrivate bool) error {
	ip = ip.Unmap()
	if !ip.IsValid() || ip.IsUnspecified() || ip.IsMulticast() {
		return fmt.Errorf("unspecified or multicast IP")
	}
	if allowPrivate {
		return nil
	}
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return fmt.Errorf("non-public IP")
	}
	if netip.MustParsePrefix("100.64.0.0/10").Contains(ip) {
		return fmt.Errorf("shared address space")
	}
	return nil
}

func redactConnectionURL(rawURL string) string {
	if rawURL == "" {
		return ""
	}
	if u, err := url.Parse(rawURL); err == nil && u.Scheme != "" {
		redacted := *u
		redacted.User = nil
		q := redacted.Query()
		for key, vals := range q {
			if isSensitiveCredentialKey(key) {
				for i := range vals {
					vals[i] = "redacted"
				}
				q[key] = vals
			}
		}
		redacted.RawQuery = q.Encode()
		return redacted.String()
	}
	return redactKeyValueDSN(rawURL)
}

func redactKeyValueDSN(dsn string) string {
	parts := strings.Split(dsn, ";")
	for i, part := range parts {
		key, val, found := strings.Cut(part, "=")
		if !found || !isSensitiveCredentialKey(key) {
			continue
		}
		parts[i] = key + "=" + MaskValue(val)
	}
	return strings.Join(parts, ";")
}

func redactConnectionProperties(properties map[string]string) map[string]string {
	if len(properties) == 0 {
		return properties
	}
	out := make(map[string]string, len(properties))
	for key, value := range properties {
		if isSensitiveCredentialKey(key) {
			out[key] = MaskValue(value)
		} else {
			out[key] = value
		}
	}
	return out
}

func isSensitiveCredentialKey(key string) bool {
	key = strings.ToLower(strings.TrimSpace(key))
	key = strings.ReplaceAll(key, "-", "_")
	key = strings.ReplaceAll(key, " ", "_")
	switch key {
	case "password", "pwd", "pass", "passwd", "secret", "token", "bearer",
		"access_token", "refresh_token", "api_key", "apikey", "client_secret",
		"user", "username", "user_id", "userid", "uid":
		return true
	default:
		return false
	}
}

func redactError(err error, rawURL, displayURL string) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s", strings.ReplaceAll(err.Error(), rawURL, displayURL))
}

// defaultPort maps a URL scheme to its conventional port for the reachability
// probe when the URL omits one.
func defaultPort(scheme string) string {
	switch scheme {
	case "http":
		return "80"
	case "https":
		return "443"
	case "postgres", "postgresql":
		return "5432"
	case "mysql":
		return "3306"
	case "sqlserver", "sql_server", "mssql":
		return "1433"
	case "clickhouse":
		return "9000"
	case "mongodb":
		return "27017"
	case "redis":
		return "6379"
	default:
		return ""
	}
}
