package pgproxy

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
)

const (
	defaultConnection    = "pg_proxy"
	defaultMaxStatements = 20000
	driver               = "postgres"
)

// dsnEnvKeys are the variables a capture falls back to when `dsn` is unset, in
// preference order.
var dsnEnvKeys = []string{"GAVEL_DB_DSN", "DATABASE_URL"}

// Params choose the postgres the proxy forwards to and what it records.
type Params struct {
	// DSN is the upstream postgres://, with $VAR references expanded. Empty
	// falls back to GAVEL_DB_DSN, then DATABASE_URL.
	DSN string `json:"dsn,omitempty" clicky:"title=Upstream DSN"`
	// Params keeps bind parameter values in each statement's args. Off by
	// default: bind values carry row data, the likeliest place for a secret.
	Params bool `json:"params,omitempty" clicky:"title=Capture bind values"`
	// Connection labels the statements, as sql_statements' connection column.
	Connection string `json:"connection,omitempty" clicky:"title=Connection label"`
	// MaxStatements bounds what one capture stores; reaching it marks the
	// capture truncated rather than growing without limit.
	MaxStatements int `json:"maxStatements,omitempty" clicky:"title=Maximum statements"`
}

func (p Params) Validate() error {
	if strings.TrimSpace(p.Connection) == "" {
		return errors.New("connection must not be blank")
	}
	if p.MaxStatements < 1 {
		return fmt.Errorf("maxStatements %d must be at least 1", p.MaxStatements)
	}
	_, err := resolveDSN(p.DSN)
	return err
}

// resolveDSN expands `$VAR` references and falls back to the environment when
// declared is unset. It fails loudly rather than recording nothing: a proxy
// with no upstream is the silent-empty-capture failure to prevent, and a
// reference to an unset variable would otherwise blank a port to 5432 or the
// whole DSN to DATABASE_URL — another postgres than the one declared.
func resolveDSN(declared string) (*url.URL, error) {
	var unset []string
	raw := strings.TrimSpace(os.Expand(declared, func(name string) string {
		value, ok := os.LookupEnv(name)
		if !ok || value == "" {
			unset = append(unset, "$"+name)
		}
		return value
	}))
	if len(unset) > 0 {
		return nil, fmt.Errorf("pg_proxy dsn: %s is not set", strings.Join(unset, ", "))
	}
	if raw == "" {
		for _, key := range dsnEnvKeys {
			if value := strings.TrimSpace(os.Getenv(key)); value != "" {
				raw = value
				break
			}
		}
	}
	if raw == "" {
		return nil, fmt.Errorf("pg_proxy needs an upstream — set dsn or one of %s", strings.Join(dsnEnvKeys, ", "))
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		// url.Error repeats the raw DSN, password included.
		return nil, errors.New("pg_proxy dsn is not a URL")
	}
	if parsed.Scheme != "postgres" && parsed.Scheme != "postgresql" {
		return nil, fmt.Errorf("pg_proxy dsn must be a postgres:// URL, got %q", parsed.Scheme)
	}
	return parsed, nil
}

func upstreamAddr(dsn *url.URL) string {
	host, port := dsn.Hostname(), dsn.Port()
	if host == "" {
		host = "127.0.0.1"
	}
	if port == "" {
		port = "5432"
	}
	return net.JoinHostPort(host, port)
}
