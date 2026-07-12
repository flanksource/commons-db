package connection

import (
	databasesql "database/sql"
	"fmt"
	"net/url"
	"slices"
	"strings"

	_ "github.com/ClickHouse/clickhouse-go/v2"
	mysql "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "github.com/microsoft/go-mssqldb"
	"github.com/microsoft/go-mssqldb/msdsn"

	"github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/types"
)

var supportedSQLTypes = []string{
	models.ConnectionTypePostgres,
	models.ConnectionTypeMySQL,
	models.ConnectionTypeSQLServer,
	models.ConnectionTypeClickHouse,
}

// SQLConnection is a multi-driver SQL connection (postgres, mysql, sqlserver).
// Ported from duty/connection so commons-db data providers can open arbitrary
// SQL backends from a models.Connection or an inline URL.
//
// +kubebuilder:object:generate=true
type SQLConnection struct {
	ConnectionName string       `yaml:"connection,omitempty" json:"connection,omitempty"`
	Type           string       `yaml:"type,omitempty" json:"type,omitempty"`
	URL            types.EnvVar `yaml:"url,omitempty" json:"url,omitempty"`
	Username       types.EnvVar `yaml:"username,omitempty" json:"username,omitempty"`
	Password       types.EnvVar `yaml:"password,omitempty" json:"password,omitempty"`
}

func (s *SQLConnection) FromModel(connection models.Connection) error {
	if !isSupportedSQLType(connection.Type) {
		return fmt.Errorf("connection of type %s cannot be used with sql, expected one of %s", connection.Type, strings.Join(supportedSQLTypes, ", "))
	}

	s.ConnectionName = connection.Name
	s.Type = connection.Type
	s.URL = types.EnvVar{ValueStatic: connection.URL}
	s.Username = types.EnvVar{ValueStatic: connection.Username}
	s.Password = types.EnvVar{ValueStatic: connection.Password}
	return nil
}

func (s SQLConnection) ToModel() models.Connection {
	connType := s.Type
	if connType == "" {
		connType = models.ConnectionTypePostgres
	}

	return models.Connection{
		Name:     s.ConnectionName,
		Type:     connType,
		URL:      s.URL.ValueStatic,
		Username: s.Username.ValueStatic,
		Password: s.Password.ValueStatic,
	}
}

// Client creates and returns a database/sql DB client.
//
// NOTE: Must be run on a hydrated SQLConnection.
func (s *SQLConnection) Client(ctx context.Context) (*databasesql.DB, error) {
	if s.Type == "" {
		s.Type = models.ConnectionTypePostgres
	}

	driverName, err := sqlDriverName(s.Type)
	if err != nil {
		return nil, err
	}

	if s.URL.ValueStatic == "" {
		return nil, fmt.Errorf("sql connection url cannot be empty")
	}

	connectionString, err := s.connectionString()
	if err != nil {
		return nil, err
	}

	client, err := databasesql.Open(driverName, connectionString)
	if err != nil {
		return nil, err
	}

	return client, nil
}

// connectionString applies credentials resolved from EnvVar-backed username and
// password fields to the driver DSN. Keeping credentials outside URL is useful
// for secret:// references, but database/sql drivers only receive the DSN passed
// to Open and otherwise fall back to process defaults (for pgx, the OS user).
func (s SQLConnection) connectionString() (string, error) {
	raw := s.URL.ValueStatic
	username := s.Username.ValueStatic
	password := s.Password.ValueStatic
	if username == "" && password == "" {
		return raw, nil
	}

	switch s.Type {
	case "", models.ConnectionTypePostgres:
		if strings.Contains(raw, "://") {
			return applyURLCredentials(raw, username, password)
		}
		if username != "" {
			raw += " user=" + quotePostgresDSNValue(username)
		}
		if password != "" {
			raw += " password=" + quotePostgresDSNValue(password)
		}
		return strings.TrimSpace(raw), nil
	case models.ConnectionTypeMySQL:
		cfg, err := mysql.ParseDSN(raw)
		if err != nil {
			return "", fmt.Errorf("invalid mysql connection string: %w", err)
		}
		if username != "" {
			cfg.User = username
		}
		if password != "" {
			cfg.Passwd = password
		}
		return cfg.FormatDSN(), nil
	case models.ConnectionTypeSQLServer:
		cfg, err := msdsn.Parse(raw)
		if err != nil {
			return "", fmt.Errorf("invalid sqlserver connection string: %w", err)
		}
		if username != "" {
			cfg.User = username
		}
		if password != "" {
			cfg.Password = password
		}
		return cfg.URL().String(), nil
	case models.ConnectionTypeClickHouse:
		return applyURLCredentials(raw, username, password)
	default:
		return raw, nil
	}
}

func applyURLCredentials(raw, username, password string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("invalid sql connection URL: %w", err)
	}
	currentUsername := ""
	currentPassword := ""
	if parsed.User != nil {
		currentUsername = parsed.User.Username()
		currentPassword, _ = parsed.User.Password()
	}
	if username != "" {
		currentUsername = username
	}
	if password != "" {
		currentPassword = password
	}
	if currentPassword != "" {
		parsed.User = url.UserPassword(currentUsername, currentPassword)
	} else if currentUsername != "" {
		parsed.User = url.User(currentUsername)
	}
	return parsed.String(), nil
}

func quotePostgresDSNValue(value string) string {
	return "'" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(value) + "'"
}

func (s *SQLConnection) HydrateConnection(ctx context.Context) error {
	if s.ConnectionName != "" {
		connection, err := ctx.HydrateConnectionByURL(s.ConnectionName)
		if err != nil {
			return fmt.Errorf("could not hydrate connection[%s]: %w", s.ConnectionName, err)
		}
		if connection == nil {
			return fmt.Errorf("connection[%s] not found", s.ConnectionName)
		}
		existing := *s
		if err := s.FromModel(*connection); err != nil {
			return err
		}
		if !existing.URL.IsEmpty() {
			s.URL = existing.URL
		}
		if !existing.Username.IsEmpty() {
			s.Username = existing.Username
		}
		if !existing.Password.IsEmpty() {
			s.Password = existing.Password
		}
		if existing.Type != "" {
			s.Type = existing.Type
		}
	}

	ns := ctx.GetNamespace()

	if v, err := ctx.GetEnvValueFromCache(s.URL, ns); err != nil {
		return fmt.Errorf("could not get sql url from env var: %w", err)
	} else {
		s.URL.ValueStatic = v
	}

	if v, err := ctx.GetEnvValueFromCache(s.Username, ns); err != nil {
		return fmt.Errorf("could not get sql username from env var: %w", err)
	} else {
		s.Username.ValueStatic = v
	}

	if v, err := ctx.GetEnvValueFromCache(s.Password, ns); err != nil {
		return fmt.Errorf("could not get sql password from env var: %w", err)
	} else {
		s.Password.ValueStatic = v
	}

	return nil
}

func sqlDriverName(connectionType string) (string, error) {
	switch connectionType {
	case models.ConnectionTypePostgres:
		return "pgx", nil
	case models.ConnectionTypeMySQL:
		return "mysql", nil
	case models.ConnectionTypeSQLServer:
		return "sqlserver", nil
	case models.ConnectionTypeClickHouse:
		return "clickhouse", nil
	default:
		return "", fmt.Errorf("unsupported sql connection type: %s", connectionType)
	}
}

func isSupportedSQLType(connectionType string) bool {
	return slices.Contains(supportedSQLTypes, connectionType)
}
