package cli

import (
	"context"
	"github.com/flanksource/commons-db/cmd/query/connections"
	"github.com/flanksource/commons-db/connection/catalog"
	dbcontext "github.com/flanksource/commons-db/context"
	commonsdb "github.com/flanksource/commons-db/db"
	"github.com/flanksource/commons-db/query"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"gocloud.dev/secrets/localsecrets"
	"io"
)

var _ = Describe("encrypted connection CLI", func() {
	It("preserves encrypted credentials when editing a redacted connection", func() {
		db, err := commonsdb.NewGorm("sqlite://"+GinkgoT().TempDir()+"/catalog.db", nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(db.AutoMigrate(&catalog.Record{})).To(Succeed())
		keeper := localsecrets.NewKeeper([32]byte{1})
		DeferCleanup(keeper.Close)
		provider, err := catalog.New(catalog.Options{DB: db, OrgID: uuid.New(), Keeper: keeper, KeyID: "test"})
		Expect(err).NotTo(HaveOccurred())
		ctx := dbcontext.New().WithConnectionResolver(provider.ResolveConnection)
		service, err := connections.New(connections.Options{Virtual: provider, Context: func() dbcontext.Context { return ctx }, DecodeBody: func(_ context.Context, body map[string]any) (map[string]any, error) { return body, nil }, Profiles: func(context.Context) ([]query.Profile, error) { return nil, nil }})
		Expect(err).NotTo(HaveOccurred())
		run := func(args ...string) error {
			cmd := New(Options{Open: func(context.Context, string) (Backend, func() error, error) {
				return service, func() error { return nil }, nil
			}})
			cmd.SetOut(io.Discard)
			cmd.SetArgs(args)
			return cmd.Execute()
		}
		Expect(run("add", "http", "--name", "example", "--url", "https://example.com", "--password", "credential-value", "--properties", "token=property-secret")).To(Succeed())
		Expect(run("edit", "example", "--name", "renamed")).To(Succeed())
		raw, err := provider.ResolveConnection(ctx, "renamed")
		Expect(err).NotTo(HaveOccurred())
		Expect(raw.Password).To(Equal("credential-value"))
		Expect(raw.Properties["token"]).To(Equal("property-secret"))
		Expect(run("delete", "renamed")).To(Succeed())
		Expect(run("list")).To(Succeed())
	})
})
