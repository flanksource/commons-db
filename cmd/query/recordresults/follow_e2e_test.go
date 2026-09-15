package recordresults_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"time"

	"github.com/flanksource/clicky/rpc"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/cmd/query/profiles"
	"github.com/flanksource/commons-db/cmd/query/recordresults"
	"github.com/flanksource/commons-db/cmd/query/sessions"
	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/recordstore"
	"github.com/flanksource/commons-db/recordstore/sqlite"
)

// sampleTally is a result type that is not followable: its profile answers a
// page and nothing else.
type sampleTally struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

const (
	followedProfile   = "/api/v1/profile/profile-trace-results-sample-event"
	unfollowedProfile = "/api/v1/profile/profile-trace-results-sample-tally"
)

func registerFollowTypes(registry *recordresults.Registry) error {
	if err := recordresults.RegisterResultType(registry, recordresults.ResultType[sampleEvent]{
		Kind: "sample_event", Title: "Sample events", TimeColumn: "at", Follow: true,
	}); err != nil {
		return err
	}
	return recordresults.RegisterResultType(registry, recordresults.ResultType[sampleTally]{
		Kind: "sample_tally", Title: "Sample tallies",
	})
}

// followServer mounts what a host serving followable results mounts: the
// sessions handler in front of the profile service, over one profile store and
// one query context, with the registry preparing every session's read.
type followServer struct {
	results *recordresults.Results
	service *profiles.Service
	server  *httptest.Server
}

// tenantHeader names the tenant a follow server request runs for, which its
// middleware puts on the request context the way a host's auth middleware does.
const tenantHeader = "X-Tenant"

func newFollowServer(backend recordstore.BackendKind) followServer {
	return newFollowServerWith(recordresults.OpenOptions{
		Prefix: "trace-results", ConnectionName: "index", Settings: localSettings(backend), Register: registerFollowTypes,
	})
}

func newFollowServerWith(options recordresults.OpenOptions) followServer {
	results := openResults(options)
	service := newResultService(results.Registry)
	store, err := profiles.NewOverlayStore(mustFileStore(), results.Registry)
	Expect(err).ToNot(HaveOccurred())
	queryCtx := dbcontext.New().WithConnectionResolver(results.Registry.ResolveConnection)
	sessionRegistry := query.NewSessionRegistry(query.RegistryOptions{BeforeRead: results.Registry.BeforeRead})
	DeferCleanup(sessionRegistry.StopAll)
	sessionService, err := sessions.New(sessions.Options{
		Profiles: func() (profiles.Store, error) { return store, nil },
		Context:  func() dbcontext.Context { return queryCtx },
		Registry: sessionRegistry,
	})
	Expect(err).ToNot(HaveOccurred())
	handler, err := sessionService.Handler("/api/v1", serveService(service))
	Expect(err).ToNot(HaveOccurred())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if tenant := r.Header.Get(tenantHeader); tenant != "" {
			r = r.WithContext(context.WithValue(r.Context(), tenantKey{}, tenant))
		}
		handler.ServeHTTP(w, r)
	}))
	DeferCleanup(server.Close)
	return followServer{results: results, service: service, server: server}
}

func mustFileStore() profiles.Store {
	store, err := profiles.NewFileStore(GinkgoT().TempDir())
	Expect(err).ToNot(HaveOccurred())
	return store
}

func (s followServer) appendEvents(first, last int) {
	_, err := recordstore.AppendTyped(context.Background(), s.results.Backend, "run-1", "sample_event", sampleEvents(first, last))
	Expect(err).ToNot(HaveOccurred())
}

func (s followServer) do(method, target string, header http.Header) *http.Response {
	request, err := http.NewRequest(method, s.server.URL+target, nil)
	Expect(err).ToNot(HaveOccurred())
	for key, values := range header {
		request.Header[key] = values
	}
	response, err := http.DefaultClient.Do(request)
	Expect(err).ToNot(HaveOccurred())
	DeferCleanup(response.Body.Close)
	return response
}

func (s followServer) startFollow(query string) (int, string) {
	return s.startFollowAs("", query)
}

// startFollowAs starts a follow as tenant, or as no tenant when it is empty.
func (s followServer) startFollowAs(tenant, query string) (int, string) {
	header := http.Header{}
	if tenant != "" {
		header.Set(tenantHeader, tenant)
	}
	response := s.do(http.MethodPost, followedProfile+"/sessions?"+query, header)
	var body strings.Builder
	_, err := bufio.NewReader(response.Body).WriteTo(&body)
	Expect(err).ToNot(HaveOccurred())
	return response.StatusCode, body.String()
}

// pagedRows is the rows a page read serves for query, keyed by seq.
func (s followServer) pagedRows(query string) map[string]map[string]any {
	response := s.do(http.MethodGet, followedProfile+"?"+query, http.Header{"Accept": {"application/json"}})
	Expect(response.StatusCode).To(Equal(http.StatusOK))
	var rows []map[string]any
	Expect(json.NewDecoder(response.Body).Decode(&rows)).To(Succeed())
	bySeq := make(map[string]map[string]any, len(rows))
	for _, row := range rows {
		bySeq[fmt.Sprint(row["seq"])] = row
	}
	return bySeq
}

