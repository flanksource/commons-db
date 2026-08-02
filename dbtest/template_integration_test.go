package dbtest

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/lib/pq"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"golang.org/x/sync/errgroup"
)

type integrationProvisioner struct {
	fingerprint      string
	templateCalls    atomic.Int32
	instanceCalls    atomic.Int32
	templateFailures atomic.Int32
	instanceFailures atomic.Int32
}

func (p *integrationProvisioner) Fingerprint(context.Context) (string, error) {
	return p.fingerprint, nil
}

func (p *integrationProvisioner) PrepareTemplate(ctx context.Context, dsn string) error {
	call := p.templateCalls.Add(1)
	if call <= p.templateFailures.Load() {
		return errors.New("injected template preparation failure")
	}
	return executeProvisionerSQL(ctx, dsn, `
CREATE TABLE provisioner_events (value text NOT NULL);
INSERT INTO provisioner_events(value) VALUES ('template')`)
}

func (p *integrationProvisioner) PrepareInstance(ctx context.Context, dsn string) error {
	call := p.instanceCalls.Add(1)
	if call <= p.instanceFailures.Load() {
		return errors.New("injected instance preparation failure")
	}
	return executeProvisionerSQL(ctx, dsn, "INSERT INTO provisioner_events(value) VALUES ('instance')")
}

