package profiles

import (
	"context"
	"encoding/json"

	"github.com/flanksource/commons-db/dbtest"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/types"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"gorm.io/gorm"
)

var _ = Describe("database profile updates", func() {
	var (
		ctx   context.Context
		db    *gorm.DB
		store *DBStore
	)

	BeforeEach(func() {
		ctx = context.Background()
		db = dbtest.ForGinkgo(dbtest.Options{Name: "profiles_update"}).Gorm()
		Expect(db.Migrator().DropTable(&profileRecord{})).To(Succeed())
		Expect(db.Exec(`CREATE TABLE profiles (
			id text PRIMARY KEY,
			name text NOT NULL UNIQUE,
			namespace text,
			spec jsonb NOT NULL,
			created_at timestamptz DEFAULT now(),
			updated_at timestamptz DEFAULT now()
		)`).Error).To(Succeed())
		var err error
		store, err = NewDBStore(db)
		Expect(err).NotTo(HaveOccurred())
	})

	It("renames the source record and cascades imports in one transaction", func() {
		source := sampleProfile("Database Source")
		dependent := sampleProfile("Database Dependent")
		dependent.Imports = []string{source.Name, source.Name}
		seedProfileRecord(db, source)
		seedProfileRecord(db, dependent)

		renamed := source
		renamed.Name = "Database Renamed"
		Expect(store.Update(ctx, source.Name, renamed, UpdateOptions{})).To(Succeed())

		profiles, err := store.List(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(names(profiles)).To(ConsistOf("Database Dependent", "Database Renamed"))
		updatedDependent, err := store.Get(ctx, dependent.Name)
		Expect(err).NotTo(HaveOccurred())
		Expect(updatedDependent.Imports).To(Equal([]string{renamed.Name}))
	})

	It("rolls back a rejected conflicting rename", func() {
		source := sampleProfile("Database Source")
		target := sampleProfile("Database Target")
		seedProfileRecord(db, source)
		seedProfileRecord(db, target)
		renamed := source
		renamed.Name = target.Name

		Expect(store.Update(ctx, source.Name, renamed, UpdateOptions{})).To(
			MatchError(ContainSubstring(ProfileNameConflictCode)),
		)
		profiles, err := store.List(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(names(profiles)).To(ConsistOf(source.Name, target.Name))
	})
})

func seedProfileRecord(db *gorm.DB, profile query.Profile) {
	data, err := json.Marshal(profile)
	Expect(err).NotTo(HaveOccurred())
	Expect(db.Create(&profileRecord{
		ID: uuid.New(), Name: profile.Name, Namespace: profile.Namespace, Spec: types.JSON(data),
	}).Error).To(Succeed())
}
