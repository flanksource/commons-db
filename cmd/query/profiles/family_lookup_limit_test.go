package profiles

import (
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/flanksource/clicky/entity"
	"github.com/flanksource/clicky/rpc"
	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
	"github.com/samber/lo"
	"github.com/spf13/cobra"
)

// countingLookupMock records the limit it was handed. The provider is the far
// end of the chain a declared limit has to travel — profile → binding →
// DynamicFilter → clicky's lookup core → here — so asserting on what arrives is
// the only check that covers every hop.
type countingLookupMock struct {
	values    []string
	lastLimit *int64
}

func newCountingLookupMock(distinct int) countingLookupMock {
	values := make([]string, distinct)
	for i := range values {
		values[i] = fmt.Sprintf("tenant-%03d", i)
	}
	return countingLookupMock{values: values, lastLimit: new(int64)}
}

// mysql, because SupportsNativeFilters is a fixed list and a column filter
// exists at all only for the provider types on it.
func (countingLookupMock) Type() string { return "mysql" }

func (countingLookupMock) Execute(dbcontext.Context, query.ProviderRequest) ([]query.Row, error) {
	return nil, nil
}

func (m countingLookupMock) LookupFilterValues(
	_ dbcontext.Context, _ query.ProviderRequest, _ query.ColumnFilterBinding, search string, limit int,
) ([]query.FilterOption, *query.Total, error) {
	atomic.StoreInt64(m.lastLimit, int64(limit))

	matched := make([]query.FilterOption, 0, len(m.values))
	for _, value := range m.values {
		if search == "" || strings.Contains(value, search) {
			matched = append(matched, query.FilterOption{Value: value})
		}
	}
	// The total is the whole matching set; only the returned page is capped —
	// which is what lets the response say how much is behind the head.
	total := query.Total{Value: int64(len(matched)), Exact: true}
	if limit > 0 && len(matched) > limit {
		matched = matched[:limit]
	}
	return matched, &total, nil
}

func limitProfile(name string, filter *query.ColumnFilterDef) query.Profile {
	return query.Profile{
		Name:     name,
		Provider: query.ProviderConfig{Type: "mysql"},
		Query:    "select tenant from orders",
		Columns:  []query.ColumnDef{{Name: "tenant", Type: query.ColumnTypeString, Filter: filter}},
	}
}

func newCountingFamilyTest(t *testing.T, distinct int, profiles ...query.Profile) (http.Handler, countingLookupMock) {
	t.Helper()
	provider := newCountingLookupMock(distinct)
	// The registry is global, so the real provider is put back rather than left
	// shadowed for whatever runs next in this package.
	real, err := query.GetProvider(provider.Type())
	if err != nil {
		t.Fatalf("GetProvider(%q): %v", provider.Type(), err)
	}
	query.RegisterProvider(provider)
	t.Cleanup(func() { query.RegisterProvider(real) })

	service, _ := newProfileServiceTest(t, profiles...)
	service.RegisterFamily()
	t.Cleanup(func() { entity.UnregisterDynamicEntityFamily(profileFamilyName) })

	root := &cobra.Command{Use: "query"}
	root.AddCommand(&cobra.Command{Use: "version", Run: func(*cobra.Command, []string) {}})
	server := rpc.NewSwaggerServer(
		&rpc.ServeConfig{
			Title: "Query", Version: "0.1.0", SkipHealth: true,
			Executor: &rpc.ExecutorConfig{Enabled: true, SkipPreRun: true, PathPrefix: "/api/v1"},
		},
		root,
		&rpc.OpenAPIConfig{Title: "Query", Version: "0.1.0"},
	)
	mux := http.NewServeMux()
	server.RegisterRoutes(mux)
	return mux, provider
}

// A profile that says nothing gets a head small enough to read, not the largest
// one the wire will carry.
func TestProfileFilterDefaultsToAReadableHead(t *testing.T) {
	handler, provider := newCountingFamilyTest(t, 400, limitProfile("Orders", nil))

	filters := requestLookup(t, handler, "/api/v1/profile/profile-orders?__lookup=filters")
	tenant := filters["filter.tenant"]

	if got := atomic.LoadInt64(provider.lastLimit); got != int64(query.DefaultFilterLookupLimit) {
		t.Fatalf("provider was asked for %d values, want the default %d", got, query.DefaultFilterLookupLimit)
	}
	if len(tenant.Options) != query.DefaultFilterLookupLimit {
		t.Fatalf("filter offers %d options, want %d", len(tenant.Options), query.DefaultFilterLookupLimit)
	}
	if !tenant.Truncated || tenant.Total != 400 {
		t.Fatalf("expected truncated=true total=400 so the bar can offer a search, got truncated=%v total=%d",
			tenant.Truncated, tenant.Total)
	}
}

func TestProfileFilterHonoursItsDeclaredLimit(t *testing.T) {
	handler, provider := newCountingFamilyTest(t, 400,
		limitProfile("Orders", &query.ColumnFilterDef{Limit: lo.ToPtr(3)}))

	tenant := requestLookup(t, handler, "/api/v1/profile/profile-orders?__lookup=filters")["filter.tenant"]

	if got := atomic.LoadInt64(provider.lastLimit); got != 3 {
		t.Fatalf("provider was asked for %d values, want the declared 3", got)
	}
	if len(tenant.Options) != 3 || !tenant.Truncated {
		t.Fatalf("expected 3 options reported truncated, got %d truncated=%v", len(tenant.Options), tenant.Truncated)
	}
}

// A set that fits under the head is the case where no typeahead is needed, and
// saying so is what keeps the control a plain list.
func TestProfileFilterThatFitsIsNotTruncated(t *testing.T) {
	handler, _ := newCountingFamilyTest(t, 4, limitProfile("Orders", nil))

	tenant := requestLookup(t, handler, "/api/v1/profile/profile-orders?__lookup=filters")["filter.tenant"]

	if len(tenant.Options) != 4 {
		t.Fatalf("filter offers %d options, want all 4", len(tenant.Options))
	}
	if tenant.Truncated {
		t.Fatalf("expected truncated=false when every value fits in the head")
	}
}

// The search is where a declared limit earns its keep: it bounds the typeahead
// too, and an overflowing search still reports how much it left behind.
func TestProfileFilterSearchStaysWithinTheDeclaredLimit(t *testing.T) {
	handler, provider := newCountingFamilyTest(t, 400,
		limitProfile("Orders", &query.ColumnFilterDef{Limit: lo.ToPtr(5)}))

	tenant := requestLookup(t, handler,
		"/api/v1/profile/profile-orders?__lookup=filters&__lookup_filter=filter.tenant&__lookup_q=tenant-1")["filter.tenant"]

	if got := atomic.LoadInt64(provider.lastLimit); got != 5 {
		t.Fatalf("search asked the provider for %d values, want the declared 5", got)
	}
	// The values are zero-padded, so "tenant-1" matches tenant-100..tenant-199 —
	// 100 of them, served 5 at a time.
	if len(tenant.Options) != 5 {
		t.Fatalf("search returned %d options, want 5", len(tenant.Options))
	}
	if !tenant.Truncated || tenant.Total != 100 {
		t.Fatalf("expected a truncated search reporting 100 matches, got truncated=%v total=%d",
			tenant.Truncated, tenant.Total)
	}
}
