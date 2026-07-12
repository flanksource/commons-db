package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"strings"
	"time"

	"github.com/valkey-io/valkey-go"

	dbconnection "github.com/flanksource/commons-db/connection"
	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/models"
)

type connectionInfo struct {
	Connection   connectionInfoDetails `json:"connection"`
	Server       serverInfo            `json:"server"`
	DiscoveredAt time.Time             `json:"discoveredAt"`
}

type connectionInfoDetails struct {
	Name               string             `json:"name"`
	Type               string             `json:"type"`
	Namespace          string             `json:"namespace,omitempty"`
	ConfiguredEndpoint string             `json:"configuredEndpoint,omitempty"`
	ResolvedEndpoint   string             `json:"resolvedEndpoint,omitempty"`
	ConfiguredUsername string             `json:"configuredUsername,omitempty"`
	ResolvedUsername   string             `json:"resolvedUsername,omitempty"`
	Password           connectionPresence `json:"password"`
	Certificate        connectionPresence `json:"certificate"`
}

type connectionPresence struct {
	Configured bool `json:"configured"`
	Resolved   bool `json:"resolved"`
}

type serverInfo struct {
	Status   string            `json:"status"`
	Product  string            `json:"product,omitempty"`
	Version  string            `json:"version,omitempty"`
	Database string            `json:"database,omitempty"`
	User     string            `json:"user,omitempty"`
	Cluster  string            `json:"cluster,omitempty"`
	Node     string            `json:"node,omitempty"`
	Details  map[string]string `json:"details,omitempty"`
	Message  string            `json:"message,omitempty"`
}

