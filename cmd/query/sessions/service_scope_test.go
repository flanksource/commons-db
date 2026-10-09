package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"time"

	"github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	profilepkg "github.com/flanksource/commons-db/cmd/query/profiles"
	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
)

const scopeEnvironmentLabel = "environment"

type scopeEnvironmentKey struct{}

func withEnvironment(ctx context.Context, environment string) context.Context {
	return context.WithValue(ctx, scopeEnvironmentKey{}, environment)
}

func environmentOf(ctx context.Context) (string, error) {
	environment, ok := ctx.Value(scopeEnvironmentKey{}).(string)
	if !ok {
		return "", errors.New("request names no environment")
	}
	return environment, nil
}

// environmentStores routes every record to its environment's own store, as a
// host serving several environments does: the store is scoped, so only the
// registry's live sessions can cross environments.
type environmentStores struct {
	mu     sync.Mutex
	stores map[string]*KVStore
}

func (s *environmentStores) storeFor(ctx context.Context) (*KVStore, error) {
	environment, err := environmentOf(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if store, ok := s.stores[environment]; ok {
		return store, nil
	}
	store, err := NewMemoryKVStore(KVStoreOptions{})
	if err != nil {
		return nil, err
	}
	s.stores[environment] = store
	return store, nil
}

func (s *environmentStores) Begin(ctx context.Context, rec query.SessionRecord) error {
	store, err := s.storeFor(ctx)
	if err != nil {
		return err
	}
	return store.Begin(ctx, rec)
}

func (s *environmentStores) Update(ctx context.Context, id string, status query.SessionStatus) error {
	store, err := s.storeFor(ctx)
	if err != nil {
		return err
	}
	return store.Update(ctx, id, status)
}

func (s *environmentStores) Get(ctx context.Context, id string) (query.SessionRecord, bool, error) {
	store, err := s.storeFor(ctx)
	if err != nil {
		return query.SessionRecord{}, false, err
	}
	return store.Get(ctx, id)
}

func (s *environmentStores) List(ctx context.Context, filter query.SessionFilter) (query.SessionPage, error) {
	store, err := s.storeFor(ctx)
	if err != nil {
		return query.SessionPage{}, err
	}
	return store.List(ctx, filter)
}

func (s *environmentStores) Lineage(ctx context.Context, ids []string) (map[string][]string, error) {
	store, err := s.storeFor(ctx)
	if err != nil {
		return nil, err
	}
	return store.Lineage(ctx, ids)
}

type scopedHarness struct {
	handler  *sessionHandler
	registry *query.SessionRegistry
}

func newScopedHarness(sessions query.SessionStore) *scopedHarness {
	h := &scopedHarness{}
	h.registry = query.NewSessionRegistry(query.RegistryOptions{
		Store: sessions,
		Scope: &query.SessionScope{Label: scopeEnvironmentLabel, Of: environmentOf},
	})
	ginkgo.DeferCleanup(func() { Expect(h.registry.StopAll(context.Background())).To(Succeed()) })
	profiles, err := profilepkg.NewFileStore(ginkgo.GinkgoT().TempDir())
	Expect(err).ToNot(HaveOccurred())
	h.handler = newSessionHandler(sessionHandlerOptions{
		Prefix: "/api/v1", Ctx: dbcontext.New(), Store: profiles, Registry: h.registry, Sessions: sessions, Next: &nextMarker{},
	})
	return h
}

// trackIn starts a live jvm_trace capture in environment.
func (h *scopedHarness) trackIn(environment string) *query.Session {
	stopAt := time.Now().Add(10 * time.Minute)
	session, err := h.registry.Track(withEnvironment(context.Background(), environment), query.TrackOptions{
		Profile: jvmProfile, Kind: query.KindCapture, StopAt: &stopAt,
	})
	Expect(err).ToNot(HaveOccurred())
	session.OnStop(func(string) { session.Finish(query.FinishUpdate{}) })
	Expect(session.Running(query.RunningUpdate{Handle: "probe-" + environment})).To(Succeed())
	return session
}

func (h *scopedHarness) doIn(environment, method, path string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, nil)
	request = request.WithContext(withEnvironment(request.Context(), environment))
	response := httptest.NewRecorder()
	h.handler.ServeHTTP(response, request)
	return response
}

func (h *scopedHarness) listIn(environment, query string) []string {
	response := h.doIn(environment, http.MethodGet, "/api/v1/sessions?"+query)
	Expect(response.Code).To(Equal(http.StatusOK), response.Body.String())
	var body sessionListBody
	Expect(json.Unmarshal(response.Body.Bytes(), &body)).To(Succeed())
	return infoIDs(body.Items)
}

var _ = ginkgo.Describe("sessions API with an environment-scoped registry", func() {
	for _, variant := range []struct {
		name  string
		store func() query.SessionStore
	}{
		{"with an environment-routed store", func() query.SessionStore { return &environmentStores{stores: map[string]*KVStore{}} }},
		{"with no store", func() query.SessionStore { return nil }},
	} {
		ginkgo.Context(variant.name, func() {
			var (
				h    *scopedHarness
				a, b *query.Session
			)
			ginkgo.BeforeEach(func() {
				h = newScopedHarness(variant.store())
				a, b = h.trackIn("env-a"), h.trackIn("env-b")
			})

			ginkgo.It("lists, counts and looks up only the live sessions of the request's environment", func() {
				Expect(h.listIn("env-b", "")).To(Equal([]string{b.ID()}))
				Expect(h.listIn("env-a", "")).To(Equal([]string{a.ID()}))
				Expect(h.listIn("env-b", "profile="+jvmProfile)).To(Equal([]string{b.ID()}))
			})

			ginkgo.It("answers another environment's live session 404 on info, events and stop, and leaves it running", func() {
				for _, route := range []struct{ method, path string }{
					{http.MethodGet, "/api/v1/sessions/" + a.ID()},
					{http.MethodGet, "/api/v1/sessions/" + a.ID() + "/events"},
					{http.MethodGet, "/api/v1/sessions/" + a.ID() + "/result"},
					{http.MethodPost, "/api/v1/sessions/" + a.ID() + "/stop"},
					{http.MethodPost, "/api/v1/sessions/" + a.ID() + "/extend?duration=1m"},
				} {
					response := h.doIn("env-b", route.method, route.path)
					Expect(response.Code).To(Equal(http.StatusNotFound), "%s %s: %s", route.method, route.path, response.Body.String())
					Expect(response.Body.String()).To(ContainSubstring(errSessionNotFound(a.ID()).Error()))
				}
				Expect(a.Snapshot().State).To(Equal(query.SessionRunning))

				response := h.doIn("env-a", http.MethodPost, "/api/v1/sessions/"+a.ID()+"/stop")
				Expect(response.Code).To(Equal(http.StatusOK), response.Body.String())
			})

			ginkgo.It("refuses a request whose environment cannot be resolved instead of showing every session", func() {
				request := httptest.NewRequest(http.MethodGet, "/api/v1/sessions", nil)
				response := httptest.NewRecorder()
				h.handler.ServeHTTP(response, request)

				Expect(response.Code).To(Equal(http.StatusInternalServerError))
				Expect(response.Body.String()).To(ContainSubstring("request names no environment"))
			})
		})
	}
})