var _ = Describe("provisioned ephemeral databases", Label("integration"), Ordered, func() {
	var adminURL string

	BeforeAll(func() {
		var err error
		adminURL, err = startSharedEmbedded("")
		Expect(err).NotTo(HaveOccurred())
	})

	It("builds one sealed template and reconciles isolated clones", func(ctx SpecContext) {
		provisioner := &integrationProvisioner{fingerprint: fmt.Sprintf("reuse-%d", time.Now().UnixNano())}
		DeferCleanup(dropProvisionerTemplates, adminURL, provisioner.fingerprint)

		firstDSN, firstCleanup, err := acquireProvisionedScratch(
			ctx, adminURL, "first", uniqueSuffix(), provisioner, time.Now(),
		)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(expectProvisionerCleanup, firstCleanup)
		secondDSN, secondCleanup, err := acquireProvisionedScratch(
			ctx, adminURL, "second", uniqueSuffix(), provisioner, time.Now(),
		)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(expectProvisionerCleanup, secondCleanup)

		Expect(provisioner.templateCalls.Load()).To(Equal(int32(1)))
		Expect(provisioner.instanceCalls.Load()).To(Equal(int32(2)))
		Expect(provisionerEventCount(ctx, firstDSN)).To(Equal(2))
		Expect(provisionerEventCount(ctx, secondDSN)).To(Equal(2))
		Expect(executeProvisionerSQL(ctx, firstDSN, "INSERT INTO provisioner_events(value) VALUES ('first-only')")).To(Succeed())
		Expect(provisionerEventCount(ctx, firstDSN)).To(Equal(3))
		Expect(provisionerEventCount(ctx, secondDSN)).To(Equal(2))

		template := provisionerTemplate(adminURL, provisioner.fingerprint)
		Expect(template.isTemplate).To(BeTrue())
		Expect(template.allowsConnections).To(BeFalse())
		firstDatabase := databaseFromDSN(firstDSN)
		secondDatabase := databaseFromDSN(secondDSN)
		Expect(firstCleanup()).To(Succeed())
		Expect(secondCleanup()).To(Succeed())
		Expect(databaseExists(ctx, adminURL, firstDatabase)).To(BeFalse())
		Expect(databaseExists(ctx, adminURL, secondDatabase)).To(BeFalse())
		Expect(databaseExists(ctx, adminURL, template.name)).To(BeTrue())
	})

	It("serializes concurrent template creation by fingerprint", func(ctx SpecContext) {
		provisioner := &integrationProvisioner{fingerprint: fmt.Sprintf("concurrent-%d", time.Now().UnixNano())}
		DeferCleanup(dropProvisionerTemplates, adminURL, provisioner.fingerprint)

		const callers = 4
		cleanups := make([]func() error, callers)
		DeferCleanup(func() {
			for _, cleanup := range cleanups {
				if cleanup != nil {
					Expect(cleanup()).To(Succeed())
				}
			}
		})
		group, groupContext := errgroup.WithContext(ctx)
		for index := range callers {
			group.Go(func() error {
				_, cleanup, err := acquireProvisionedScratch(
					groupContext,
					adminURL,
					"concurrent",
					uniqueSuffix(),
					provisioner,
					time.Now(),
				)
				cleanups[index] = cleanup
				return err
			})
		}
		Expect(group.Wait()).To(Succeed())
		for _, cleanup := range cleanups {
			Expect(cleanup()).To(Succeed())
		}
		Expect(provisioner.templateCalls.Load()).To(Equal(int32(1)))
		Expect(provisioner.instanceCalls.Load()).To(Equal(int32(callers)))
	})

	It("retries template construction after a failed preparation", func(ctx SpecContext) {
		provisioner := &integrationProvisioner{fingerprint: fmt.Sprintf("retry-%d", time.Now().UnixNano())}
		provisioner.templateFailures.Store(1)
		DeferCleanup(dropProvisionerTemplates, adminURL, provisioner.fingerprint)

		_, _, err := acquireProvisionedScratch(
			ctx, adminURL, "failed", uniqueSuffix(), provisioner, time.Now(),
		)
		Expect(err).To(MatchError(ContainSubstring("injected template preparation failure")))
		_, cleanup, err := acquireProvisionedScratch(
			ctx, adminURL, "retry", uniqueSuffix(), provisioner, time.Now(),
		)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(expectProvisionerCleanup, cleanup)
		Expect(cleanup()).To(Succeed())
		Expect(provisioner.templateCalls.Load()).To(Equal(int32(2)))
		Expect(provisioner.instanceCalls.Load()).To(Equal(int32(1)))
	})

	It("drops a clone when instance reconciliation fails", func(ctx SpecContext) {
		provisioner := &integrationProvisioner{fingerprint: fmt.Sprintf("instance-failure-%d", time.Now().UnixNano())}
		provisioner.instanceFailures.Store(1)
		DeferCleanup(dropProvisionerTemplates, adminURL, provisioner.fingerprint)
		created := time.Now().UTC().Truncate(time.Second)
		unique := uniqueSuffix()
		instanceName := managedDatabaseName(testDatabasePrefix, created, unique, "instance-failure")

		_, _, err := acquireProvisionedScratch(
			ctx, adminURL, "instance-failure", unique, provisioner, created,
		)

		Expect(err).To(MatchError(ContainSubstring("injected instance preparation failure")))
		Expect(databaseExists(ctx, adminURL, instanceName)).To(BeFalse())
		Expect(provisioner.templateCalls.Load()).To(Equal(int32(1)))
		Expect(provisioner.instanceCalls.Load()).To(Equal(int32(1)))
	})

	It("retains the current template and safely reaps an unlocked superseded template", func(ctx SpecContext) {
		now := time.Now().UTC().Truncate(time.Second)
		old := now.Add(-staleDatabaseAge - time.Hour)
		oldProvisioner := &integrationProvisioner{fingerprint: fmt.Sprintf("old-%d", time.Now().UnixNano())}
		newProvisioner := &integrationProvisioner{fingerprint: fmt.Sprintf("new-%d", time.Now().UnixNano())}
		triggerProvisioner := &integrationProvisioner{fingerprint: fmt.Sprintf("trigger-%d", time.Now().UnixNano())}
		DeferCleanup(dropProvisionerTemplates, adminURL, oldProvisioner.fingerprint)
		DeferCleanup(dropProvisionerTemplates, adminURL, newProvisioner.fingerprint)
		DeferCleanup(dropProvisionerTemplates, adminURL, triggerProvisioner.fingerprint)

		_, cleanup, err := acquireProvisionedScratch(
			ctx, adminURL, "old", uniqueSuffix(), oldProvisioner, old,
		)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(expectProvisionerCleanup, cleanup)
		Expect(cleanup()).To(Succeed())
		oldTemplate := provisionerTemplate(adminURL, oldProvisioner.fingerprint)

		_, cleanup, err = acquireProvisionedScratch(
			ctx, adminURL, "current", uniqueSuffix(), oldProvisioner, now,
		)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(expectProvisionerCleanup, cleanup)
		Expect(cleanup()).To(Succeed())
		Expect(provisionerTemplate(adminURL, oldProvisioner.fingerprint).name).To(Equal(oldTemplate.name))

		identity, err := newTemplateIdentity(oldProvisioner.fingerprint)
		Expect(err).NotTo(HaveOccurred())
		lockSession, err := openPoolSession(ctx, adminURL)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { Expect(lockSession.close()).To(Succeed()) })
		Expect(lockSession.lock(ctx, templateLockNamespace+identity.key)).To(Succeed())
		_, cleanup, err = acquireProvisionedScratch(
			ctx, adminURL, "new", uniqueSuffix(), newProvisioner, now,
		)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(expectProvisionerCleanup, cleanup)
		Expect(cleanup()).To(Succeed())
		Expect(databaseExists(ctx, adminURL, oldTemplate.name)).To(BeTrue())
		Expect(lockSession.unlock(ctx, templateLockNamespace+identity.key)).To(Succeed())

		_, cleanup, err = acquireProvisionedScratch(
			ctx, adminURL, "trigger", uniqueSuffix(), triggerProvisioner, now,
		)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(expectProvisionerCleanup, cleanup)
		Expect(cleanup()).To(Succeed())
		Expect(databaseExists(ctx, adminURL, oldTemplate.name)).To(BeFalse())
	})

	It("reaps stale template databases left without metadata", func(ctx SpecContext) {
		now := time.Now().UTC().Truncate(time.Second)
		identity, err := newTemplateIdentity(fmt.Sprintf("orphan-%d", time.Now().UnixNano()))
		Expect(err).NotTo(HaveOccurred())
		orphanName := templateDatabaseName(identity, now.Add(-staleDatabaseAge-time.Hour))
		createSealedTemplate(ctx, adminURL, "template0", orphanName, "")
		DeferCleanup(dropTemplateIfPresent, adminURL, orphanName)
		trigger := &integrationProvisioner{fingerprint: fmt.Sprintf("orphan-trigger-%d", time.Now().UnixNano())}
		DeferCleanup(dropProvisionerTemplates, adminURL, trigger.fingerprint)

		_, cleanup, err := acquireProvisionedScratch(
			ctx, adminURL, "orphan-trigger", uniqueSuffix(), trigger, now,
		)

		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(expectProvisionerCleanup, cleanup)
		Expect(cleanup()).To(Succeed())
		Expect(databaseExists(ctx, adminURL, orphanName)).To(BeFalse())
	})

	It("keeps the newest sealed template when an identity has duplicates", func(ctx SpecContext) {
		now := time.Now().UTC().Truncate(time.Second)
		provisioner := &integrationProvisioner{fingerprint: fmt.Sprintf("duplicate-%d", time.Now().UnixNano())}
		DeferCleanup(dropProvisionerTemplates, adminURL, provisioner.fingerprint)
		_, cleanup, err := acquireProvisionedScratch(
			ctx, adminURL, "original", uniqueSuffix(), provisioner, now,
		)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(expectProvisionerCleanup, cleanup)
		Expect(cleanup()).To(Succeed())
		original := provisionerTemplate(adminURL, provisioner.fingerprint)
		identity, err := newTemplateIdentity(provisioner.fingerprint)
		Expect(err).NotTo(HaveOccurred())
		duplicateName := templateDatabaseName(identity, now.Add(time.Second))
		createSealedTemplate(ctx, adminURL, original.name, duplicateName, identity.metadata)
		DeferCleanup(dropTemplateIfPresent, adminURL, duplicateName)

		_, cleanup, err = acquireProvisionedScratch(
			ctx, adminURL, "duplicate", uniqueSuffix(), provisioner, now.Add(2*time.Second),
		)

		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(expectProvisionerCleanup, cleanup)
		Expect(cleanup()).To(Succeed())
		Expect(databaseExists(ctx, adminURL, original.name)).To(BeFalse())
		Expect(provisionerTemplate(adminURL, provisioner.fingerprint).name).To(Equal(duplicateName))
	})
})