func (h *connectionBrowserHandler) serveConnectionInfo(w http.ResponseWriter, r *http.Request, id string) {
	raw, err := findConnection(h.ctx.DB(), id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	resolved := cloneConnection(raw)
	if _, err := dbcontext.HydrateConnection(h.ctx, resolved); err != nil {
		http.Error(w, sanitizeConnectionError(err, raw, resolved), http.StatusUnprocessableEntity)
		return
	}

	discoveryContext, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	server := discoverServer(discoveryContext, h.ctx, resolved)
	if server.Status == "error" {
		server.Message = sanitizeConnectionError(fmt.Errorf("%s", server.Message), raw, resolved)
	}

	writeK8sJSON(w, connectionInfo{
		Connection: connectionInfoDetails{
			Name:               raw.Name,
			Type:               raw.Type,
			Namespace:          raw.Namespace,
			ConfiguredEndpoint: redactConnectionURL(raw.URL),
			ResolvedEndpoint:   redactConnectionURL(resolved.URL),
			ConfiguredUsername: raw.Username,
			ResolvedUsername:   resolved.Username,
			Password: connectionPresence{
				Configured: strings.TrimSpace(raw.Password) != "",
				Resolved:   strings.TrimSpace(resolved.Password) != "",
			},
			Certificate: connectionPresence{
				Configured: strings.TrimSpace(raw.Certificate) != "",
				Resolved:   strings.TrimSpace(resolved.Certificate) != "",
			},
		},
		Server:       server,
		DiscoveredAt: time.Now().UTC(),
	})
}

func cloneConnection(source *models.Connection) *models.Connection {
	clone := *source
	clone.Properties = maps.Clone(source.Properties)
	return &clone
}

func sanitizeConnectionError(err error, connections ...*models.Connection) string {
	if err == nil {
		return ""
	}
	message := err.Error()
	for _, connection := range connections {
		if connection == nil {
			continue
		}
		for _, secret := range []string{connection.Password, connection.Certificate} {
			if strings.TrimSpace(secret) != "" {
				message = strings.ReplaceAll(message, secret, "redacted")
			}
		}
		if connection.URL != "" {
			message = strings.ReplaceAll(message, connection.URL, redactConnectionURL(connection.URL))
		}
		for key, value := range connection.Properties {
			if isSensitiveCredentialKey(key) && value != "" {
				message = strings.ReplaceAll(message, value, "redacted")
			}
		}
	}
	return message
}

func discoverServer(ctx context.Context, connectionContext dbcontext.Context, connection *models.Connection) serverInfo {
	var info serverInfo
	var err error
	switch connection.Type {
	case models.ConnectionTypePostgres, models.ConnectionTypeMySQL, models.ConnectionTypeSQLServer, models.ConnectionTypeClickHouse:
		info, err = discoverSQLServer(ctx, connectionContext, connection)
	case models.ConnectionTypeOpenSearch:
		info, err = discoverOpenSearch(ctx, connectionContext, connection)
	case models.ConnectionTypePrometheus:
		info, err = discoverPrometheus(ctx, connectionContext, connection)
	case models.ConnectionTypeRedis:
		info, err = discoverRedis(ctx, connection)
	default:
		return serverInfo{Status: "unavailable", Message: "Server version discovery is not available for this connection type"}
	}
	if err != nil {
		return serverInfo{Status: "error", Message: err.Error()}
	}
	info.Status = "available"
	return info
}

func discoverSQLServer(ctx context.Context, connectionContext dbcontext.Context, connection *models.Connection) (serverInfo, error) {
	var sqlConnection dbconnection.SQLConnection
	if err := sqlConnection.FromModel(*connection); err != nil {
		return serverInfo{}, err
	}
	client, err := sqlConnection.Client(connectionContext)
	if err != nil {
		return serverInfo{}, err
	}
	defer client.Close()

	var query string
	switch connection.Type {
	case models.ConnectionTypePostgres:
		query = `SELECT current_setting('server_version'), current_database(), current_user`
	case models.ConnectionTypeMySQL:
		query = `SELECT VERSION(), COALESCE(DATABASE(), ''), CURRENT_USER()`
	case models.ConnectionTypeSQLServer:
		query = `SELECT CAST(SERVERPROPERTY('ProductVersion') AS nvarchar(128)), DB_NAME(), SUSER_SNAME(), CAST(SERVERPROPERTY('Edition') AS nvarchar(128)), CAST(SERVERPROPERTY('ProductLevel') AS nvarchar(128))`
	case models.ConnectionTypeClickHouse:
		query = `SELECT version(), currentDatabase(), currentUser()`
	}

	row := client.QueryRowContext(ctx, query)
	info := serverInfo{Product: sqlProductName(connection.Type)}
	if connection.Type == models.ConnectionTypeSQLServer {
		var edition, productLevel sql.NullString
		if err := row.Scan(&info.Version, &info.Database, &info.User, &edition, &productLevel); err != nil {
			return serverInfo{}, err
		}
		info.Details = nonEmptyDetails(map[string]string{"edition": edition.String, "productLevel": productLevel.String})
		return info, nil
	}
	if err := row.Scan(&info.Version, &info.Database, &info.User); err != nil {
		return serverInfo{}, err
	}
	return info, nil
}

func sqlProductName(connectionType string) string {
	switch connectionType {
	case models.ConnectionTypePostgres:
		return "PostgreSQL"
	case models.ConnectionTypeMySQL:
		return "MySQL"
	case models.ConnectionTypeSQLServer:
		return "SQL Server"
	case models.ConnectionTypeClickHouse:
		return "ClickHouse"
	default:
		return connectionType
	}
}

func discoverOpenSearch(ctx context.Context, connectionContext dbcontext.Context, connection *models.Connection) (serverInfo, error) {
	var response struct {
		Name        string `json:"name"`
		ClusterName string `json:"cluster_name"`
		Version     struct {
			Distribution  string `json:"distribution"`
			Number        string `json:"number"`
			LuceneVersion string `json:"lucene_version"`
		} `json:"version"`
	}
	if err := getConnectionJSON(ctx, connectionContext, connection, "/", &response); err != nil {
		return serverInfo{}, err
	}
	product := "OpenSearch"
	if response.Version.Distribution != "" {
		product = response.Version.Distribution
	}
	return serverInfo{
		Product: product, Version: response.Version.Number, Cluster: response.ClusterName, Node: response.Name,
		Details: nonEmptyDetails(map[string]string{"luceneVersion": response.Version.LuceneVersion}),
	}, nil
}

func discoverPrometheus(ctx context.Context, connectionContext dbcontext.Context, connection *models.Connection) (serverInfo, error) {
	var response struct {
		Status string `json:"status"`
		Data   struct {
			Version   string `json:"version"`
			Revision  string `json:"revision"`
			Branch    string `json:"branch"`
			BuildDate string `json:"buildDate"`
			GoVersion string `json:"goVersion"`
		} `json:"data"`
	}
	if err := getConnectionJSON(ctx, connectionContext, connection, "/api/v1/status/buildinfo", &response); err != nil {
		return serverInfo{}, err
	}
	if response.Status != "success" {
		return serverInfo{}, fmt.Errorf("Prometheus build information returned status %q", response.Status)
	}
	return serverInfo{
		Product: "Prometheus", Version: response.Data.Version,
		Details: nonEmptyDetails(map[string]string{
			"revision": response.Data.Revision, "branch": response.Data.Branch,
			"buildDate": response.Data.BuildDate, "goVersion": response.Data.GoVersion,
		}),
	}, nil
}

func getConnectionJSON(ctx context.Context, connectionContext dbcontext.Context, connection *models.Connection, path string, target any) error {
	httpConnection, err := dbconnection.NewHTTPConnection(connectionContext, *connection)
	if err != nil {
		return err
	}
	client := &http.Client{Transport: httpConnection.Transport()}
	targetURL := strings.TrimRight(connection.URL, "/") + path
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return err
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("server metadata request returned %s", response.Status)
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(target); err != nil {
		return fmt.Errorf("decode server metadata: %w", err)
	}
	return nil
}

func discoverRedis(ctx context.Context, connection *models.Connection) (serverInfo, error) {
	option, err := valkey.ParseURL(connection.URL)
	if err != nil {
		return serverInfo{}, err
	}
	if connection.Username != "" {
		option.Username = connection.Username
	}
	if connection.Password != "" {
		option.Password = connection.Password
	}
	client, err := valkey.NewClient(option)
	if err != nil {
		return serverInfo{}, err
	}
	defer client.Close()
	result, err := client.Do(ctx, client.B().Info().Section("server").Build()).ToString()
	if err != nil {
		return serverInfo{}, err
	}
	values := parseRedisInfo(result)
	product := "Redis"
	version := values["redis_version"]
	if values["server_name"] != "" {
		product = values["server_name"]
	}
	if values["valkey_version"] != "" {
		product, version = "Valkey", values["valkey_version"]
	}
	return serverInfo{
		Product: product, Version: version,
		Details: nonEmptyDetails(map[string]string{
			"mode": values["redis_mode"], "os": values["os"], "architecture": values["arch_bits"],
		}),
	}, nil
}

func parseRedisInfo(value string) map[string]string {
	result := map[string]string{}
	for _, line := range strings.Split(value, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, found := strings.Cut(line, ":")
		if found {
			result[strings.TrimSpace(key)] = strings.TrimSpace(value)
		}
	}
	return result
}

func nonEmptyDetails(values map[string]string) map[string]string {
	for key, value := range values {
		if strings.TrimSpace(value) == "" {
			delete(values, key)
		}
	}
	if len(values) == 0 {
		return nil
	}
	return values
}
