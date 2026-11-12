package connection

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/flanksource/commons-db/models"
	mysql "github.com/go-sql-driver/mysql"
)

func (s SQLConnection) Validate() error {
	_, err := s.address()
	return err
}

func (s SQLConnection) address() (string, error) {
	raw := s.URL.ValueStatic
	host, database, port := s.Properties["host"], s.Properties["database"], s.Properties["port"]
	if raw != "" {
		return raw, nil
	}
	if host == "" || database == "" {
		return "", fmt.Errorf("a database URL or both Host and Database are required")
	}
	if strings.ContainsAny(host, "/@?# ") || strings.Contains(host, "://") {
		return "", fmt.Errorf("database Host must be a hostname or IP address")
	}
	if port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return "", fmt.Errorf("database Port must be between 1 and 65535")
		}
		host = net.JoinHostPort(strings.Trim(host, "[]"), port)
	} else if strings.Contains(host, ":") {
		host = "[" + strings.Trim(host, "[]") + "]"
	}
	if s.Type == "mysql" {
		cfg := mysql.NewConfig()
		cfg.Net, cfg.Addr, cfg.DBName = "tcp", host, database
		return cfg.FormatDSN(), nil
	}
	u := url.URL{Scheme: s.Type, Host: host, Path: "/" + database}
	if u.Scheme == "" {
		u.Scheme = "postgres"
	}
	if s.Type == models.ConnectionTypeSQLServer {
		u.Scheme = "sqlserver"
		u.Path = ""
		u.RawQuery = url.Values{"database": {database}}.Encode()
	}
	return u.String(), nil
}
