package profiles

import (
	"net/http"
	"strings"
)

const SchemaContentType = "application/schema+json"

func WantsSchema(r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	if r.URL.Query().Has("__schema") {
		return true
	}
	for _, part := range strings.Split(r.Header.Get("Accept"), ",") {
		mediaType := strings.ToLower(strings.TrimSpace(strings.SplitN(part, ";", 2)[0]))
		if mediaType == SchemaContentType || mediaType == "application/json+schema" {
			return true
		}
	}
	return false
}

func wantsSchema(r *http.Request) bool { return WantsSchema(r) }
