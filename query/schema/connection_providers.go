package schema

import (
	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/types"
)

// Provider config structs describe the connection form per backend. Field types
// and clicky:"..." tags select the widget: an EnvVar field renders as a string
// (its value round-trips as secret://name/key or a literal); `type=` picks the
// k8s secret/URL picker; `property=` routes the field into the connection's
// Properties map instead of a top-level column; `order=` sets the render order.
// Only name/namespace/properties are universal (the base form); everything else
// lives here, so a connection only shows the fields it uses. Connection types
// without an entry in tailoredProviders fall back to the base fields.

// secretCreds is the shared username/password pair rendered with the secret picker.
type secretCreds struct {
	Username types.EnvVar `json:"username" clicky:"type=k8s-secret-selector,title=Username,source=value,order=4"`
	Password types.EnvVar `json:"password" clicky:"type=k8s-secret-selector,title=Password,format=password,source=secret,order=5"`
}

// httpConnection contains the transport fields shared by HTTP-family backends.
// Authentication is added as a conditional nested schema by tailoredBranch so
// the form can switch between None, Basic, OAuth and mTLS without showing every
// credential field at once.
type httpConnection struct {
	URL         types.EnvVar `json:"url"          clicky:"type=k8s-url-selector,title=URL,source=value,required,order=2,desc=Base URL of the HTTP endpoint"`
	InsecureTLS bool         `json:"insecure_tls" clicky:"title=Insecure TLS,order=3"`
}

// HTTPProvider models a generic HTTP endpoint.
type HTTPProvider struct{ httpConnection }

// OpenSearchProvider extends the HTTP form for an OpenSearch endpoint.
type OpenSearchProvider struct{ httpConnection }

// OpenTelemetryProvider delegates trace storage to a nested OpenSearch
// connection while retaining its own first-class connection identity.
type OpenTelemetryProvider struct {
	Connection string `json:"connection" clicky:"property=connection,title=OpenSearch Connection,required,order=2"`
}

// PrometheusProvider extends the HTTP form for a Prometheus endpoint.
type PrometheusProvider struct{ httpConnection }

// LokiProvider extends the HTTP form for a Loki endpoint.
type LokiProvider struct{ httpConnection }

// JaegerProvider extends the HTTP form for a Jaeger query endpoint.
type JaegerProvider struct{ httpConnection }

// PostgresProvider models a postgres SQL DSN connection.
type PostgresProvider struct {
	URL types.EnvVar `json:"url" clicky:"type=k8s-url-selector,title=URL,source=value,required,order=2,desc=postgres://user:pass@host:5432/db?sslmode=disable"`
	secretCreds
}

// MySQLProvider models a MySQL DSN connection.
type MySQLProvider struct {
	URL types.EnvVar `json:"url" clicky:"type=k8s-url-selector,title=URL,source=value,required,order=2,desc=user:pass@tcp(host:3306)/db"`
	secretCreds
}

// SQLServerProvider models a SQL Server DSN connection.
type SQLServerProvider struct {
	URL types.EnvVar `json:"url" clicky:"type=k8s-url-selector,title=URL,source=value,required,order=2,desc=sqlserver://user:pass@host:1433?database=db"`
	secretCreds
}

// ClickHouseProvider models a ClickHouse DSN connection.
type ClickHouseProvider struct {
	URL types.EnvVar `json:"url" clicky:"type=k8s-url-selector,title=URL,source=value,required,order=2,desc=clickhouse://user:pass@host:9000/db"`
	secretCreds
}

// RedisProvider models a Redis/Valkey endpoint. A URL may carry the database
// number and credentials; explicit username/password fields override URI auth
// after hydration when the browser creates its client.
type RedisProvider struct {
	URL types.EnvVar `json:"url" clicky:"type=k8s-url-selector,title=URL,source=value,required,order=2,desc=redis://host:6379/0"`
	secretCreds
}

// KubernetesProvider models a Kubernetes cluster connection: an optional
// kubeconfig (in-cluster config is used when empty).
type KubernetesProvider struct {
	Certificate types.EnvVar `json:"certificate" clicky:"type=k8s-secret-selector,title=Kubeconfig,source=secret,order=6"`
}

// GCPProvider models a Google Cloud connection: optional endpoint + the required
// service account JSON.
type GCPProvider struct {
	URL         types.EnvVar `json:"url"         clicky:"type=k8s-url-selector,title=Endpoint,source=value,order=2"`
	Certificate types.EnvVar `json:"certificate" clicky:"type=k8s-secret-selector,title=Service Account JSON,source=secret,required,order=6"`
}

// GCSProvider models a Google Cloud Storage connection.
type GCSProvider struct {
	URL         types.EnvVar `json:"url"         clicky:"type=k8s-url-selector,title=Endpoint,source=value,order=2"`
	Certificate types.EnvVar `json:"certificate" clicky:"type=k8s-secret-selector,title=Service Account JSON,source=secret,required,order=6"`
}

// GCPKMSProvider models a Google Cloud KMS connection.
type GCPKMSProvider struct {
	URL         types.EnvVar `json:"url"         clicky:"type=k8s-url-selector,title=Endpoint,source=value,order=2"`
	Certificate types.EnvVar `json:"certificate" clicky:"type=k8s-secret-selector,title=Service Account JSON,source=secret,required,order=6"`
}

// GitProvider models a Git repository connection: URL + basic-auth, or an SSH
// private key.
type GitProvider struct {
	URL types.EnvVar `json:"url" clicky:"type=k8s-url-selector,title=URL,source=value,required,order=2"`
	secretCreds
	Certificate types.EnvVar `json:"certificate" clicky:"type=k8s-secret-selector,title=SSH Private Key,source=secret,order=6"`
}

// tailoredProviders binds each connection type to the struct that drives its
// form. Types without an entry fall back to the base fields (name/namespace/
// properties). Covers the query-provider backends plus the form-shaped backends
// (HTTP-family, SQL, cert/cloud). Kept in sync with allConnectionTypes by the
// drift-guard test.
var tailoredProviders = map[string]any{
	models.ConnectionTypeHTTP:          HTTPProvider{},
	models.ConnectionTypeOpenSearch:    OpenSearchProvider{},
	models.ConnectionTypeOpenTelemetry: OpenTelemetryProvider{},
	models.ConnectionTypePrometheus:    PrometheusProvider{},
	models.ConnectionTypeLoki:          LokiProvider{},
	models.ConnectionTypeJaeger:        JaegerProvider{},
	models.ConnectionTypePostgres:      PostgresProvider{},
	models.ConnectionTypeMySQL:         MySQLProvider{},
	models.ConnectionTypeSQLServer:     SQLServerProvider{},
	models.ConnectionTypeClickHouse:    ClickHouseProvider{},
	models.ConnectionTypeRedis:         RedisProvider{},
	models.ConnectionTypeKubernetes:    KubernetesProvider{},
	models.ConnectionTypeGCP:           GCPProvider{},
	models.ConnectionTypeGCS:           GCSProvider{},
	models.ConnectionTypeGCPKMS:        GCPKMSProvider{},
	models.ConnectionTypeGit:           GitProvider{},
}

// TailoredProviderTypes returns the set of connection types that get a tailored
// if/then branch. Exposed for the drift guard test.
func TailoredProviderTypes() map[string]struct{} {
	set := make(map[string]struct{}, len(tailoredProviders))
	for typ := range tailoredProviders {
		set[typ] = struct{}{}
	}
	return set
}
