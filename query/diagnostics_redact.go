package query

import (
	"encoding/json"
	"net/url"
	"strings"
)

// SanitizeDiagnosticValues blanks the values whose key names a credential, so a
// caller reporting its own map beside a provider's diagnostics holds both to the
// same rule rather than re-deriving it.
func SanitizeDiagnosticValues(values map[string]any) map[string]any {
	return sanitizeDiagnosticMap(values)
}

func sanitizeDiagnosticMap(values map[string]any) map[string]any {
	if len(values) == 0 {
		return nil
	}
	clean := make(map[string]any, len(values))
	for key, value := range values {
		if diagnosticSecretKey(key) {
			clean[key] = "********"
			continue
		}
		if text, ok := value.(string); ok && diagnosticURLKey(key) {
			clean[key] = redactDiagnosticURL(text)
			continue
		}
		clean[key] = sanitizeDiagnosticValue(value)
	}
	return clean
}

func sanitizeDiagnosticValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return sanitizeDiagnosticMap(typed)
	case []any:
		return cloneDiagnosticValues(typed)
	case []map[string]any:
		items := make([]map[string]any, 0, len(typed))
		for _, item := range typed {
			items = append(items, sanitizeDiagnosticMap(item))
		}
		return items
	default:
		return value
	}
}

func cloneDiagnosticValues(values []any) []any {
	if len(values) == 0 {
		return nil
	}
	cloned := make([]any, 0, len(values))
	for _, value := range values {
		cloned = append(cloned, sanitizeDiagnosticValue(value))
	}
	return cloned
}

func diagnosticSecretKey(key string) bool {
	normalized := strings.ToLower(strings.ReplaceAll(key, "-", "_"))
	for _, token := range []string{
		"password", "passwd", "secret", "token", "authorization", "cookie",
		"api_key", "apikey", "client_secret", "access_key", "private_key",
	} {
		if normalized == token || strings.HasSuffix(normalized, "_"+token) {
			return true
		}
	}
	return false
}

// diagnosticURLKey names an option that holds a connection string. Its key says
// nothing about a credential, but its value routinely carries one.
func diagnosticURLKey(key string) bool {
	switch strings.ToLower(strings.ReplaceAll(key, "-", "_")) {
	case "url", "uri", "dsn", "address", "endpoint", "connection", "connection_string":
		return true
	default:
		return false
	}
}

// redactDiagnosticURL strips the credentials out of a connection string while
// leaving the part an operator reads it for — which host, which database.
//
// Both shapes a DSN comes in are handled, because both appear in provider
// options: a URL whose userinfo and query carry the secret, and a key=value
// string where a `password=` field does.
func redactDiagnosticURL(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return raw
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" {
		return redactDiagnosticDSN(raw)
	}
	if parsed.User != nil {
		if name := parsed.User.Username(); name != "" {
			parsed.User = url.User(name)
		} else {
			parsed.User = nil
		}
	}
	values := parsed.Query()
	for key := range values {
		if diagnosticSecretKey(key) {
			values.Set(key, "********")
		}
	}
	if len(values) > 0 {
		parsed.RawQuery = values.Encode()
	}
	return parsed.String()
}

// redactDiagnosticDSN rewrites the secret-named fields of a key=value DSN and
// nothing else — separators included, so what is left reads as the original
// string with holes in it rather than as a reformatted one.
func redactDiagnosticDSN(raw string) string {
	var out strings.Builder
	start := 0
	for i := 0; i <= len(raw); i++ {
		if i < len(raw) && raw[i] != ';' && raw[i] != ' ' {
			continue
		}
		segment := raw[start:i]
		if key, _, found := strings.Cut(segment, "="); found && diagnosticSecretKey(key) {
			out.WriteString(key + "=********")
		} else {
			out.WriteString(segment)
		}
		if i < len(raw) {
			out.WriteByte(raw[i])
		}
		start = i + 1
	}
	return out.String()
}

func MarshalDiagnosticPreview(value any) []byte {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return []byte(`{"error":"diagnostic preview could not be encoded"}`)
	}
	return encoded
}
