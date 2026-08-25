package connections

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/flanksource/clicky/rpc"
	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/query"
)

const (
	connectionDashboardPath = "/api/v1/connections/dashboard"
	connectionHealthPath    = "/api/v1/connections/health"
)

type ProfileProvider func(context.Context) ([]query.Profile, error)

type connectionDashboardHandlerOptions struct {
	Prefix   string
	Context  dbcontext.Context
	Profiles ProfileProvider
	Next     http.Handler
}

type connectionDashboardHandler struct {
	path     string
	ctx      dbcontext.Context
	profiles ProfileProvider
	next     http.Handler
}

type connectionDashboardResponse struct {
	Connections []connectionDashboardItem `json:"connections"`
	GeneratedAt time.Time                 `json:"generatedAt"`
}

type connectionDashboardItem struct {
	ID               string                    `json:"id"`
	Name             string                    `json:"name"`
	Namespace        string                    `json:"namespace"`
	Type             string                    `json:"type"`
	Endpoint         *dashboardEndpoint        `json:"endpoint,omitempty"`
	SecretCount      int                       `json:"secretCount"`
	InlineCredential bool                      `json:"inlineCredential"`
	InsecureTLS      bool                      `json:"insecureTLS"`
	// Health is present only when a probe result is already cached for this
	// exact row. Listing never probes — see POST /connections/health.
	Health       *connectionDashboardHealth `json:"health,omitempty"`
	ProfileCount int                        `json:"profileCount"`
	UpdatedAt    time.Time                  `json:"updatedAt"`
}

type dashboardEndpoint struct {
	Scheme string `json:"scheme"`
	Host   string `json:"host"`
	Path   string `json:"path,omitempty"`
}

type connectionDashboardHealth struct {
	State     connectionHealthState `json:"state"`
	Detail    string                `json:"detail"`
	CheckedAt time.Time             `json:"checkedAt"`
	Cached    bool                  `json:"cached"`
}

func newConnectionDashboardHandler(options connectionDashboardHandlerOptions) http.Handler {
	return &connectionDashboardHandler{
		path:     strings.TrimRight(options.Prefix, "/") + "/connections/dashboard",
		ctx:      options.Context,
		profiles: options.Profiles,
		next:     options.Next,
	}
}