// streamedEvent is one SSE event frame: its resume id and the event it carries.
type streamedEvent struct {
	id    string
	event query.Event
}

// subscribe reads a session's event stream in the background until the spec
// ends or cancel is called.
func (s followServer) subscribe(id, lastEventID string) (<-chan streamedEvent, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	DeferCleanup(cancel)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, s.server.URL+"/api/v1/sessions/"+id+"/events", nil)
	Expect(err).ToNot(HaveOccurred())
	if lastEventID != "" {
		request.Header.Set("Last-Event-ID", lastEventID)
	}
	response, err := http.DefaultClient.Do(request)
	Expect(err).ToNot(HaveOccurred())
	Expect(response.StatusCode).To(Equal(http.StatusOK))
	events := make(chan streamedEvent, 100)
	go func() {
		defer GinkgoRecover()
		defer func() { _ = response.Body.Close() }()
		scanner := bufio.NewScanner(response.Body)
		scanner.Buffer(make([]byte, 0, 1<<16), 1<<20)
		var frame streamedEvent
		for scanner.Scan() {
			line := scanner.Text()
			if value, ok := strings.CutPrefix(line, "id: "); ok {
				frame.id = value
			}
			if value, ok := strings.CutPrefix(line, "data: "); ok && frame.id != "" {
				Expect(json.Unmarshal([]byte(value), &frame.event)).To(Succeed())
				events <- frame
				frame = streamedEvent{}
			}
		}
	}()
	return events, cancel
}

// rowWait is how long rowsFrom waits for a row: past the follow buffer's one
// second, and short of recordresults.FollowRecheckInterval, so a row that
// arrives was delivered by its append and not found by a recheck.
const rowWait = 3 * time.Second

// rowsFrom collects the rows events carry until it holds count of them.
func rowsFrom(events <-chan streamedEvent, count int) ([]map[string]any, string) {
	rows, _, lastID := followedFrom(events, count)
	return rows, lastID
}

// followedFrom collects count events: the raw row and the presented clicky row
// each carries, as JSON reads them back, and the id of the last one.
func followedFrom(events <-chan streamedEvent, count int) (raw, presented []map[string]any, lastID string) {
	for len(raw) < count {
		select {
		case frame := <-events:
			raw = append(raw, jsonObject(frame.event.Row))
			presented = append(presented, jsonObject(frame.event.ClickyRow))
			lastID = frame.id
		case <-time.After(rowWait):
			Fail(fmt.Sprintf("the session streamed %d of %d rows", len(raw), count))
		}
	}
	return raw, presented, lastID
}

// jsonObject is value as its JSON encoding reads back into a map.
func jsonObject(value any) map[string]any {
	encoded, err := json.Marshal(value)
	Expect(err).ToNot(HaveOccurred())
	var object map[string]any
	Expect(json.Unmarshal(encoded, &object)).To(Succeed())
	return object
}

// presentedSeq is the seq cell of a presented clicky row, as JSON.
func presentedSeq(row map[string]any) string {
	cells, ok := row["cells"].(map[string]any)
	Expect(ok).To(BeTrue(), "presented row %v has no cells", row)
	encoded, err := json.Marshal(cells[seqCell])
	Expect(err).ToNot(HaveOccurred())
	return string(encoded)
}

const seqCell = "seq"

// presentedPage is the rows the interactive page read serves for query —
// application/json+clicky, as the catalog's table reads it — keyed by their
// seq cell.
func (s followServer) presentedPage(query string) map[string]map[string]any {
	response := s.do(http.MethodGet, followedProfile+"?"+query, http.Header{"Accept": {"application/json+clicky"}})
	Expect(response.StatusCode).To(Equal(http.StatusOK))
	var document struct {
		Node struct {
			Rows []map[string]any `json:"rows"`
		} `json:"node"`
	}
	Expect(json.NewDecoder(response.Body).Decode(&document)).To(Succeed())
	bySeq := make(map[string]map[string]any, len(document.Node.Rows))
	for _, row := range document.Node.Rows {
		bySeq[presentedSeq(row)] = row
	}
	return bySeq
}

func seqs(rows []map[string]any) []string {
	values := make([]string, len(rows))
	for index, row := range rows {
		values[index] = fmt.Sprint(row["seq"])
	}
	return values
}

