package profiles

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"

	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("profile updates", func() {
	var (
		ctx   context.Context
		store *FileStore
	)

	BeforeEach(func() {
		ctx = context.Background()
		var err error
		store, err = NewFileStore(GinkgoT().TempDir())
		Expect(err).NotTo(HaveOccurred())
	})

	It("renames a profile and cascades imports without duplicates", func() {
		source := sampleProfile("Source Profile")
		dependent := sampleProfile("Dependent")
		dependent.Imports = []string{"Source Profile", "Other", "Source Profile"}
		Expect(store.Save(ctx, source)).To(Succeed())
		Expect(store.Save(ctx, dependent)).To(Succeed())

		renamed := source
		renamed.Name = "Renamed Profile"
		renamed.Query = "select renamed"
		Expect(store.Update(ctx, source.Name, renamed, UpdateOptions{})).To(Succeed())

		_, err := store.Get(ctx, source.Name)
		Expect(err).To(HaveOccurred())
		Expect(store.Get(ctx, renamed.Name)).To(Equal(renamed))
		updatedDependent, err := store.Get(ctx, dependent.Name)
		Expect(err).NotTo(HaveOccurred())
		Expect(updatedDependent.Imports).To(Equal([]string{"Renamed Profile", "Other"}))
	})

	It("rejects a conflicting rename without changing either profile", func() {
		source := sampleProfile("Source")
		target := sampleProfile("Target")
		target.Query = "select target"
		Expect(store.Save(ctx, source)).To(Succeed())
		Expect(store.Save(ctx, target)).To(Succeed())

		replacement := source
		replacement.Name = target.Name
		replacement.Query = "select replacement"
		err := store.Update(ctx, source.Name, replacement, UpdateOptions{})

		Expect(err).To(MatchError(ContainSubstring(ProfileNameConflictCode)))
		Expect(store.Get(ctx, source.Name)).To(Equal(source))
		Expect(store.Get(ctx, target.Name)).To(Equal(target))
	})

	It("replaces the destination only after explicit confirmation", func() {
		source := sampleProfile("Source")
		target := sampleProfile("Target")
		dependent := sampleProfile("Dependent")
		dependent.Imports = []string{"Source", "Target"}
		Expect(store.Save(ctx, source)).To(Succeed())
		Expect(store.Save(ctx, target)).To(Succeed())
		Expect(store.Save(ctx, dependent)).To(Succeed())

		replacement := source
		replacement.Name = target.Name
		replacement.Query = "select replacement"
		Expect(store.Update(ctx, source.Name, replacement, UpdateOptions{ReplaceExisting: true})).To(Succeed())

		profiles, err := store.List(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(names(profiles)).To(ConsistOf("Dependent", "Target"))
		Expect(store.Get(ctx, target.Name)).To(Equal(replacement))
		updatedDependent, err := store.Get(ctx, dependent.Name)
		Expect(err).NotTo(HaveOccurred())
		Expect(updatedDependent.Imports).To(Equal([]string{"Target"}))
	})

	It("treats a canonical route slug collision as a name conflict", func() {
		source := sampleProfile("Source")
		target := sampleProfile("Existing Profile")
		Expect(store.Save(ctx, source)).To(Succeed())
		Expect(store.Save(ctx, target)).To(Succeed())

		renamed := source
		renamed.Name = "existing.profile"
		Expect(store.Update(ctx, source.Name, renamed, UpdateOptions{})).To(
			MatchError(ContainSubstring(ProfileNameConflictCode)),
		)
	})

	It("passes explicit replacement through the update service without persisting it", func() {
		source := sampleProfile("Source")
		target := sampleProfile("Target")
		Expect(store.Save(ctx, source)).To(Succeed())
		Expect(store.Save(ctx, target)).To(Succeed())
		service, err := New(Options{
			Store:      func() (Store, error) { return store, nil },
			Context:    func() dbcontext.Context { return dbcontext.New() },
			DecodeBody: func(_ context.Context, body map[string]any) (map[string]any, error) { return body, nil },
		})
		Expect(err).NotTo(HaveOccurred())
		replacement := source
		replacement.Name = target.Name
		body := profileBody(replacement)
		body["replaceExisting"] = true

		updated, err := service.Save(ctx, body, source.Name)

		Expect(err).NotTo(HaveOccurred())
		Expect(updated).To(Equal(replacement))
		profiles, err := store.List(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(names(profiles)).To(Equal([]string{"Target"}))
		data, err := os.ReadFile(filepath.Join(store.Dir, "target.yaml"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(data)).NotTo(ContainSubstring("replaceExisting"))
	})
})

func profileBody(profile query.Profile) map[string]any {
	data, err := json.Marshal(profile)
	Expect(err).NotTo(HaveOccurred())
	var body map[string]any
	Expect(json.Unmarshal(data, &body)).To(Succeed())
	return body
}
