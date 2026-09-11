package profiles

import (
	"context"
	"fmt"
	"net/http"

	"github.com/flanksource/clicky/entity"
	"github.com/flanksource/clicky/rpc"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/spf13/cobra"

	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
)

// nameOnlyVirtualStore answers to a profile's exact name and nothing else,
// which is how the snapshot manager and the record result registry behave.
type nameOnlyVirtualStore struct{ profile query.Profile }

func (s nameOnlyVirtualStore) List(context.Context) ([]query.Profile, error) {
	return []query.Profile{s.profile}, nil
}

func (s nameOnlyVirtualStore) Get(_ context.Context, name string) (query.Profile, error) {
	if name != s.profile.Name {
		return query.Profile{}, fmt.Errorf("profile %q not found", name)
	}
	return s.profile, nil
}

func (s nameOnlyVirtualStore) Peek(ctx context.Context, name string) (query.Profile, error) {
	return s.Get(ctx, name)
}

func (s nameOnlyVirtualStore) IsVirtual(name string) bool { return name == s.profile.Name }

func (nameOnlyVirtualStore) Save(context.Context, query.Profile) error {
	return fmt.Errorf("read-only")
}

func (nameOnlyVirtualStore) Update(context.Context, string, query.Profile, UpdateOptions) error {
	return fmt.Errorf("read-only")
}

func (nameOnlyVirtualStore) Delete(context.Context, string) error { return fmt.Errorf("read-only") }

var _ = Describe("the profile family over a virtual profile", func() {
	It("resolves the surface key the filter bar asks on to the virtual profile's name", func() {
		query.RegisterProvider(familyLookupMock{values: []string{"api", "payments"}})
		base, err := NewFileStore(GinkgoT().TempDir())
		Expect(err).ToNot(HaveOccurred())
		overlay, err := NewOverlayStore(base, nameOnlyVirtualStore{profile: lookupProfile("results/spans")})
		Expect(err).ToNot(HaveOccurred())
		service, err := New(Options{
			Store:      func() (Store, error) { return overlay, nil },
			Context:    func() dbcontext.Context { return dbcontext.New() },
			DecodeBody: func(_ context.Context, body map[string]any) (map[string]any, error) { return body, nil },
		})
		Expect(err).ToNot(HaveOccurred())
		service.RegisterFamily()
		DeferCleanup(func() { entity.UnregisterDynamicEntityFamily(profileFamilyName) })
		root := &cobra.Command{Use: "query"}
		root.AddCommand(&cobra.Command{Use: "version", Run: func(*cobra.Command, []string) {}})
		server := rpc.NewSwaggerServer(
			&rpc.ServeConfig{
				Title: "Query", Version: "0.1.0", SkipHealth: true,
				Executor: &rpc.ExecutorConfig{Enabled: true, SkipPreRun: true, PathPrefix: "/api/v1"},
			},
			root, &rpc.OpenAPIConfig{Title: "Query", Version: "0.1.0"},
		)
		mux := http.NewServeMux()
		server.RegisterRoutes(mux)

		response := get(mux, "/api/v1/profile/profile-results-spans?__lookup=filters", "application/json+clicky")
		Expect(response.Code).To(Equal(http.StatusOK), response.Body.String())
		Expect(response.Body.String()).To(ContainSubstring(`"payments"`))
	})
})
