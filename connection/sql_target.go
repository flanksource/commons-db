package connection

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"

	clickhouse "github.com/ClickHouse/clickhouse-go/v2"
	mysql "github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/microsoft/go-mssqldb/msdsn"

	"github.com/flanksource/commons-db/models"
)

// SQLTarget is where a SQL connection points — the server, login and database
// the driver is opened with — and never its password.
type SQLTarget struct {
	Host     string `json:"host,omitempty"`
	Port     string `json:"port,omitempty"`
	User     string `json:"user,omitempty"`
	Database string `json:"database,omitempty"`
}

func (t SQLTarget) String() string {
	address := t.Host
	if t.Port != "" {
		address = net.JoinHostPort(t.Host, t.Port)
	}
	if t.User != "" {
		address = t.User + "@" + address
	}
	switch {
	case address == "":
		return t.Database
	case t.Database != "":
		address += "/" + t.Database
	}
	return address
}

// Target is the SQLTarget of the DSN Client opens, read through each driver's
// own parser so defaults (a postgres keyword DSN's PGHOST, the SQL Server
// port) are the ones the driver applies.
//
// NOTE: Must be run on a hydrated SQLConnection.
func (s SQLConnection) Target() (SQLTarget, error) {
	dsn, err := s.connectionString()
	if err != nil {
		return SQLTarget{}, err
	}
	switch s.Type {
	case "", models.ConnectionTypePostgres:
		cfg, err := pgconn.ParseConfig(dsn)
		if err != nil {
			return SQLTarget{}, fmt.Errorf("invalid postgres connection string: %w", err)
		}
		return SQLTarget{Host: cfg.Host, Port: strconv.Itoa(int(cfg.Port)), User: cfg.User, Database: cfg.Database}, nil
	case models.ConnectionTypeMySQL:
		cfg, err := mysql.ParseDSN(dsn)
		if err != nil {
			return SQLTarget{}, fmt.Errorf("invalid mysql connection string: %w", err)
		}
		host, port, err := net.SplitHostPort(cfg.Addr)
		if err != nil {
			host, port = cfg.Addr, ""
		}
		return SQLTarget{Host: host, Port: port, User: cfg.User, Database: cfg.DBName}, nil
	case models.ConnectionTypeSQLServer:
		cfg, err := msdsn.Parse(dsn)
		if err != nil {
			return SQLTarget{}, fmt.Errorf("invalid sqlserver connection string: %w", err)
		}
		host := cfg.Host
		if cfg.Instance != "" {
			host += `\` + cfg.Instance
		}
		target := SQLTarget{Host: host, User: cfg.User, Database: cfg.Database}
		if cfg.Port != 0 {
			target.Port = strconv.FormatUint(cfg.Port, 10)
		}
		return target, nil
	case models.ConnectionTypeClickHouse:
		options, err := clickhouse.ParseDSN(dsn)
		if err != nil {
			return SQLTarget{}, fmt.Errorf("invalid clickhouse connection string: %w", err)
		}
		target := SQLTarget{Host: strings.Join(options.Addr, ","), User: options.Auth.Username, Database: options.Auth.Database}
		if len(options.Addr) == 1 {
			if host, port, err := net.SplitHostPort(options.Addr[0]); err == nil {
				target.Host, target.Port = host, port
			}
		}
		return target, nil
	case models.ConnectionTypeSQLite:
		path := strings.TrimPrefix(dsn, "file:")
		if parsed, err := url.Parse(path); err == nil && parsed.Path != "" {
			path = parsed.Path
		}
		return SQLTarget{Database: path}, nil
	}
	return SQLTarget{}, fmt.Errorf("unsupported sql connection type: %s", s.Type)
}
