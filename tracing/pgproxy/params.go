package pgproxy

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"regexp"
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
	// Env names the variable a process reads its DSN from, such as FIN_DB_URL.
	// When DSN is unset the upstream is read from it, and the proxied DSN is
	// exported under it so a process that reads only that variable is observed.
	Env string `json:"env,omitempty" clicky:"title=DSN variable"`
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
	if p.Env != "" && !envNamePattern.MatchString(p.Env) {
		return fmt.Errorf("env %q is not an environment variable name", p.Env)
	}
	_, err := resolveDSN(p)
	return err
}

var envNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// resolveDSN expands `$VAR` references and falls back to the environment when
// the DSN is unset: the named Env, else GAVEL_DB_DSN, then DATABASE_URL. It
// fails loudly rather than recording nothing: a proxy with no upstream is the
// silent-empty-capture failure to prevent, and a reference to an unset variable
// would otherwise blank a port to 5432 or the whole DSN to DATABASE_URL —
// another postgres than the one declared. A named Env that is unset is refused
// for the same reason, never replaced by the generic fallbacks.
func resolveDSN(p Params) (*url.URL, error) {
	declared := p.DSN
	if strings.TrimSpace(declared) == "" && p.Env != "" {
		declared = "$" + p.Env
	}
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
