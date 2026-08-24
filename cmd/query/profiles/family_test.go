package profiles

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/flanksource/clicky/entity"
	"github.com/flanksource/clicky/rpc"
	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
	"github.com/spf13/cobra"
)

// familyLookupMock only has to answer "what values does this field have". The
// lookup path never runs the profile's query, so Execute is never reached.
type familyLookupMock struct{ values []string }

func (familyLookupMock) Type() string { return "clickhouse" }

func (familyLookupMock) Execute(dbcontext.Context, query.ProviderRequest) ([]query.Row, error) {
	return nil, nil
}

func (m familyLookupMock) LookupFilterValues(
	_ dbcontext.Context, _ query.ProviderRequest, _ query.ColumnFilterBinding, search string, _ int,
) ([]query.FilterOption, *query.Total, error) {
	matches := make([]query.FilterOption, 0, len(m.values))
	for _, value := range m.values {
		if search == "" || strings.Contains(value, search) {
			matches = append(matches, query.FilterOption{Value: value})
		}
	}
	return matches, &query.Total{Value: int64(len(matches)), Exact: true}, nil
}

// lookupProfile has one column of each shape the filter bar has to distinguish:
// a value selection the backend enumerates, and a range that is typed rather
// than picked.
func lookupProfile(name string) query.Profile {
	return query.Profile{
		Name:     name,
		Provider: query.ProviderConfig{Type: "clickhouse"},
		Query:    "select service, latency_ms from spans",
		Columns: []query.ColumnDef{
			{Name: "service", Type: query.ColumnTypeString},
			{Name: "latency_ms", Type: query.ColumnTypeNumber},
		},
	}
}

