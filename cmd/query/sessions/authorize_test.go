package sessions

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	profilepkg "github.com/flanksource/commons-db/cmd/query/profiles"
	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
)

var _ = ginkgo.Describe("stream sessions authorized per profile", func() {
	const (
		providerType = "sess-authz-stream"
		allowed      = "allowed-trace"
		denied       = "denied-trace"
	)

	var (
		handler  *sessionHandler
		registry *query.SessionRegistry
		store    *Store
	)

	authorize := func(_ *http.Request, profile string, _ Action) error {
		if profile != allowed {
			return fmt.Errorf("profile %q is not readable by this caller", profile)
		}
		return nil
	}

	request := func(method, path string) *httptest.ResponseRecorder {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(method, path, nil))
		return response
	}

	ginkgo.BeforeEach(func() {
		query.RegisterProvider(&sessionStreamMock{typ: providerType, rows: []query.Row{{"n": 1.0}}, block: true})
		profiles, err := profilepkg.NewFileStore(ginkgo.GinkgoT().TempDir())
		Expect(err).ToNot(HaveOccurred())
		for _, name := range []string{allowed, denied} {
			Expect(profiles.Save(context.Background(), traceTestProfile(name, providerType))).To(Succeed())
		}
		store = newGormStore(sessionStoreDB(), time.Hour)
		registry = query.NewSessionRegistry(query.RegistryOptions{Store: store, Events: store})
		store.BindResolver(registry.Get)
		ginkgo.DeferCleanup(func() { Expect(registry.StopAll(context.Background())).To(Succeed()) })
		handler = newSessionHandler(sessionHandlerOptions{
			Prefix: "/api/v1", Ctx: dbcontext.New(), Store: profiles, Registry: registry, Sessions: store,
			EventLog: store, Authorize: authorize, Next: &nextMarker{},
		})
	})

	// liveSession starts a session on profile the way another caller would,
	// past this caller's authorization.
	liveSession := func(profile string) string {
		session, err := query.ExecuteStream(dbcontext.New(), registry, traceTestProfile(profile, providerType))
		Expect(err).ToNot(HaveOccurred())
		Eventually(func() int64 { return session.Snapshot().EventCount }).Should(BeNumerically(">=", 1))
		return session.ID()
	}

	// persistedSession records a finished stream session on profile that only
	// the durable store still knows.
	persistedSession := func(id, profile string) string {
		started := time.Now()
		Expect(store.Begin(context.Background(), query.SessionRecord{
			SessionStart: query.SessionStart{ID: id, Profile: profile, Kind: query.KindTrace, Role: query.SessionRoleCapture, StartedAt: started},
			SessionStatus: query.SessionStatus{State: query.SessionCompleted, EventCount: 1, StoppedAt: &started,
				UpdatedAt: started, HeartbeatAt: started},
		})).To(Succeed())
		Expect(store.Append(context.Background(), query.Event{SessionID: id, Sequence: 1, Time: started, Row: query.Row{"n": 1.0}})).To(Succeed())
		Expect(store.Flush()).To(Succeed())
		return id
	}

	expectAccess := func(id string, status int) {
		base := "/api/v1/sessions/" + id
		for _, path := range []string{base, base + "/events?format=ndjson", base + "/result"} {
			response := request(http.MethodGet, path)
			Expect(response.Code).To(Equal(status), "%s: %s", path, response.Body.String())
			if status == http.StatusNotFound {
				Expect(response.Body.String()).To(Equal(errSessionNotFound(id).Error()+"\n"), "a refusal reads as a missing session: %s", path)
			}
		}
	}

	eventsAvailable := func(id string) bool {
		response := request(http.MethodGet, "/api/v1/sessions/"+id)
		Expect(response.Code).To(Equal(http.StatusOK), response.Body.String())
		return decodeInfo(response).EventsAvailable
	}

	listed := func() []string {
		response := request(http.MethodGet, "/api/v1/sessions")
		Expect(response.Code).To(Equal(http.StatusOK), response.Body.String())
		var body sessionListResponse
		Expect(json.Unmarshal(response.Body.Bytes(), &body)).To(Succeed())
		return infoIDs(body.Items)
	}

	ginkgo.It("refuses a live session on a profile the caller may not read, and serves one it may", func() {
		deniedID, allowedID := liveSession(denied), liveSession(allowed)

		expectAccess(deniedID, http.StatusNotFound)
		Expect(request(http.MethodPost, "/api/v1/sessions/"+deniedID+"/stop").Code).To(Equal(http.StatusNotFound))
		session, ok := registry.Get(deniedID)
		Expect(ok).To(BeTrue())
		Expect(session.Snapshot().State.Terminal()).To(BeFalse(), "a refused stop must not stop the session")

		Expect(listed()).To(And(ContainElement(allowedID), Not(ContainElement(deniedID))))
		expectAccess(allowedID, http.StatusOK)
	})

	ginkgo.It("refuses a persisted session on a profile the caller may not read, and serves one it may", func() {
		deniedID := persistedSession("persisted-denied", denied)
		allowedID := persistedSession("persisted-allowed", allowed)

		expectAccess(deniedID, http.StatusNotFound)
		Expect(listed()).To(And(ContainElement(allowedID), Not(ContainElement(deniedID))))
		expectAccess(allowedID, http.StatusOK)
	})

	ginkgo.It("reports a persisted stream session's events available only when the event log holds some", func() {
		withEvents := persistedSession("persisted-events", allowed)
		started := time.Now()
		Expect(store.Begin(context.Background(), query.SessionRecord{
			SessionStart:  query.SessionStart{ID: "persisted-silent", Profile: allowed, Kind: query.KindTrace, Role: query.SessionRoleCapture, StartedAt: started},
			SessionStatus: query.SessionStatus{State: query.SessionCompleted, StoppedAt: &started, UpdatedAt: started, HeartbeatAt: started},
		})).To(Succeed())

		Expect(eventsAvailable(withEvents)).To(BeTrue())
		Expect(eventsAvailable("persisted-silent")).To(BeFalse(), "an event log being configured is not the session having events")
	})

	ginkgo.It("refuses to start a session on a profile the caller may not control", func() {
		response := request(http.MethodPost, "/api/v1/profile/"+denied+"/sessions")
		Expect(response.Code).To(Equal(http.StatusForbidden), response.Body.String())
		Expect(response.Body.String()).To(ContainSubstring("not readable by this caller"))
		Expect(registry.List()).To(BeEmpty())

		response = request(http.MethodPost, "/api/v1/profile/"+allowed+"/sessions")
		Expect(response.Code).To(Equal(http.StatusCreated), response.Body.String())
	})
})
