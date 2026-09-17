package sessions

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"

	"github.com/flanksource/clicky/cache"
	"github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	profilepkg "github.com/flanksource/commons-db/cmd/query/profiles"
	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/recordstore"
	"github.com/flanksource/commons-db/recordstore/kv"
	"github.com/flanksource/commons-db/recordstore/recordstoretest"
)

const (
	jvmProfile = "trace-capture/jvm_trace"
	sqlProfile = "trace-capture/sql_xevent"
)

// apiHarness serves the sessions API over a memory kv session store seeded
// with the contract fixture, a memory record store, and a registry that can
// restart jvm_trace captures.
type apiHarness struct {
	handler  *sessionHandler
	registry *query.SessionRegistry
	store    *KVStore
	records  *recordstore.Notifier
	epoch    time.Time

	mu        sync.Mutex
	unread    map[string]bool // profiles Authorize refuses to read
	uncontrol map[string]bool // profiles Authorize refuses to control
	calls     map[string]int  // Authorize calls per profile/action
}

func newAPIHarness() *apiHarness {
	h := &apiHarness{epoch: contractEpoch(), unread: map[string]bool{}, uncontrol: map[string]bool{}, calls: map[string]int{}}
	var err error
	h.store, err = NewMemoryKVStore(KVStoreOptions{})
	Expect(err).ToNot(HaveOccurred())
	backend, err := kv.New(kv.Options{
		Store: cache.NewMemory(), Prefix: "records", Schema: recordstoretest.Schema, TTL: time.Hour, MaxChunkBytes: 1 << 20,
	})
	Expect(err).ToNot(HaveOccurred())
	h.records, err = recordstore.NewNotifier(backend, recordstore.NotifierOptions{RecheckInterval: 20 * time.Millisecond})
	Expect(err).ToNot(HaveOccurred())

	h.registry = query.NewSessionRegistry(query.RegistryOptions{
		Store: h.store,
		Restarters: map[string]query.RestartFunc{
			"trace-capture/jvm": func(ctx context.Context, _ query.SessionRecord, opts query.TrackOptions) (*query.Session, error) {
				return h.registry.Track(ctx, opts)
			},
		},
	})
	ginkgo.DeferCleanup(func() { Expect(h.registry.StopAll(context.Background())).To(Succeed()) })
	profiles, err := profilepkg.NewFileStore(ginkgo.GinkgoT().TempDir())
	Expect(err).ToNot(HaveOccurred())

	h.handler = newSessionHandler(sessionHandlerOptions{
		Prefix: "/api/v1", Ctx: dbcontext.New(), Store: profiles, Registry: h.registry, Sessions: h.store,
		Records: h.records, Authorize: h.authorize, Next: &nextMarker{},
		Principal: func(*http.Request) string { return "tester" },
	})
	for _, rec := range contractFixture(h.epoch) {
		Expect(h.store.Begin(context.Background(), rec)).To(Succeed())
	}
	return h
}

func (h *apiHarness) authorize(_ *http.Request, profile string, action Action) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.calls[profile+"/"+string(action)]++
	if h.unread[profile] || (action == ActionControl && h.uncontrol[profile]) {
		return fmt.Errorf("profile %q is not %s-able by this caller", profile, action)
	}
	return nil
}

func (h *apiHarness) callsFor(profile string, action Action) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.calls[profile+"/"+string(action)]
}

func (h *apiHarness) do(method, path string, header ...string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, nil)
	for i := 0; i+1 < len(header); i += 2 {
		request.Header.Set(header[i], header[i+1])
	}
	response := httptest.NewRecorder()
	h.handler.ServeHTTP(response, request)
	return response
}

// track starts a live jvm_trace capture in the harness registry.
func (h *apiHarness) track(labels map[string]string) *query.Session {
	stopAt := time.Now().Add(10 * time.Minute)
	session, err := h.registry.Track(context.Background(), query.TrackOptions{
		Profile: jvmProfile, Kind: query.KindCapture, Labels: labels, Principal: "admin",
		Params: map[string]any{"class": "com.example.Listener"}, StopAt: &stopAt,
	})
	Expect(err).ToNot(HaveOccurred())
	session.OnStop(func(string) { session.Finish(query.FinishUpdate{}) })
	Expect(session.Running(query.RunningUpdate{Handle: "probe-1"})).To(Succeed())
	return session
}

// filterRecordingStore records the filter of every List it serves.
type filterRecordingStore struct {
	query.SessionStore
	mu      sync.Mutex
	filters []query.SessionFilter
}

func (s *filterRecordingStore) List(ctx context.Context, filter query.SessionFilter) (query.SessionPage, error) {
	s.mu.Lock()
	s.filters = append(s.filters, filter)
	s.mu.Unlock()
	return s.SessionStore.List(ctx, filter)
}

func (s *filterRecordingStore) last() query.SessionFilter {
	s.mu.Lock()
	defer s.mu.Unlock()
	Expect(s.filters).ToNot(BeEmpty())
	return s.filters[len(s.filters)-1]
}

type sessionListBody struct {
	Items  []query.SessionInfo `json:"items"`
	Total  int                 `json:"total"`
	Shared bool                `json:"shared"`
}

func (h *apiHarness) list(query string) sessionListBody {
	response := h.do(http.MethodGet, "/api/v1/sessions?"+query)
	Expect(response.Code).To(Equal(http.StatusOK), response.Body.String())
	var body sessionListBody
	Expect(json.Unmarshal(response.Body.Bytes(), &body)).To(Succeed())
	return body
}

func infoIDs(infos []query.SessionInfo) []string {
	ids := make([]string, len(infos))
	for i, info := range infos {
		ids[i] = info.ID
	}
	return ids
}

func decodeInfo(response *httptest.ResponseRecorder) query.SessionInfo {
	var info query.SessionInfo
	Expect(json.Unmarshal(response.Body.Bytes(), &info)).To(Succeed(), response.Body.String())
	return info
}

func sseFrames(body string, event string) int {
	return strings.Count(body, "event: "+event+"\n")
}