func (h *connectionDashboardHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet || r.URL.Path != h.path {
		h.next.ServeHTTP(w, r)
		return
	}
	if h.profiles == nil {
		http.Error(w, "connection dashboard profile provider is not configured", http.StatusInternalServerError)
		return
	}

	profiles, err := h.profiles(r.Context())
	if err != nil {
		http.Error(w, fmt.Sprintf("list dashboard profiles: %v", err), http.StatusInternalServerError)
		return
	}
	connections, err := h.listConnections(r.Context(), ListOptions{
		Type: r.URL.Query().Get("type"), Types: r.URL.Query().Get("types"),
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, connectionDashboardResponse{
		Connections: dashboardItems(connections, profileUsageCounts(profiles)),
		GeneratedAt: time.Now().UTC(),
	})
}

func (h *connectionDashboardHandler) listConnections(ctx context.Context, options ListOptions) ([]*models.Connection, error) {
	query := h.ctx.DB().WithContext(ctx).Model(&models.Connection{})
	if types := connectionTypeFilter(options); len(types) > 0 {
		query = query.Where("type IN ?", types)
	}
	var connections []*models.Connection
	if err := query.Order("name").Find(&connections).Error; err != nil {
		return nil, fmt.Errorf("list dashboard connections: %w", err)
	}
	return connections, nil
}

// dashboardItems builds the inventory from the DB rows alone. It performs no
// network I/O and no secret hydration: health is attached only where a probe
// already ran, so listing a fleet of unreachable connections is as fast as
// listing a healthy one.
func dashboardItems(connections []*models.Connection, usage map[string]int) []connectionDashboardItem {
	items := make([]connectionDashboardItem, len(connections))
	for index, connection := range connections {
		items[index] = connectionDashboardItem{
			ID: connection.ID.String(), Name: connection.Name, Namespace: connection.Namespace,
			Type: connection.Type, Endpoint: dashboardEndpointFor(connection),
			SecretCount:      dashboardSecretCount(connection),
			InlineCredential: hasInlineConnectionCredential(connection.URL),
			InsecureTLS:      connection.InsecureTLS,
			ProfileCount:     dashboardProfileCount(connection, usage), UpdatedAt: connection.UpdatedAt,
		}
		if cached, ok := cachedConnectionHealth(connection.ID.String(), connection.UpdatedAt); ok {
			items[index].Health = healthSummary(cached)
		}
	}
	return items
}

func dashboardEndpointFor(connection *models.Connection) *dashboardEndpoint {
	raw := strings.TrimSpace(connection.URL)
	if raw == "" {
		raw = strings.TrimSpace(connection.Properties["connection"])
	}
	if raw == "" {
		return nil
	}
	if strings.Contains(raw, "=") && !strings.Contains(raw, "://") {
		host, port := parseKeyValueDSN(raw)
		if host == "" {
			return nil
		}
		if port != "" {
			host += ":" + port
		}
		path := keyValueDSNField(raw, "database", "initial catalog")
		if path != "" {
			path = "/" + path
		}
		return &dashboardEndpoint{Scheme: "sqlserver", Host: host, Path: path}
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil
	}
	return &dashboardEndpoint{Scheme: strings.ToLower(parsed.Scheme), Host: parsed.Host, Path: parsed.Path}
}

func keyValueDSNField(raw string, keys ...string) string {
	for _, part := range strings.Split(raw, ";") {
		key, value, found := strings.Cut(part, "=")
		if !found {
			continue
		}
		for _, candidate := range keys {
			if strings.EqualFold(strings.TrimSpace(key), candidate) {
				return strings.TrimSpace(value)
			}
		}
	}
	return ""
}

func dashboardSecretCount(connection *models.Connection) int {
	values := []string{connection.URL, connection.Username, connection.Password, connection.Certificate}
	for _, value := range connection.Properties {
		values = append(values, value)
	}
	count := 0
	for _, value := range values {
		if strings.HasPrefix(strings.TrimSpace(value), "secret://") {
			count++
		}
	}
	return count
}

func hasInlineConnectionCredential(raw string) bool {
	if parsed, err := url.Parse(raw); err == nil && parsed.Scheme != "" {
		if parsed.User != nil {
			if _, present := parsed.User.Password(); present {
				return true
			}
		}
		for key, values := range parsed.Query() {
			if isPasswordCredentialKey(key) && len(values) > 0 && values[0] != "" {
				return true
			}
		}
	}
	for _, part := range strings.Split(raw, ";") {
		key, value, found := strings.Cut(part, "=")
		if found && isPasswordCredentialKey(key) && strings.TrimSpace(value) != "" {
			return true
		}
	}
	return false
}

func isPasswordCredentialKey(key string) bool {
	key = strings.ToLower(strings.TrimSpace(key))
	switch key {
	case "password", "pwd", "pass", "passwd":
		return true
	default:
		return false
	}
}

func profileUsageCounts(profiles []query.Profile) map[string]int {
	counts := map[string]int{}
	for _, profile := range profiles {
		parts, found := strings.CutPrefix(strings.TrimSpace(profile.Provider.Connection), "connection://")
		if !found {
			continue
		}
		segments := strings.Split(parts, "/")
		for index := range segments {
			segments[index] = strings.TrimSpace(segments[index])
		}
		switch len(segments) {
		case 1:
			if segments[0] != "" {
				counts[segments[0]]++
			}
		case 2:
			if segments[0] != "" && segments[1] != "" {
				counts[segments[0]+"/"+segments[1]]++
			}
		case 3:
			if segments[1] != "" && segments[2] != "" {
				counts[segments[1]+"/"+segments[2]]++
			}
		}
	}
	return counts
}

func dashboardProfileCount(connection *models.Connection, counts map[string]int) int {
	count := counts[connection.Name]
	if connection.Namespace != "" {
		count += counts[connection.Namespace+"/"+connection.Name]
	}
	return count
}

// AddConnectionsOpenAPI documents the inventory read and its opt-in health
// trigger without publishing either as an explorer surface.
func AddConnectionsOpenAPI(spec *rpc.OpenAPISpec) {
	if spec.Paths == nil {
		spec.Paths = map[string]rpc.OpenAPIPath{}
	}
	item := &rpc.OpenAPISchema{Type: "object", Properties: map[string]*rpc.OpenAPISchema{
		"id": {Type: "string"}, "name": {Type: "string"}, "namespace": {Type: "string"},
		"type": {Type: "string"}, "endpoint": {Type: "object"}, "secretCount": {Type: "integer"},
		"inlineCredential": {Type: "boolean"}, "insecureTLS": {Type: "boolean"},
		"health": {Type: "object"}, "profileCount": {Type: "integer"},
		"updatedAt": {Type: "string", Format: "date-time"},
	}}
	spec.Paths[connectionDashboardPath] = rpc.OpenAPIPath{"get": {
		Tags: []string{"Connections"}, Summary: "List connection inventory",
		Description: "Returns the profile-aware connection fleet for the namespace-lane dashboard. " +
			"Reads the database only — health is attached only where a prior check is still cached.",
		OperationID: "listConnectionInventory",
		Parameters: []rpc.OpenAPIParameter{
			{Name: "type", In: "query", Schema: &rpc.OpenAPISchema{Type: "string"}},
			{Name: "types", In: "query", Schema: &rpc.OpenAPISchema{Type: "string"}},
		},
		Responses: map[string]rpc.OpenAPIResponse{"200": {
			Description: "Connection inventory",
			Content: map[string]rpc.OpenAPIMediaType{"application/json": {Schema: &rpc.OpenAPISchema{
				Type: "object", Properties: map[string]*rpc.OpenAPISchema{
					"connections": {Type: "array", Items: item},
					"generatedAt": {Type: "string", Format: "date-time"},
				},
			}}},
		}},
	}}
	spec.Paths[connectionHealthPath] = rpc.OpenAPIPath{"post": {
		Tags: []string{"Connections"}, Summary: "Check connection health",
		Description: "Probes the named connections on demand and caches the outcome. " +
			"A slow or failing connection yields an individual result, never a failed request.",
		OperationID: "checkConnectionHealth",
		RequestBody: &rpc.OpenAPIRequestBody{
			Required: true,
			Content: map[string]rpc.OpenAPIMediaType{"application/json": {Schema: &rpc.OpenAPISchema{
				Type: "object", Properties: map[string]*rpc.OpenAPISchema{
					"ids":   {Type: "array", Items: &rpc.OpenAPISchema{Type: "string"}},
					"force": {Type: "boolean"},
				},
			}}},
		},
		Responses: map[string]rpc.OpenAPIResponse{"200": {
			Description: "Connection health results",
			Content: map[string]rpc.OpenAPIMediaType{"application/json": {Schema: &rpc.OpenAPISchema{
				Type: "object", Properties: map[string]*rpc.OpenAPISchema{
					"results": {Type: "array", Items: &rpc.OpenAPISchema{
						Type: "object", Properties: map[string]*rpc.OpenAPISchema{
							"id": {Type: "string"}, "state": {Type: "string"}, "detail": {Type: "string"},
							"checkedAt": {Type: "string", Format: "date-time"},
							"durationMs": {Type: "integer"}, "cached": {Type: "boolean"},
						},
					}},
					"generatedAt": {Type: "string", Format: "date-time"},
				},
			}}},
		}},
	}}
}
