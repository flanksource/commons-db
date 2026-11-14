package connections

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"slices"
	"strings"

	"github.com/flanksource/clicky/entity"
	dbconnection "github.com/flanksource/commons-db/connection"
	"github.com/flanksource/commons-db/models"
	"gorm.io/gorm"
)

// proxyHandler serves GET|HEAD {prefix}/connections/{name}/proxy/{path...}: a
// read-only reverse proxy to an http connection's URL that injects the
// connection's own credentials, so they never reach the caller.
func (s *Service) proxyHandler(prefix string, next http.Handler) http.Handler {
	base := prefix + "/connections/"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rest, matched := strings.CutPrefix(r.URL.EscapedPath(), base)
		name, tail, ok := proxyRoute(rest)
		if !matched || !ok {
			next.ServeHTTP(w, r)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			entity.WriteError(w, entity.NewStatusErrorf(http.StatusMethodNotAllowed, "method_not_allowed",
				"connection proxy is read-only: %s is not allowed", r.Method))
			return
		}
		if err := s.serveProxy(w, r, name, tail); err != nil {
			entity.WriteError(w, err)
		}
	})
}

// proxyRoute splits "{name}/proxy[/{path...}]" into the unescaped name and the
// still-escaped upstream path.
func proxyRoute(rest string) (string, string, bool) {
	escapedName, tail, ok := strings.Cut(rest, "/proxy")
	if !ok || escapedName == "" || strings.Contains(escapedName, "/") || tail != "" && !strings.HasPrefix(tail, "/") {
		return "", "", false
	}
	name, err := url.PathUnescape(escapedName)
	if err != nil {
		return "", "", false
	}
	return name, tail, true
}

func (s *Service) serveProxy(w http.ResponseWriter, r *http.Request, name, tail string) error {
	raw, err := s.proxyConnection(name)
	if err != nil {
		return err
	}
	unescaped, err := url.PathUnescape(tail)
	if err != nil || slices.Contains(strings.Split(unescaped, "/"), "..") {
		return entity.NewStatusErrorf(http.StatusBadRequest, "invalid_path", "connection %q proxy path %q is invalid", name, tail)
	}
	conn, err := dbconnection.NewHTTPConnection(s.context().Wrap(r.Context()), *raw)
	if err != nil {
		return fmt.Errorf("resolve connection %q: %s", name, sanitizeConnectionError(err, raw))
	}
	resolved := cloneConnection(raw)
	resolved.URL, resolved.Password = conn.URL, conn.HTTPBasicAuth.GetPassword()
	target, err := proxyTarget(conn.URL, tail, r.URL.RawQuery)
	if err != nil {
		return fmt.Errorf("connection %q: %s", name, sanitizeConnectionError(err, raw, resolved))
	}
	proxy := &httputil.ReverseProxy{
		Transport: conn.Transport(),
		Rewrite: func(out *httputil.ProxyRequest) {
			out.Out.URL, out.Out.Host = target, target.Host
			out.Out.Header.Del("Cookie")
			out.Out.Header.Del("Authorization")
		},
		// The upstream session belongs to the connection, not the caller's origin.
		ModifyResponse: func(response *http.Response) error {
			response.Header.Del("Set-Cookie")
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			http.Error(w, fmt.Sprintf("proxy connection %q: %s", raw.Name, sanitizeConnectionError(err, raw, resolved)), http.StatusBadGateway)
		},
	}
	proxy.ServeHTTP(w, r)
	return nil
}

// proxyConnection resolves name the way every connection surface does and
// refuses one the service's policy hides (404) or that is not http (400).
func (s *Service) proxyConnection(name string) (*models.Connection, error) {
	raw, err := s.findConnection(name)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, entity.NewStatusErrorf(http.StatusNotFound, "not_found", "connection %q not found", name)
	}
	if err != nil {
		return nil, err
	}
	if _, visible := s.policy.scope(ListOptions{Type: raw.Type}); !visible {
		return nil, entity.NewStatusErrorf(http.StatusNotFound, "not_found", "connection %q not found", name)
	}
	if raw.Type != models.ConnectionTypeHTTP {
		return nil, entity.NewStatusErrorf(http.StatusBadRequest, "invalid_type",
			"connection %q has type %q; only %s connections can be proxied", name, raw.Type, models.ConnectionTypeHTTP)
	}
	return raw, nil
}

// proxyTarget joins the hydrated connection URL with the escaped request tail
// and the request's query.
func proxyTarget(connectionURL, tail, rawQuery string) (*url.URL, error) {
	target, err := url.Parse(connectionURL)
	if err != nil {
		return nil, fmt.Errorf("invalid URL: %w", err)
	}
	if target.Scheme == "" || target.Host == "" {
		return nil, fmt.Errorf("URL needs a scheme and host to be proxied")
	}
	escapedBase := strings.TrimSuffix(target.EscapedPath(), "/")
	path, err := url.PathUnescape(escapedBase + tail)
	if err != nil {
		return nil, fmt.Errorf("invalid URL path: %w", err)
	}
	target.Path, target.RawPath = path, escapedBase+tail
	target.RawQuery = strings.Trim(target.RawQuery+"&"+rawQuery, "&")
	return target, nil
}
