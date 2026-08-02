package dbtest

import (
	"context"
	"database/sql"
	"strings"
	"sync/atomic"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type recordingProvisioner struct {
	fingerprint string
	calls       atomic.Int32
}

func (p *recordingProvisioner) Fingerprint(context.Context) (string, error) {
	p.calls.Add(1)
	return p.fingerprint, nil
}

func (*recordingProvisioner) PrepareTemplate(context.Context, string) error { return nil }

func (*recordingProvisioner) PrepareInstance(context.Context, string) error { return nil }

var _ = Describe("provisioned database configuration", func() {
	It("derives a stable managed template identity from the provisioner fingerprint", func() {
		first, err := newTemplateIdentity("schema-v1")
		Expect(err).NotTo(HaveOccurred())
		second, err := newTemplateIdentity("schema-v1")
		Expect(err).NotTo(HaveOccurred())
		changed, err := newTemplateIdentity("schema-v2")
		Expect(err).NotTo(HaveOccurred())

		Expect(first.key).To(Equal(second.key))
		Expect(first.key).NotTo(Equal(changed.key))
		Expect(first.metadata).To(HavePrefix(templateMetadataPrefix))
		name := templateDatabaseName(first, time.Unix(1_785_600_000, 0))
		Expect(name).To(HaveLen(maxIdentifier))
	})

	It("rejects an empty provisioner fingerprint", func() {
		_, err := newTemplateIdentity("  ")
		Expect(err).To(MatchError("dbtest: provisioner fingerprint is empty"))
	})

	It("validates both metadata and the digest embedded in a managed template name", func() {
		identity, err := newTemplateIdentity("schema-v1")
		Expect(err).NotTo(HaveOccurred())
		database := templateDatabase{
			name:     templateDatabaseName(identity, time.Unix(1_785_600_000, 0)),
			metadata: sql.NullString{String: identity.metadata, Valid: true},
		}

		key, managed := managedTemplateKey(database)
		Expect(managed).To(BeTrue())
		Expect(key).To(Equal(identity.key))
		database.name = strings.TrimSuffix(database.name, identity.key[:templateDigestLength]) +
			strings.Repeat("0", templateDigestLength)
		_, managed = managedTemplateKey(database)
		Expect(managed).To(BeFalse())
	})

	It("rejects provisioners in direct database mode before invoking them", func(ctx SpecContext) {
		GinkgoT().Setenv(EnvURL, "postgres://unused.example/target")
		GinkgoT().Setenv(EnvCreate, "false")
		provisioner := &recordingProvisioner{fingerprint: "schema-v1"}

		_, _, err := open(ctx, Options{Name: "direct", Provisioner: provisioner})

		Expect(err).To(MatchError(ContainSubstring("COMMONS_DB_CREATE=false")))
		Expect(provisioner.calls.Load()).To(BeZero())
	})
})