// newProfileServiceTest builds a service over a store holding profiles.
func newProfileServiceTest(t *testing.T, profiles ...query.Profile) (*Service, Store) {
	t.Helper()
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	for _, profile := range profiles {
		if err := store.Save(context.Background(), profile); err != nil {
			t.Fatalf("Save %q: %v", profile.Name, err)
		}
	}
	service, err := New(Options{
		Store:      func() (Store, error) { return store, nil },
		Context:    func() dbcontext.Context { return dbcontext.New() },
		DecodeBody: func(_ context.Context, body map[string]any) (map[string]any, error) { return body, nil },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return service, store
}

// newProfileFamilyTest builds the server the way `query serve` does: the
// family's single route is registered once, up front, and every profile below is
// resolved through it per request.
func newProfileFamilyTest(t *testing.T, profiles ...query.Profile) (http.Handler, Store) {
	t.Helper()
	query.RegisterProvider(familyLookupMock{values: []string{"api", "payments", "payroll"}})

	service, store := newProfileServiceTest(t, profiles...)
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
	return mux, store
}

// lookupFilter is the part of clicky's lookup envelope the filter bar reads.
// Truncated and Total are what decide between a list to scroll and a box to type
// into, so they are as load-bearing as the options themselves.
type lookupFilter struct {
	Label     string                    `json:"label"`
	Type      string                    `json:"type"`
	Multi     bool                      `json:"multi"`
	Options   map[string]map[string]any `json:"options"`
	Truncated bool                      `json:"truncated"`
	Total     int                       `json:"total"`
}

func requestLookup(t *testing.T, handler http.Handler, target string) map[string]lookupFilter {
	t.Helper()
	response := get(handler, target, "application/json+clicky")
	if response.Code != http.StatusOK {
		t.Fatalf("GET %s status=%d body=%s", target, response.Code, response.Body.String())
	}
	var body struct {
		Filters map[string]lookupFilter `json:"filters"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("lookup body is not JSON: %v (%s)", err, response.Body.String())
	}
	return body.Filters
}

func optionKeys(filter lookupFilter) []string {
	keys := make([]string, 0, len(filter.Options))
	for key := range filter.Options {
		keys = append(keys, key)
	}
	return keys
}

// The bug this family exists to fix: x-clicky-lookup.url points every profile's
// filter bar at {prefix}/profile/<key>, and nothing was registered there, so the
// request fell through to a bare mux and 404'd — leaving the bar permanently
// empty.
func TestProfileFamilyAnswersTheFilterLookupTheSurfacePointsAt(t *testing.T) {
	handler, _ := newProfileFamilyTest(t, lookupProfile("Spans"))

	filters := requestLookup(t, handler, "/api/v1/profile/profile-spans?__lookup=filters")

	if len(filters) != 2 {
		t.Fatalf("lookup returned %d filters, want one per filterable column: %v", len(filters), filters)
	}
	service, ok := filters["filter.service"]
	if !ok {
		t.Fatalf("no filter keyed filter.service in %v", filters)
	}
	if service.Type != "multi-filter" || !service.Multi {
		t.Errorf("filter.service type=%q multi=%v, want a multi-filter", service.Type, service.Multi)
	}
	for _, want := range []string{"api", "payments", "payroll"} {
		if _, offered := service.Options[want]; !offered {
			t.Errorf("filter.service does not offer %q: %v", want, optionKeys(service))
		}
	}
	// A range is typed, not chosen from. It still has to appear, because the
	// filter is what tells the browser which control to render.
	latency, ok := filters["filter.latency_ms"]
	if !ok {
		t.Fatalf("no filter keyed filter.latency_ms in %v", filters)
	}
	if latency.Type != "number" {
		t.Errorf("filter.latency_ms type=%q, want number", latency.Type)
	}
	if len(latency.Options) != 0 {
		t.Errorf("a range filter offers %v, want nothing to pick from", optionKeys(latency))
	}
}

// Typing into the filter bar sends __lookup_filter + __lookup_q, which has to
// narrow that one filter server-side rather than returning the head set again.
func TestProfileFamilyLookupSearchesTheNamedFilter(t *testing.T) {
	handler, _ := newProfileFamilyTest(t, lookupProfile("Spans"))

	filters := requestLookup(t, handler,
		"/api/v1/profile/profile-spans?__lookup=filters&__lookup_filter=filter.service&__lookup_q=pay")

	service := filters["filter.service"]
	if len(service.Options) != 2 {
		t.Fatalf("search returned %v, want only the payments/payroll matches", optionKeys(service))
	}
	for _, want := range []string{"payments", "payroll"} {
		if _, matched := service.Options[want]; !matched {
			t.Errorf("search dropped %q: %v", want, optionKeys(service))
		}
	}
	if _, matched := service.Options["api"]; matched {
		t.Error(`"api" matched the query "pay"`)
	}
}

// The whole reason a family replaces a per-profile registration: the route is
// registered once, and the instance behind it is resolved per request. A profile
// saved after the routes were built has to be reachable without a restart.
func TestProfileFamilyResolvesAProfileCreatedAfterTheRoutesWereBuilt(t *testing.T) {
	handler, store := newProfileFamilyTest(t)

	if response := get(handler, "/api/v1/profile/profile-late?__lookup=filters", ""); response.Code != http.StatusNotFound {
		t.Fatalf("status=%d, want 404 before the profile exists", response.Code)
	}
	if err := store.Save(context.Background(), lookupProfile("Late")); err != nil {
		t.Fatal(err)
	}

	filters := requestLookup(t, handler, "/api/v1/profile/profile-late?__lookup=filters")
	if _, ok := filters["filter.service"]; !ok {
		t.Fatalf("a profile created after startup is not routable: %v", filters)
	}
}

// A name nothing resolves is a 404 in the shared error shape, so a client can
// branch on the code rather than string-matching the message.
func TestProfileFamilyUnknownProfileIsAStructuredNotFound(t *testing.T) {
	handler, _ := newProfileFamilyTest(t, lookupProfile("Spans"))

	response := get(handler, "/api/v1/profile/profile-missing?__lookup=filters", "")
	if response.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s, want 404", response.Code, response.Body.String())
	}
	var body entity.StatusError
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("error body is not JSON: %v (%s)", err, response.Body.String())
	}
	if body.Code != "not_found" || body.Message == "" {
		t.Fatalf("error = %+v, want a not_found code and a readable message", body)
	}
	if origin := response.Header().Get("Access-Control-Allow-Origin"); origin != "*" {
		t.Errorf("Access-Control-Allow-Origin=%q on a 404 a browser has to read", origin)
	}
}

// The surface key is the profile's identity everywhere outside the store: the
// last path segment, x-clicky.surfaces[].key, and the frontend route. clicky
// derives all three from DynamicEntitySpec.Name, so it is the key and not the
// profile's own name.
func TestProfileFamilySurfaceKeepsTheProfilePrefixedKey(t *testing.T) {
	service, _ := newProfileServiceTest(t, lookupProfile("jms.incoming"))

	surfaces, err := service.listSurfaces(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(surfaces) != 1 {
		t.Fatalf("listed %d surfaces, want 1", len(surfaces))
	}
	surface := surfaces[0]
	if surface.Name != "profile-jms-incoming" {
		t.Errorf("surface name = %q, want the profile-<slug> key the frontend routes on", surface.Name)
	}
	if surface.Parent != profileSurfaceParent {
		t.Errorf("surface parent = %q, want %q", surface.Parent, profileSurfaceParent)
	}
	if surface.Title != "jms.incoming" {
		t.Errorf("surface title = %q, want the profile name", surface.Title)
	}
	// The title stays the full name for page headings; the path is what nests
	// the sidebar, and the key has already flattened every separator.
	if surface.Path != "jms/incoming" {
		t.Errorf("surface path = %q, want jms/incoming", surface.Path)
	}
}
