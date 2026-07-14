package sessions

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	profilepkg "github.com/flanksource/commons-db/cmd/query/profiles"
	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
	"github.com/stretchr/testify/require"
)

type nextMarker struct{ hit bool }

func (n *nextMarker) ServeHTTP(http.ResponseWriter, *http.Request) { n.hit = true }

type execMock struct{ rows []query.Row }

func (m *execMock) Type() string { return "session-exec-mock" }
func (m *execMock) Execute(_ dbcontext.Context, _ query.ProviderRequest) ([]query.Row, error) {
	return m.rows, nil
}

func execProfile(name string) query.Profile {
	return query.Profile{
		Name: name, Provider: query.ProviderConfig{Type: "session-exec-mock"},
		Query:  "select * where region = '{{.params.region}}'",
		Params: []query.ParamDef{{Name: "region", Type: query.ParamTypeEnum, Options: []string{"US", "EU"}}},
	}
}

// sessionStreamMock is a StreamProvider that emits fixed rows and then either
// ends or blocks until stopped.
type sessionStreamMock struct {
	typ   string
	rows  []query.Row
	block bool
}

func (m *sessionStreamMock) Type() string { return m.typ }

func (m *sessionStreamMock) Execute(_ dbcontext.Context, _ query.ProviderRequest) ([]query.Row, error) {
	return m.rows, nil
}

func (m *sessionStreamMock) Stream(ctx dbcontext.Context, _ query.ProviderRequest, emit func(query.Row)) error {
	for _, row := range m.rows {
		emit(row)
	}
	if m.block {
		<-ctx.Done()
		return ctx.Err()
	}
	return nil
}

func newSessionAPITest(t *testing.T, maxSessions int, profiles ...query.Profile) (*sessionHandler, *query.SessionRegistry) {
	t.Helper()
	store, err := profilepkg.NewFileStore(t.TempDir())
	require.NoError(t, err)
	for _, p := range profiles {
		require.NoError(t, store.Save(context.Background(), p))
	}
	registry := query.NewSessionRegistry(query.RegistryOptions{MaxSessions: maxSessions})
	t.Cleanup(registry.StopAll)
	h := newSessionHandler(sessionHandlerOptions{
		Prefix:   "/api/v1",
		Ctx:      dbcontext.New(),
		Store:    store,
		Registry: registry,
		Next:     &nextMarker{},
	})
	return h, registry
}

func traceTestProfile(name, providerType string) query.Profile {
	return query.Profile{
		Name:     name,
		Provider: query.ProviderConfig{Type: providerType},
		Trace:    &query.TraceSpec{},
	}
}

func doReq(h http.Handler, method, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec
}

func startSession(t *testing.T, h http.Handler, path string) query.SessionInfo {
	t.Helper()
	rec := doReq(h, http.MethodPost, path)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var info query.SessionInfo
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &info))
	require.NotEmpty(t, info.ID)
	return info
}

func waitSessionState(t *testing.T, reg *query.SessionRegistry, id string, state query.SessionState) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if s, ok := reg.Get(id); ok && s.Snapshot().State == state {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	s, _ := reg.Get(id)
	t.Fatalf("session %s never reached %s (now %s)", id, state, s.Snapshot().State)
}

func TestSessionAPIStartsTraceSession(t *testing.T) {
	query.RegisterProvider(&sessionStreamMock{typ: "sess-api-trace", rows: []query.Row{{"n": 1.0}}})
	h, reg := newSessionAPITest(t, 5, traceTestProfile("exec trace", "sess-api-trace"))

	info := startSession(t, h, "/api/v1/profile/exec-trace/sessions")
	require.Equal(t, query.KindTrace, info.Kind)
	waitSessionState(t, reg, info.ID, query.SessionCompleted)
}

