package migrate

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"ariga.io/atlas/sql/schema"
	"github.com/lib/pq"
)

const defaultSchema = "public"

var normalizedSchemaName = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

// ValidateSchemaName validates an application-normalized PostgreSQL schema identifier.
func ValidateSchemaName(name string) error {
	if name == "" {
		return fmt.Errorf("schema name is empty")
	}
	if len(name) > 63 {
		return fmt.Errorf("schema name %q is %d bytes; maximum is 63", name, len(name))
	}
	if !normalizedSchemaName.MatchString(name) {
		return fmt.Errorf("schema name %q must match %s", name, normalizedSchemaName.String())
	}
	return nil
}

// ConnectionForSchema returns a PostgreSQL URL whose connections select schemaName.
func ConnectionForSchema(connection, schemaName string) (string, error) {
	if err := ValidateSchemaName(schemaName); err != nil {
		return "", err
	}
	parsed, err := url.Parse(connection)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("schema-scoped migrations require a URL-form PostgreSQL connection")
	}
	switch strings.ToLower(parsed.Scheme) {
	case "postgres", "postgresql":
	default:
		return "", fmt.Errorf("schema-scoped migrations require a PostgreSQL connection, got %q", parsed.Scheme)
	}
	query := parsed.Query()
	query.Set("search_path", schemaName)
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func migrationScope(schemaName, bundleName string) string {
	if schemaName != defaultSchema {
		return schemaName + ":" + bundleName
	}
	return bundleName
}

func remapDesiredSchema(realm *schema.Realm, schemaName string) error {
	if realm == nil {
		return fmt.Errorf("desired realm is nil")
	}
	if err := ValidateSchemaName(schemaName); err != nil {
		return err
	}
	var source *schema.Schema
	for _, candidate := range realm.Schemas {
		if candidate.Name != defaultSchema {
			return fmt.Errorf("desired realm contains schema %q; schema-scoped bundles must declare only %q", candidate.Name, defaultSchema)
		}
		source = candidate
	}
	if source == nil {
		return fmt.Errorf("desired realm does not declare schema %q", defaultSchema)
	}
	source.Name = schemaName
	return nil
}

func remapSecuritySchema(spec securitySpec, schemaName string) securitySpec {
	out := securitySpec{Roles: append([]roleSpec(nil), spec.Roles...), Permissions: make([]permissionSpec, len(spec.Permissions))}
	for i, permission := range spec.Permissions {
		permission.Privileges = append([]string(nil), permission.Privileges...)
		for _, prefix := range []string{"table:", "column:", "sequence:"} {
			permission.Target = strings.Replace(permission.Target, prefix+defaultSchema+".", prefix+schemaName+".", 1)
		}
		if permission.Target == "schema:"+defaultSchema {
			permission.Target = "schema:" + schemaName
		}
		out.Permissions[i] = permission
	}
	return out
}

type schemaConnectionOptions struct {
	Connection string
	Schema     string
}

func prepareSchemaConnection(ctx context.Context, opts schemaConnectionOptions) (string, error) {
	if err := ValidateSchemaName(opts.Schema); err != nil {
		return "", err
	}
	if opts.Schema == defaultSchema {
		return opts.Connection, nil
	}
	scopedConnection, err := ConnectionForSchema(opts.Connection, opts.Schema)
	if err != nil {
		return "", err
	}
	database, err := sql.Open("postgres", opts.Connection)
	if err != nil {
		return "", fmt.Errorf("open schema migration database: %w", err)
	}
	defer database.Close() //nolint:errcheck
	if err := database.PingContext(ctx); err != nil {
		return "", fmt.Errorf("connect schema migration database: %w", err)
	}
	if _, err := database.ExecContext(ctx, "CREATE SCHEMA IF NOT EXISTS "+pq.QuoteIdentifier(opts.Schema)); err != nil {
		return "", fmt.Errorf("create schema %q: %w", opts.Schema, err)
	}
	return scopedConnection, nil
}
