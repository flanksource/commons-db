package dbtest

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/flanksource/commons/logger"
	"github.com/lib/pq"
)

const (
	templateDatabasePrefix  = "commons_db_template_"
	templateMetadataPrefix  = "commons-db-template:v1:"
	templateIdentityVersion = "commons-db/dbtest-template/v1"
	templateLockNamespace   = "commons-db:template:"
	templateDigestLength    = 32
)

type templateIdentity struct {
	key      string
	metadata string
}

type templateDatabase struct {
	name              string
	metadata          sql.NullString
	isTemplate        bool
	allowsConnections bool
}

type templateManager struct {
	adminURL    string
	provisioner Provisioner
	identity    templateIdentity
	now         time.Time
}

func newTemplateIdentity(fingerprint string) (templateIdentity, error) {
	if strings.TrimSpace(fingerprint) == "" {
		return templateIdentity{}, errors.New("dbtest: provisioner fingerprint is empty")
	}
	digest := sha256.Sum256([]byte(templateIdentityVersion + "\x00" + fingerprint))
	key := hex.EncodeToString(digest[:])
	return templateIdentity{key: key, metadata: templateMetadataPrefix + key}, nil
}

func acquireProvisionedScratch(
	ctx context.Context,
	adminURL, name, unique string,
	provisioner Provisioner,
	now time.Time,
) (string, func() error, error) {
	fingerprint, err := provisioner.Fingerprint(ctx)
	if err != nil {
		return "", nil, fmt.Errorf("fingerprint database provisioner: %w", err)
	}
	identity, err := newTemplateIdentity(fingerprint)
	if err != nil {
		return "", nil, err
	}
	manager := templateManager{
		adminURL: adminURL, provisioner: provisioner, identity: identity,
		now: now.UTC().Truncate(time.Second),
	}
	return manager.acquire(ctx, name, unique)
}

func (m templateManager) acquire(
	ctx context.Context,
	name, unique string,
) (string, func() error, error) {
	if name == "" {
		return "", nil, errors.New("provisioned database name is required")
	}
	if unique == "" {
		return "", nil, errors.New("provisioned database unique suffix is required")
	}
	session, err := openPoolSession(ctx, m.adminURL)
	if err != nil {
		return "", nil, err
	}
	lockName := templateLockNamespace + m.identity.key
	if err := session.lock(ctx, lockName); err != nil {
		return "", nil, errors.Join(err, session.close())
	}
	templateName, err := m.ensureTemplate(ctx, session)
	if err == nil {
		if cleanupErr := m.cleanupStaleTemplates(ctx, session, templateName); cleanupErr != nil {
			logger.Warnf("dbtest: stale database template cleanup failed: %v", cleanupErr)
		}
	}
	instanceName := managedDatabaseName(testDatabasePrefix, m.now, unique, name)
	instanceCreated := false
	if err == nil {
		err = m.createInstance(ctx, session, templateName, instanceName)
		instanceCreated = err == nil
	}
	if err == nil {
		err = session.lock(ctx, instanceName)
	}
	unlockErr := session.unlock(ctx, lockName)
	if err != nil || unlockErr != nil {
		return "", nil, errors.Join(err, unlockErr, cleanupProvisioningFailure(session, instanceName, instanceCreated))
	}

	lease := &databaseLease{session: session, name: instanceName}
	dsn, err := withDatabase(m.adminURL, instanceName)
	if err != nil {
		return "", nil, errors.Join(err, lease.cleanup())
	}
	if err := m.provisioner.PrepareInstance(ctx, dsn); err != nil {
		return "", nil, errors.Join(
			fmt.Errorf("prepare provisioned database instance: %w", err),
			lease.cleanup(),
		)
	}
	return dsn, lease.cleanup, nil
}

