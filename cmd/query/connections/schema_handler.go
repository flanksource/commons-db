package connections

import (
	"net/http"
	"strings"

	"github.com/flanksource/clicky/entity"
	"github.com/flanksource/commons-db/cmd/query/profiles"
	"github.com/flanksource/commons-db/query/schema"
)

// newConnectionSchemaHandler answers a schema request (`Accept:
// application/schema+json` or `?__schema`) on {prefix}/connection[s] with the
// connection form schema, so every host that mounts Service.Handler serves the
// form without re-implementing it. Every other request goes to next.
func newConnectionSchemaHandler(prefix string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimSuffix(r.URL.Path, "/")
		if !profiles.WantsSchema(r) || (path != prefix+"/connection" && path != prefix+"/connections") {
			next.ServeHTTP(w, r)
			return
		}
		body, err := schema.JSON(schema.Connection())
		if err != nil {
			entity.WriteError(w, entity.NewStatusErrorf(http.StatusInternalServerError, "schema_failed",
				"encode the connection schema: %v", err))
			return
		}
		w.Header().Set("Content-Type", profiles.SchemaContentType)
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.WriteHeader(http.StatusOK)
		if r.Method != http.MethodHead {
			_, _ = w.Write(body)
		}
	})
}