func executeProvisionerSQL(ctx context.Context, dsn, statement string) error {
	database, err := sql.Open("postgres", dsn)
	if err != nil {
		return err
	}
	defer database.Close() //nolint:errcheck
	_, err = database.ExecContext(ctx, statement)
	return err
}

func expectProvisionerCleanup(cleanup func() error) {
	GinkgoHelper()
	Expect(cleanup()).To(Succeed())
}

func provisionerEventCount(ctx context.Context, dsn string) int {
	GinkgoHelper()
	database, err := sql.Open("postgres", dsn)
	Expect(err).NotTo(HaveOccurred())
	defer database.Close() //nolint:errcheck
	var count int
	Expect(database.QueryRowContext(ctx, "SELECT count(*) FROM provisioner_events").Scan(&count)).To(Succeed())
	return count
}

func provisionerTemplate(adminURL, fingerprint string) templateDatabase {
	GinkgoHelper()
	identity, err := newTemplateIdentity(fingerprint)
	Expect(err).NotTo(HaveOccurred())
	ctx, cancel := context.WithTimeout(context.Background(), connectTimeout)
	defer cancel()
	session, err := openPoolSession(ctx, adminURL)
	Expect(err).NotTo(HaveOccurred())
	defer session.close() //nolint:errcheck
	databases, err := listTemplateDatabases(ctx, session)
	Expect(err).NotTo(HaveOccurred())
	for _, database := range databases {
		if database.metadata.Valid && database.metadata.String == identity.metadata {
			return database
		}
	}
	Fail("provisioner template was not found")
	return templateDatabase{}
}

