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
	"golang.org/x/sync/errgroup"
)

const connectionDashboardPath = "/api/v1/connections/dashboard"

const dashboardProbeConcurrency = 6

type ProfileProvider func(context.Context) ([]query.Profile, error)

type connectionHealthState string

const (
	connectionHealthHealthy      connectionHealthState = "healthy"
	connectionHealthCredentials  connectionHealthState = "credentials"
	connectionHealthUnreachable  connectionHealthState = "unreachable"
	connectionHealthUnverifiable connectionHealthState = "unverifiable"
)

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
	Health           connectionDashboardHealth `json:"health"`
	ProfileCount     int                       `json:"profileCount"`
	UpdatedAt        time.Time                 `json:"updatedAt"`
}

type dashboardEndpoint struct {
	Scheme string `json:"scheme"`
	Host   string `json:"host"`
	Path   string `json:"path,omitempty"`
}

type connectionDashboardHealth struct {
	State  connectionHealthState `json:"state"`
	Detail string                `json:"detail"`
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
	items, err := h.dashboardItems(r.Context(), connections, profileUsageCounts(profiles))
	if err != nil {
		http.Error(w, fmt.Sprintf("build connection dashboard: %v", err), http.StatusRequestTimeout)
		return
	}

	writeJSON(w, connectionDashboardResponse{Connections: items, GeneratedAt: time.Now().UTC()})
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

func (h *connectionDashboardHandler) dashboardItems(
	ctx context.Context,
	connections []*models.Connection,
	usage map[string]int,
) ([]connectionDashboardItem, error) {
	items := make([]connectionDashboardItem, len(connections))
	group, groupContext := errgroup.WithContext(ctx)
	group.SetLimit(dashboardProbeConcurrency)
	for index, connection := range connections {
		index, connection := index, connection
		group.Go(func() error {
			health, err := dashboardHealth(groupContext, h.ctx, connection)
			if err != nil {
				return err
			}
			items[index] = connectionDashboardItem{
				ID: connection.ID.String(), Name: connection.Name, Namespace: connection.Namespace,
				Type: connection.Type, Endpoint: dashboardEndpointFor(connection),
				SecretCount:      dashboardSecretCount(connection),
				InlineCredential: hasInlineConnectionCredential(connection.URL),
				InsecureTLS:      connection.InsecureTLS, Health: health,
				ProfileCount: dashboardProfileCount(connection, usage), UpdatedAt: connection.UpdatedAt,
			}
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		return nil, err
	}
	return items, nil
}

func dashboardHealth(
	ctx context.Context,
	connectionContext dbcontext.Context,
	connection *models.Connection,
) (connectionDashboardHealth, error) {
	resolved := cloneConnection(connection)
	if _, err := dbcontext.HydrateConnection(connectionContext, resolved); err != nil {
		return connectionDashboardHealth{
			State: connectionHealthCredentials, Detail: sanitizeConnectionError(err, connection, resolved),
		}, nil
	}

	probeContext, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	server := discoverServer(probeContext, connectionContext, resolved)
	if ctx.Err() != nil {
		return connectionDashboardHealth{}, ctx.Err()
	}
	if server.Status == "error" {
		return connectionDashboardHealth{
			State:  connectionHealthUnreachable,
			Detail: sanitizeConnectionError(fmt.Errorf("%s", server.Message), connection, resolved),
		}, nil
	}
	if server.Status == "unavailable" {
		return connectionDashboardHealth{
			State: connectionHealthUnverifiable, Detail: "No version discovery for this connection type",
		}, nil
	}
	detail := strings.TrimSpace(strings.Join([]string{server.Product, server.Version}, " "))
	if detail == "" {
		detail = "Available"
	}
	return connectionDashboardHealth{State: connectionHealthHealthy, Detail: detail}, nil
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

// AddDashboardOpenAPI documents the aggregate read without publishing it as an explorer surface.
func AddDashboardOpenAPI(spec *rpc.OpenAPISpec) {
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
		Tags: []string{"Connections"}, Summary: "List connection dashboard health",
		Description: "Returns one profile-aware, concurrently probed connection fleet for the namespace-lane dashboard.",
		OperationID: "listConnectionDashboardHealth",
		Parameters: []rpc.OpenAPIParameter{
			{Name: "type", In: "query", Schema: &rpc.OpenAPISchema{Type: "string"}},
			{Name: "types", In: "query", Schema: &rpc.OpenAPISchema{Type: "string"}},
		},
		Responses: map[string]rpc.OpenAPIResponse{"200": {
			Description: "Connection dashboard",
			Content: map[string]rpc.OpenAPIMediaType{"application/json": {Schema: &rpc.OpenAPISchema{
				Type: "object", Properties: map[string]*rpc.OpenAPISchema{
					"connections": {Type: "array", Items: item},
					"generatedAt": {Type: "string", Format: "date-time"},
				},
			}}},
		}},
	}}
}