func (m templateManager) ensureTemplate(ctx context.Context, session *poolSession) (string, error) {
	databases, err := listTemplateDatabases(ctx, session)
	if err != nil {
		return "", err
	}
	var matching []templateDatabase
	for _, database := range databases {
		if database.metadata.Valid && database.metadata.String == m.identity.metadata {
			key, managed := managedTemplateKey(database)
			if !managed || key != m.identity.key {
				return "", fmt.Errorf("database template %s has an invalid identity name", database.name)
			}
			matching = append(matching, database)
		}
	}
	newest := -1
	var newestCreated time.Time
	for index, database := range matching {
		if !database.isTemplate || database.allowsConnections {
			continue
		}
		created, err := managedDatabaseCreatedWithPrefixes(
			database.name,
			templateDatabasePrefix,
			templateDatabasePrefix,
		)
		if err != nil {
			return "", err
		}
		if newest < 0 || created.After(newestCreated) {
			newest, newestCreated = index, created
		}
	}
	for index, database := range matching {
		if index == newest {
			continue
		}
		if err := dropTemplateDatabase(ctx, session, database); err != nil {
			return "", fmt.Errorf("remove duplicate database template %s: %w", database.name, err)
		}
	}
	if newest >= 0 {
		return matching[newest].name, nil
	}
	return m.createTemplate(ctx, session)
}

func (m templateManager) createTemplate(ctx context.Context, session *poolSession) (string, error) {
	name := templateDatabaseName(m.identity, m.now)
	if exists, err := databaseByName(ctx, session, name); err != nil {
		return "", err
	} else if exists != nil {
		return "", fmt.Errorf("database template name %s is owned by different metadata", name)
	}
	if _, err := session.conn.ExecContext(
		ctx,
		"CREATE DATABASE "+pq.QuoteIdentifier(name)+" WITH TEMPLATE template0",
	); err != nil {
		return "", fmt.Errorf("create database template %s: %w", name, err)
	}
	created := templateDatabase{name: name}
	if _, err := session.conn.ExecContext(
		ctx,
		"COMMENT ON DATABASE "+pq.QuoteIdentifier(name)+" IS "+pq.QuoteLiteral(m.identity.metadata),
	); err != nil {
		return "", errors.Join(
			fmt.Errorf("label database template %s: %w", name, err),
			dropTemplateDatabase(ctx, session, created),
		)
	}
	created.metadata = sql.NullString{String: m.identity.metadata, Valid: true}
	dsn, err := withDatabase(m.adminURL, name)
	if err == nil {
		err = m.provisioner.PrepareTemplate(ctx, dsn)
	}
	if err != nil {
		return "", errors.Join(
			fmt.Errorf("prepare database template %s: %w", name, err),
			dropTemplateDatabase(ctx, session, created),
		)
	}
	if _, err := session.conn.ExecContext(
		ctx,
		"ALTER DATABASE "+pq.QuoteIdentifier(name)+" WITH ALLOW_CONNECTIONS false",
	); err != nil {
		return "", errors.Join(
			fmt.Errorf("stop connections to database template %s: %w", name, err),
			dropTemplateDatabase(ctx, session, created),
		)
	}
	drainContext, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()
	if err := session.waitForDatabaseConnectionsToClose(drainContext, name); err != nil {
		return "", errors.Join(
			fmt.Errorf("database template %s still has active connections after preparation: %w", name, err),
			dropTemplateDatabase(ctx, session, created),
		)
	}
	if _, err := session.conn.ExecContext(
		ctx,
		"ALTER DATABASE "+pq.QuoteIdentifier(name)+" WITH IS_TEMPLATE true",
	); err != nil {
		return "", errors.Join(
			fmt.Errorf("seal database template %s: %w", name, err),
			dropTemplateDatabase(ctx, session, created),
		)
	}
	return name, nil
}

func cleanupProvisioningFailure(session *poolSession, instanceName string, instanceCreated bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), connectTimeout)
	defer cancel()
	var dropErr error
	if instanceCreated {
		dropErr = session.dropDatabase(ctx, instanceName, true)
	}
	return errors.Join(dropErr, session.close())
}

func (m templateManager) createInstance(
	ctx context.Context,
	session *poolSession,
	templateName, instanceName string,
) error {
	if _, err := session.conn.ExecContext(
		ctx,
		"CREATE DATABASE "+pq.QuoteIdentifier(instanceName)+" WITH TEMPLATE "+pq.QuoteIdentifier(templateName),
	); err != nil {
		return fmt.Errorf("clone database template %s into %s: %w", templateName, instanceName, err)
	}
	return nil
}

func templateDatabaseName(identity templateIdentity, created time.Time) string {
	return fmt.Sprintf(
		"%s%d_%s",
		templateDatabasePrefix,
		created.UTC().Unix(),
		identity.key[:templateDigestLength],
	)
}
