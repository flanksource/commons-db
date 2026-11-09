package catalog

import (
	"bytes"
	"context"
	dbcontext "github.com/flanksource/commons-db/context"
	commonsdb "github.com/flanksource/commons-db/db"
	"github.com/flanksource/commons-db/models"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"gocloud.dev/secrets/localsecrets"
	"testing"
)

func TestCatalog(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Encrypted connection catalog")
}

var _ = Describe("encrypted catalog", func() {
	It("encrypts complete credentials, survives reopening and rejects other organizations", func() {
		db, err := commonsdb.NewGorm("sqlite://"+GinkgoT().TempDir()+"/catalog.db", nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(db.AutoMigrate(&Record{})).To(Succeed())
		keeper := localsecrets.NewKeeper([32]byte{1})
		DeferCleanup(keeper.Close)
		org := uuid.New()
		opts := Options{DB: db, OrgID: org, Keeper: keeper, KeyID: "test-key"}
		store, err := New(opts)
		Expect(err).NotTo(HaveOccurred())
		input := &models.Connection{Name: "example", Type: "http", URL: "https://example.com", Password: "credential-value", Properties: map[string]string{"token": "property-secret"}}
		saved, err := store.SaveConnection(context.Background(), input)
		Expect(err).NotTo(HaveOccurred())
		var row Record
		Expect(db.First(&row).Error).To(Succeed())
		Expect(bytes.Contains(row.Payload, []byte(input.Password))).To(BeFalse())
		reopened, err := New(opts)
		Expect(err).NotTo(HaveOccurred())
		found, err := reopened.ResolveConnection(dbcontext.New(), saved.ID.String())
		Expect(err).NotTo(HaveOccurred())
		Expect(found.Password).To(Equal(input.Password))
		Expect(found.Properties).To(Equal(input.Properties))
		other, err := New(Options{DB: db, OrgID: uuid.New(), Keeper: keeper, KeyID: "test-key"})
		Expect(err).NotTo(HaveOccurred())
		_, err = other.ResolveConnection(dbcontext.New(), saved.ID.String())
		Expect(err).To(HaveOccurred())
		Expect(store.DeleteConnection(context.Background(), saved.ID.String())).To(Succeed())
		listed, err := store.ListConnections()
		Expect(err).NotTo(HaveOccurred())
		Expect(listed).To(BeEmpty())
	})
	It("rejects ciphertext moved between owners or opened with the wrong key", func() {
		db, err := commonsdb.NewGorm("sqlite://"+GinkgoT().TempDir()+"/catalog.db", nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(db.AutoMigrate(&Record{})).To(Succeed())
		keeper := localsecrets.NewKeeper([32]byte{1})
		DeferCleanup(keeper.Close)
		org := uuid.New()
		store, err := New(Options{DB: db, OrgID: org, Keeper: keeper, KeyID: "original"})
		Expect(err).NotTo(HaveOccurred())
		saved, err := store.SaveConnection(context.Background(), &models.Connection{Name: "example", Type: "http", Password: "secret"})
		Expect(err).NotTo(HaveOccurred())
		wrong := localsecrets.NewKeeper([32]byte{2})
		DeferCleanup(wrong.Close)
		bad, err := New(Options{DB: db, OrgID: org, Keeper: wrong, KeyID: "original"})
		Expect(err).NotTo(HaveOccurred())
		_, err = bad.ResolveConnection(dbcontext.New(), saved.ID.String())
		Expect(err).To(HaveOccurred())
		nextOrg := uuid.New()
		Expect(db.Model(&Record{}).Where("id = ?", saved.ID).Update("org_id", nextOrg).Error).To(Succeed())
		moved, err := New(Options{DB: db, OrgID: nextOrg, Keeper: keeper, KeyID: "original"})
		Expect(err).NotTo(HaveOccurred())
		_, err = moved.ResolveConnection(dbcontext.New(), saved.ID.String())
		Expect(err).To(HaveOccurred())
	})
})
