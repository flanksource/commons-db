// Stores and tenants for the specs: an in-process kv source, a router that
// gives each tenant its own, and the settings and Open a server would use.
package recordresultstest

import (
	"context"
	"errors"
	"time"

	"github.com/flanksource/clicky/cache"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"

	"github.com/flanksource/commons-db/recordstore"
	"github.com/flanksource/commons-db/recordstore/kv"
	"github.com/flanksource/commons-db/recordstore/recordresults"
)

// NewKV is an in-process kv source resolving kinds through schemas.
func NewKV(schemas *recordstore.Schemas) *kv.Backend {
	source, err := kv.New(kv.Options{
		Store: cache.NewMemory(), Prefix: "records", Schema: schemas.Kind, TTL: time.Hour, MaxChunkBytes: 1 << 20,
	})
	gomega.Expect(err).ToNot(gomega.HaveOccurred())
	return source
}

// tenantKey carries the tenant a request runs for, the way a server's
// middleware puts it on the request context.
type tenantKey struct{}

// WithTenant is ctx running for tenant.
func WithTenant(ctx context.Context, tenant string) context.Context {
	return context.WithValue(ctx, tenantKey{}, tenant)
}

// ForTenant is a background context running for tenant.
func ForTenant(tenant string) context.Context {
	return WithTenant(context.Background(), tenant)
}

// TenantOf is the tenant ctx runs for, the route a Router resolves.
func TenantOf(ctx context.Context) (string, error) {
	tenant, _ := ctx.Value(tenantKey{}).(string)
	if tenant == "" {
		return "", errors.New("the request carries no tenant")
	}
	return tenant, nil
}

// RegisterSampleEvents registers SampleEvent as the sample_event result type.
func RegisterSampleEvents(registry *recordresults.Registry) error {
	return recordresults.RegisterResultType(registry, recordresults.ResultType[SampleEvent]{
		Kind: "sample_event", Title: "Sample events", TimeColumn: "at",
	})
}

// KVRouter routes each tenant to an in-process kv store of its own, resolving
// kinds through schemas.
func KVRouter(schemas *recordstore.Schemas) *recordstore.Router {
	router, err := recordstore.NewRouter(recordstore.RouterOptions{
		Route: TenantOf,
		Open: func(context.Context, string) (recordstore.Backend, error) {
			return NewKV(schemas), nil
		},
	})
	gomega.Expect(err).ToNot(gomega.HaveOccurred())
	return router
}

// LocalSettings are settings for backend in a spec's own temporary directory.
func LocalSettings(backend recordstore.BackendKind) recordstore.Settings {
	return recordstore.Settings{
		Prefix: "spec", Backend: backend, Dir: ginkgo.GinkgoT().TempDir(), TTL: time.Hour,
		NDJSONMaxBytes: 1 << 20, NDJSONKeepStreams: 5,
	}
}

// OpenResults opens options and closes the results when the spec ends.
func OpenResults(options recordresults.OpenOptions) *recordresults.Results {
	results, err := recordresults.Open(options)
	gomega.Expect(err).ToNot(gomega.HaveOccurred())
	ginkgo.DeferCleanup(results.Close)
	return results
}