func dropProvisionerTemplates(adminURL, fingerprint string) {
	GinkgoHelper()
	identity, err := newTemplateIdentity(fingerprint)
	Expect(err).NotTo(HaveOccurred())
	ctx, cancel := context.WithTimeout(context.Background(), connectTimeout)
	defer cancel()
	session, err := openPoolSession(ctx, adminURL)
	Expect(err).NotTo(HaveOccurred())
	defer session.close() //nolint:errcheck
	databases, err := listTemplateDatabases(ctx, session)
	Expect(err).NotTo(HaveOccurred())
	for _, database := range databases {
		if database.metadata.Valid && database.metadata.String == identity.metadata {
			if database.isTemplate {
				_, err = session.conn.ExecContext(
					ctx,
					"ALTER DATABASE "+pq.QuoteIdentifier(database.name)+" WITH IS_TEMPLATE false",
				)
				Expect(err).NotTo(HaveOccurred())
			}
			Expect(session.dropDatabase(ctx, database.name, true)).To(Succeed())
		}
	}
}

func createSealedTemplate(ctx context.Context, adminURL, source, name, metadata string) {
	GinkgoHelper()
	session, err := openPoolSession(ctx, adminURL)
	Expect(err).NotTo(HaveOccurred())
	defer session.close() //nolint:errcheck
	_, err = session.conn.ExecContext(
		ctx,
		"CREATE DATABASE "+pq.QuoteIdentifier(name)+" WITH TEMPLATE "+pq.QuoteIdentifier(source),
	)
	Expect(err).NotTo(HaveOccurred())
	if metadata != "" {
		_, err = session.conn.ExecContext(
			ctx,
			"COMMENT ON DATABASE "+pq.QuoteIdentifier(name)+" IS "+pq.QuoteLiteral(metadata),
		)
		Expect(err).NotTo(HaveOccurred())
	}
	_, err = session.conn.ExecContext(
		ctx,
		"ALTER DATABASE "+pq.QuoteIdentifier(name)+" WITH ALLOW_CONNECTIONS false IS_TEMPLATE true",
	)
	Expect(err).NotTo(HaveOccurred())
}

func dropTemplateIfPresent(adminURL, name string) {
	GinkgoHelper()
	ctx, cancel := context.WithTimeout(context.Background(), connectTimeout)
	defer cancel()
	session, err := openPoolSession(ctx, adminURL)
	Expect(err).NotTo(HaveOccurred())
	defer session.close() //nolint:errcheck
	database, err := databaseByName(ctx, session, name)
	Expect(err).NotTo(HaveOccurred())
	if database != nil {
		Expect(dropTemplateDatabase(ctx, session, *database)).To(Succeed())
	}
}