var _ = Describe("following a record result type through the sessions API", func() {
	DescribeTable("streams the rows appended after a follow starts, filtered, in the shape a page serves them",
		func(backend recordstore.BackendKind) {
			server := newFollowServer(backend)
			server.appendEvents(1, 3)

			// Event n is bob's when n is odd, so the filter keeps the even seqs.
			status, body := server.startFollow("follow=true&stream=run-1&afterSeq=3&filter.user=!bob")
			Expect(status).To(Equal(http.StatusCreated), body)
			var info query.SessionInfo
			Expect(json.Unmarshal([]byte(body), &info)).To(Succeed())
			events, disconnect := server.subscribe(info.ID, "")

			server.appendEvents(4, 9)
			rows, presented, lastID := followedFrom(events, 3)
			Expect(seqs(rows)).To(Equal([]string{"4", "6", "8"}))
			paged := server.pagedRows("stream=run-1&afterSeq=3&filter.user=!bob")
			for _, row := range rows {
				Expect(row).To(Equal(paged[fmt.Sprint(row["seq"])]))
			}

			By("carrying each row presented exactly as the interactive page presents the same seq")
			presentedPaged := server.presentedPage("stream=run-1&afterSeq=3&filter.user=!bob")
			Expect(presentedPaged).To(HaveLen(3))
			for _, row := range presented {
				Expect(presentedPaged).To(HaveKey(presentedSeq(row)))
				Expect(row).To(Equal(presentedPaged[presentedSeq(row)]))
			}
			// The type's TableProvider styles its database cell: the presented row
			// is the rich cell, not the raw string.
			db, err := json.Marshal(presented[0]["cells"].(map[string]any)["db"])
			Expect(err).ToNot(HaveOccurred())
			Expect(string(db)).To(ContainSubstring(`"className":"text-blue-500"`))

			By("resuming a reconnect after the last event it saw, replaying none of them")
			disconnect()
			server.appendEvents(10, 11)
			resumed, _ := server.subscribe(info.ID, lastID)
			rows, _ = rowsFrom(resumed, 1)
			Expect(seqs(rows)).To(Equal([]string{"10"}))
		},
		Entry("over a local sqlite file that is its own index", recordstore.BackendSQLite),
		Entry("over ndjson streams mirrored into a derived index", recordstore.BackendNDJSON),
	)

	It("stops a follow bounded by toSeq once it has read through it", func() {
		server := newFollowServer(recordstore.BackendSQLite)
		server.appendEvents(1, 2)
		status, body := server.startFollow("follow=true&stream=run-1&toSeq=3")
		Expect(status).To(Equal(http.StatusCreated), body)
		var info query.SessionInfo
		Expect(json.Unmarshal([]byte(body), &info)).To(Succeed())

		server.appendEvents(3, 4)
		// The state is read with the session's error, so a failed follow says why.
		Eventually(func() string {
			response := server.do(http.MethodGet, "/api/v1/sessions/"+info.ID, nil)
			var current query.SessionInfo
			Expect(json.NewDecoder(response.Body).Decode(&current)).To(Succeed())
			return strings.TrimSpace(string(current.State) + " " + current.Error)
		}, 10*time.Second, 50*time.Millisecond).Should(Equal(string(query.SessionCompleted)))
		rows, _ := rowsFrom(server.mustSubscribe(info.ID), 3)
		Expect(seqs(rows)).To(Equal([]string{"1", "2", "3"}))
	})

	It("answers a follow of a stream nobody wrote with a 404", func() {
		status, body := newFollowServer(recordstore.BackendSQLite).startFollow("follow=true&stream=run-404")
		Expect(status).To(Equal(http.StatusNotFound), body)
		Expect(body).To(ContainSubstring("run-404"))
	})

	It("refuses to follow a result type that did not opt in", func() {
		server := newFollowServer(recordstore.BackendSQLite)
		response := server.do(http.MethodPost, unfollowedProfile+"/sessions?follow=true&stream=run-1", nil)
		Expect(response.StatusCode).To(Equal(http.StatusBadRequest))
		var body strings.Builder
		_, err := bufio.NewReader(response.Body).WriteTo(&body)
		Expect(err).ToNot(HaveOccurred())
		Expect(body.String()).To(ContainSubstring("cannot be followed"))
	})

	It("refuses a followable result type over a source whose appends wake nobody", func() {
		schemas := recordstore.NewSchemas()
		index, err := sqlite.Open(sqlite.Options{
			Path: filepath.Join(GinkgoT().TempDir(), "index.sqlite"), Schema: schemas.Kind, Derived: true, SweepInterval: time.Minute,
		})
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(index.Close)
		registry, err := recordresults.NewRegistry(recordresults.RegistryOptions{
			Prefix: "trace-results", Schemas: schemas, Index: index, Source: newKV(schemas), ConnectionName: "index",
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(registerFollowTypes(registry)).To(MatchError(ContainSubstring("*recordstore.Notifier")))
	})

	It("offers a session start only for the result type that opted into follow", func() {
		server := newFollowServer(recordstore.BackendSQLite)
		spec := &rpc.OpenAPISpec{Paths: map[string]rpc.OpenAPIPath{}, Clicky: &rpc.ClickySpecMeta{}}
		Expect(server.service.AddProfilesOpenAPI(context.Background(), spec)).To(Succeed())
		Expect(spec.Paths).To(HaveKey(followedProfile + "/sessions"))
		Expect(spec.Paths).ToNot(HaveKey(unfollowedProfile + "/sessions"))
		Expect(spec.Paths).To(HaveKey(unfollowedProfile))
	})
})

// mustSubscribe replays a finished session's events.
func (s followServer) mustSubscribe(id string) <-chan streamedEvent {
	events, _ := s.subscribe(id, "")
	return events
}
