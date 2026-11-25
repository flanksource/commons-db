package aws

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/url"
	"strings"

	commonshttp "github.com/flanksource/commons/http"
)

// call describes one HTTP exchange with an AWS service.
type call struct {
	Operation string
	Method    string
	// BaseURL replaces the service's base URL, e.g. for S3 virtual-host addressing.
	BaseURL string
	// Path is the already-escaped request path, starting with "/".
	Path   string
	Query  url.Values
	Header map[string]string
	Body   any
}

// send runs c and returns the 2xx response with its body open; the caller closes
// it. Any other status becomes an *APIError.
func (s *service) send(ctx context.Context, c call) (*commonshttp.Response, error) {
	if s.err != nil {
		return nil, fmt.Errorf("aws %s %s: %w", s.name, c.Operation, s.err)
	}
	req := s.client.R(ctx)
	if c.Body != nil {
		if err := req.Body(c.Body); err != nil {
			return nil, fmt.Errorf("aws %s %s: setting request body: %w", s.name, c.Operation, err)
		}
	}
	for k, v := range c.Header {
		req = req.Header(k, v)
	}
	for k, values := range c.Query {
		for _, v := range values {
			req = req.QueryParamAdd(k, v)
		}
	}
	base := s.baseURL
	if c.BaseURL != "" {
		base = c.BaseURL
	}
	resp, err := req.Do(c.Method, base+c.Path)
	if err != nil {
		return nil, fmt.Errorf("aws %s %s: %w", s.name, c.Operation, err)
	}
	if !resp.IsOK() {
		defer resp.Body.Close()
		body, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			return nil, fmt.Errorf("aws %s %s: HTTP %d, reading error body: %w", s.name, c.Operation, resp.StatusCode, readErr)
		}
		return nil, newAPIError(s.name, c.Operation, resp.Response, body)
	}
	return resp, nil
}

// do sends c with an optional request body and decodes a JSON or XML response
// into out (nil discards the body).
func (s *service) do(ctx context.Context, c call, out any, decode func(io.Reader, any) error) error {
	resp, err := s.send(ctx, c)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if out == nil {
		_, err = io.Copy(io.Discard, resp.Body)
		return err
	}
	if err := decode(resp.Body, out); err != nil {
		return fmt.Errorf("aws %s %s: decoding response: %w", s.name, c.Operation, err)
	}
	return nil
}

func decodeJSON(r io.Reader, out any) error { return json.NewDecoder(r).Decode(out) }

func decodeXML(r io.Reader, out any) error { return xml.NewDecoder(r).Decode(out) }

// jsonRPC calls an AWS JSON protocol operation (X-Amz-Target); version is the
// protocol version, "1.0" or "1.1".
func (s *service) jsonRPC(ctx context.Context, protocol jsonProtocol, operation string, in, out any) error {
	return s.do(ctx, call{
		Operation: operation,
		Method:    "POST",
		Path:      "/",
		Header: map[string]string{
			"Content-Type": "application/x-amz-json-" + protocol.Version,
			"X-Amz-Target": protocol.TargetPrefix + "." + operation,
		},
		Body: in,
	}, out, decodeJSON)
}

type jsonProtocol struct{ Version, TargetPrefix string }

// queryCall calls an AWS query protocol operation: a form POST answered with XML.
func (s *service) queryCall(ctx context.Context, apiVersion, operation string, params url.Values, out any) error {
	form := url.Values{"Action": {operation}, "Version": {apiVersion}}
	for k, v := range params {
		form[k] = v
	}
	return s.do(ctx, call{
		Operation: operation,
		Method:    "POST",
		Path:      "/",
		Header:    map[string]string{"Content-Type": "application/x-www-form-urlencoded; charset=utf-8"},
		Body:      form.Encode(),
	}, out, decodeXML)
}

// escapePath encodes a request path with AWS UriEncode: every byte except
// A-Za-z0-9-._~ becomes %XX (uppercase) and "/" separates segments. url.URL's
// own escaping leaves characters such as = : + , ; @ $ & bare, so a server that
// canonicalizes with UriEncode would compute a different signature. The result
// is parsed into URL.RawPath, so it is both the wire path and the signed one.
func escapePath(parts ...string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	b.WriteByte('/')
	path := strings.Join(parts, "/")
	for i := 0; i < len(path); i++ {
		switch c := path[i]; {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '.', c == '_', c == '~', c == '/':
			b.WriteByte(c)
		default:
			b.WriteByte('%')
			b.WriteByte(hex[c>>4])
			b.WriteByte(hex[c&15])
		}
	}
	return b.String()
}
