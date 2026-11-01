package profiles

import (
	"context"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/query/profilestore"
)

// profileRefProvider answers `ref:<name>` by reading profiles.<name> the way
// metric SQL does (query.ProfileTables), and any other query with one row
// naming it.
type profileRefProvider struct{}

func (profileRefProvider) Type() string { return "profile-ref-mock" }

func (profileRefProvider) Execute(ctx dbcontext.Context, req query.ProviderRequest) ([]query.Row, error) {
	name, ok := strings.CutPrefix(req.Query, "ref:")
	if !ok {
		return []query.Row{{"from": req.Query, "namespace": req.Params["namespace"]}}, nil
	}
	table, err := query.ProfileTables(ctx, req.Params)(name)
	return table.Rows, err
}

var _ = Describe("profiles.<name> on every service read", func() {
	var (
		service *Service
		reads   []string
	)
	refProfile := func(name, q string) query.Profile {
		return query.Profile{Name: name, Query: q, Provider: query.ProviderConfig{Type: "profile-ref-mock"}, Params: []query.ParamDef{{Name: "namespace"}}}
	}

	BeforeEach(func() {
		reads = nil
		query.RegisterProvider(profileRefProvider{})
		store, err := NewFileStore(GinkgoT().TempDir())
		Expect(err).ToNot(HaveOccurred())
		for _, p := range []query.Profile{refProfile("parent", "ref:child"), refProfile("child", "leaf"), refProfile("a", "ref:b"), refProfile("b", "ref:a")} {
			Expect(store.Save(context.Background(), p)).To(Succeed())
		}
		service, err = New(Options{
			Store:      func() (profilestore.Store, error) { return store, nil },
			Context:    func() dbcontext.Context { return dbcontext.New() },
			DecodeBody: func(_ context.Context, body map[string]any) (map[string]any, error) { return body, nil },
			BeforeExecute: func(_ context.Context, batch []profilestore.ReadRequest) (func(), error) {
				for _, read := range batch {
					reads = append(reads, read.Profile.Name)
				}
				return func() {}, nil
			},
		})
		Expect(err).ToNot(HaveOccurred())
	})

	It("lists a profile that reads another, preparing the child under the caller", func() {
		rows, err := service.executeRows(context.Background(), "parent", map[string]string{"namespace": "acme"})
		Expect(err).ToNot(HaveOccurred())
		Expect(rows).To(Equal([]map[string]any{{"from": "leaf", "namespace": "acme"}}))
		Expect(reads).To(Equal([]string{"parent", "child"}))
	})

	It("still refuses a reference cycle with its path", func() {
		_, err := service.executeRows(context.Background(), "a", nil)
		Expect(err).To(MatchError(ContainSubstring("profile reference cycle: a -> b -> a")))
	})
})
