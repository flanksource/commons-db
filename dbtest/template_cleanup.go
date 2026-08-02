package dbtest

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/flanksource/commons/logger"
	"github.com/lib/pq"
)

func listTemplateDatabases(ctx context.Context, session *poolSession) ([]templateDatabase, error) {
	rows, err := session.conn.QueryContext(
		ctx,
		`SELECT datname, shobj_description(oid, 'pg_database'), datistemplate, datallowconn
		 FROM pg_database
		 WHERE left(datname, length($1)) = $1
		 ORDER BY datname`,
		templateDatabasePrefix,
	)
	if err != nil {
		return nil, fmt.Errorf("list database templates: %w", err)
	}
	defer rows.Close() //nolint:errcheck

	var databases []templateDatabase
	for rows.Next() {
		var database templateDatabase
		if err := rows.Scan(
			&database.name,
			&database.metadata,
			&database.isTemplate,
			&database.allowsConnections,
		); err != nil {
			return nil, fmt.Errorf("scan database template: %w", err)
		}
		databases = append(databases, database)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list database templates: %w", err)
	}
	return databases, nil
}

func databaseByName(
	ctx context.Context,
	session *poolSession,
	name string,
) (*templateDatabase, error) {
	var database templateDatabase
	err := session.conn.QueryRowContext(
		ctx,
		`SELECT datname, shobj_description(oid, 'pg_database'), datistemplate, datallowconn
		 FROM pg_database
		 WHERE datname = $1`,
		name,
	).Scan(
		&database.name,
		&database.metadata,
		&database.isTemplate,
		&database.allowsConnections,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("inspect database template %s: %w", name, err)
	}
	return &database, nil
}

func (m templateManager) cleanupStaleTemplates(
	ctx context.Context,
	session *poolSession,
	currentName string,
) error {
	databases, err := listTemplateDatabases(ctx, session)
	if err != nil {
		return err
	}
	var failures error
	for _, database := range databases {
		if database.name == currentName {
			continue
		}
		key, ok := managedTemplateKey(database)
		if !ok {
			if database.metadata.Valid {
				logger.Warnf("dbtest: skipping database template %q with unknown metadata", database.name)
				continue
			}
			key = database.name
		}
		created, err := managedDatabaseCreatedWithPrefixes(
			database.name,
			templateDatabasePrefix,
			templateDatabasePrefix,
		)
		if err != nil || created.After(m.now) {
			logger.Warnf("dbtest: skipping database template %q with invalid timestamp", database.name)
			continue
		}
		if m.now.Sub(created) <= staleDatabaseAge {
			continue
		}
		lockName := templateLockNamespace + key
		locked, err := session.tryLock(ctx, lockName)
		if err != nil {
			failures = errors.Join(failures, err)
			continue
		}
		if !locked {
			continue
		}
		err = m.dropStaleTemplate(ctx, session, database)
		err = errors.Join(err, session.unlock(ctx, lockName))
		if err != nil {
			failures = errors.Join(failures, err)
		}
	}
	return failures
}

func (m templateManager) dropStaleTemplate(
	ctx context.Context,
	session *poolSession,
	expected templateDatabase,
) error {
	current, err := databaseByName(ctx, session, expected.name)
	if err != nil || current == nil {
		return err
	}
	if current.metadata != expected.metadata {
		return fmt.Errorf("database template %s metadata changed while locked", expected.name)
	}
	active, err := session.databaseHasConnections(ctx, expected.name)
	if err != nil || active {
		return err
	}
	return dropTemplateDatabase(ctx, session, *current)
}

func managedTemplateKey(database templateDatabase) (string, bool) {
	if !database.metadata.Valid || !strings.HasPrefix(database.metadata.String, templateMetadataPrefix) {
		return "", false
	}
	if _, err := managedDatabaseCreatedWithPrefixes(
		database.name,
		templateDatabasePrefix,
		templateDatabasePrefix,
	); err != nil {
		return "", false
	}
	key := strings.TrimPrefix(database.metadata.String, templateMetadataPrefix)
	if len(key) != 64 {
		return "", false
	}
	for _, character := range key {
		if !strings.ContainsRune("0123456789abcdef", character) {
			return "", false
		}
	}
	remainder := strings.TrimPrefix(database.name, templateDatabasePrefix)
	_, nameDigest, ok := strings.Cut(remainder, "_")
	if !ok || nameDigest != key[:templateDigestLength] {
		return "", false
	}
	return key, true
}

func dropTemplateDatabase(
	ctx context.Context,
	session *poolSession,
	database templateDatabase,
) error {
	if database.isTemplate {
		if _, err := session.conn.ExecContext(
			ctx,
			"ALTER DATABASE "+pq.QuoteIdentifier(database.name)+" WITH IS_TEMPLATE false",
		); err != nil {
			return fmt.Errorf("unmark database template %s: %w", database.name, err)
		}
	}
	return session.dropDatabase(ctx, database.name, true)
}