func TestSessionAPIRejectsPlainProfileWithoutInterval(t *testing.T) {
	h, _ := newSessionAPITest(t, 5, execProfile("plain"))
	rec := doReq(h, http.MethodPost, "/api/v1/profile/plain/sessions?region=EU")
	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestSessionAPISynthesizesTopForPlainProfile(t *testing.T) {
	query.RegisterProvider(&execMock{rows: []query.Row{{"id": 1}}})
	h, reg := newSessionAPITest(t, 5, execProfile("plain-top"))

	info := startSession(t, h, "/api/v1/profile/plain-top/sessions?interval=1s&region=EU")
	require.Equal(t, query.KindTop, info.Kind)
	require.Equal(t, "EU", info.Params["region"], "filter params flow through, interval does not")
	require.NotContains(t, info.Params, "interval")

	s, ok := reg.Get(info.ID)
	require.True(t, ok)
	s.Stop()
}

func TestSessionAPIReturnsConflictAtCapacity(t *testing.T) {
	query.RegisterProvider(&sessionStreamMock{typ: "sess-api-block", block: true})
	h, reg := newSessionAPITest(t, 1, traceTestProfile("blocker", "sess-api-block"))

	info := startSession(t, h, "/api/v1/profile/blocker/sessions")
	waitSessionState(t, reg, info.ID, query.SessionRunning)

	rec := doReq(h, http.MethodPost, "/api/v1/profile/blocker/sessions")
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
}

func TestSessionAPIStopAndList(t *testing.T) {
	query.RegisterProvider(&sessionStreamMock{typ: "sess-api-stop", rows: []query.Row{{"n": 1.0}}, block: true})
	h, reg := newSessionAPITest(t, 5, traceTestProfile("stoppable", "sess-api-stop"))

	info := startSession(t, h, "/api/v1/profile/stoppable/sessions")
	waitSessionState(t, reg, info.ID, query.SessionRunning)

	rec := doReq(h, http.MethodGet, "/api/v1/sessions")
	require.Equal(t, http.StatusOK, rec.Code)
	var list []query.SessionInfo
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list))
	require.Len(t, list, 1)

	rec = doReq(h, http.MethodDelete, "/api/v1/sessions/"+info.ID)
	require.Equal(t, http.StatusOK, rec.Code)
	waitSessionState(t, reg, info.ID, query.SessionStopped)

	rec = doReq(h, http.MethodGet, "/api/v1/sessions/"+info.ID)
	require.Equal(t, http.StatusOK, rec.Code)
	var got query.SessionInfo
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Equal(t, query.SessionStopped, got.State)

	rec = doReq(h, http.MethodGet, "/api/v1/sessions/nope")
	require.Equal(t, http.StatusNotFound, rec.Code)
}

func TestSessionAPIStreamsEventsAsSSE(t *testing.T) {
	query.RegisterProvider(&sessionStreamMock{typ: "sess-api-sse", rows: []query.Row{{"n": 1.0}, {"n": 2.0}}})
	h, reg := newSessionAPITest(t, 5, traceTestProfile("sse trace", "sess-api-sse"))
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	info := startSession(t, h, "/api/v1/profile/sse-trace/sessions")
	waitSessionState(t, reg, info.ID, query.SessionCompleted)

	resp, err := http.Get(srv.URL + "/api/v1/sessions/" + info.ID + "/events")
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	require.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))

	var eventLines, doneLines int
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "event: event") {
			eventLines++
		}
		if strings.HasPrefix(line, "event: done") {
			doneLines++
		}
	}
	require.Equal(t, 2, eventLines, "both buffered events are replayed")
	require.Equal(t, 1, doneLines, "terminal done event closes the stream")
}

func TestSessionAPIExportsNDJSON(t *testing.T) {
	query.RegisterProvider(&sessionStreamMock{typ: "sess-api-ndjson", rows: []query.Row{{"n": 1.0}, {"n": 2.0}}})
	h, reg := newSessionAPITest(t, 5, traceTestProfile("ndjson trace", "sess-api-ndjson"))

	info := startSession(t, h, "/api/v1/profile/ndjson-trace/sessions")
	waitSessionState(t, reg, info.ID, query.SessionCompleted)

	rec := doReq(h, http.MethodGet, "/api/v1/sessions/"+info.ID+"/events?format=ndjson")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "application/x-ndjson", rec.Header().Get("Content-Type"))

	lines := strings.Split(strings.TrimSpace(rec.Body.String()), "\n")
	require.Len(t, lines, 2)
	var e query.Event
	require.NoError(t, json.Unmarshal([]byte(lines[1]), &e))
	require.Equal(t, int64(2), e.Sequence)
}

func TestSessionAPIServesResult(t *testing.T) {
	query.RegisterProvider(&sessionStreamMock{typ: "sess-api-result", rows: []query.Row{{"n": 1.0}, {"n": 2.0}}})
	h, reg := newSessionAPITest(t, 5, traceTestProfile("result trace", "sess-api-result"))

	info := startSession(t, h, "/api/v1/profile/result-trace/sessions")
	waitSessionState(t, reg, info.ID, query.SessionCompleted)

	rec := doReq(h, http.MethodGet, "/api/v1/sessions/"+info.ID+"/result")
	require.Equal(t, http.StatusOK, rec.Code)
	var rows []query.Row
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &rows))
	require.Len(t, rows, 2)
}

func TestSessionAPIDelegatesUnrelatedPaths(t *testing.T) {
	h, _ := newSessionAPITest(t, 5)
	next := h.next.(*nextMarker)
	_ = doReq(h, http.MethodGet, "/api/v1/profile/anything")
	require.True(t, next.hit, "non-session paths fall through")
}
