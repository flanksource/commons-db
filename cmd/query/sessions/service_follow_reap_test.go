package sessions

import (
	"bufio"
	stdcontext "context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
	"github.com/stretchr/testify/require"
)

// viewerGrace is the ViewGrace these tests run with: long enough that opening
// an in-process connection inside it is not a race, short enough that waiting
// one out costs a test under a second.
const viewerGrace = 500 * time.Millisecond

// tailMock is a StreamProvider that tails: it emits a row for every value
// pushed to rows and otherwise blocks until its session's context is cancelled,
// which is what a log tail or a probe stream does.
type tailMock struct {
	typ  string
	rows chan query.Row
}

func newTailMock(typ string) *tailMock {
	mock := &tailMock{typ: typ, rows: make(chan query.Row)}
	query.RegisterProvider(mock)
	return mock
}

func (m *tailMock) Type() string { return m.typ }

func (m *tailMock) Execute(_ dbcontext.Context, _ query.ProviderRequest) ([]query.Row, error) {
	return nil, nil
}

func (m *tailMock) Stream(ctx dbcontext.Context, _ query.ProviderRequest, emit func(query.Row)) error {
	for {
		select {
		case row := <-m.rows:
			emit(row)
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// tailProfile declares a trace whose buffer flushes one row at a time, so a
// test reads an event as soon as the provider emits it rather than at the
// follow buffer's default wait.
func tailProfile(name, providerType string) query.Profile {
	return query.Profile{
		Name:     name,
		Provider: query.ProviderConfig{Type: providerType},
		Trace:    &query.TraceSpec{Buffer: &query.TraceBufferSpec{MaxRows: 1}},
	}
}

// sseViewer is one open event stream, read line by line, with the close that
// drops the connection the way a closed tab or a reload does: no DELETE, just a
// connection that goes away.
type sseViewer struct {
	lines chan string
	close func()
}

func openSSE(t *testing.T, url, lastEventID string) *sseViewer {
	t.Helper()
	ctx, cancel := stdcontext.WithCancel(stdcontext.Background())
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	require.NoError(t, err)
	if lastEventID != "" {
		request.Header.Set("Last-Event-ID", lastEventID)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		cancel()
		t.Fatalf("open %s: %v", url, err)
	}
	require.Equal(t, http.StatusOK, response.StatusCode)

	viewer := &sseViewer{
		lines: make(chan string, 64),
		close: func() { cancel(); _ = response.Body.Close() },
	}
	go func() {
		defer close(viewer.lines)
		scanner := bufio.NewScanner(response.Body)
		for scanner.Scan() {
			select {
			case viewer.lines <- scanner.Text():
			case <-ctx.Done():
				return
			}
		}
	}()
	t.Cleanup(viewer.close)
	return viewer
}

// nextEventID is the sequence of the next event frame the viewer receives,
// skipping keepalive comments and the data lines of the frames themselves.
func (v *sseViewer) nextEventID(t *testing.T) string {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case line, open := <-v.lines:
			if !open {
				t.Fatal("the event stream closed before the next event frame")
			}
			if id, found := strings.CutPrefix(line, "id: "); found {
				return id
			}
		case <-deadline:
			t.Fatal("no event frame arrived within 5s")
		}
	}
}

func followTestAPI(t *testing.T, profile string, mock *tailMock) (*httptest.Server, *query.SessionRegistry) {
	t.Helper()
	handler, registry := newSessionAPITest(t,
		query.RegistryOptions{MaxSessions: 5, ViewGrace: viewerGrace},
		tailProfile(profile, mock.typ))
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return server, registry
}

func snapshotOf(t *testing.T, reg *query.SessionRegistry, id string) query.SessionInfo {
	t.Helper()
	session, live := reg.Get(id)
	require.True(t, live, "session %s is no longer tracked", id)
	return session.Snapshot()
}

// A tab that closes, reloads or restarts on HMR never sends the stop its
// unmount handler would have. Only the dropped event stream says the viewer is
// gone, and the session has to end on that alone.
func TestSessionAPIReapsAFollowWhoseViewerLeft(t *testing.T) {
	mock := newTailMock("sess-api-reap")
	server, registry := followTestAPI(t, "reap follow", mock)

	info := startSession(t, server.Config.Handler, "/api/v1/profile/reap-follow/sessions?follow=true")
	require.Equal(t, query.SessionRoleView, info.Role, "a followed profile is a view, not a capture")
	viewer := openSSE(t, server.URL+"/api/v1/sessions/"+info.ID+"/events", "")
	waitSessionState(t, registry, info.ID, query.SessionRunning)

	mock.rows <- query.Row{"n": 1.0}
	require.Equal(t, "1", viewer.nextEventID(t))

	viewer.close()
	waitSessionState(t, registry, info.ID, query.SessionStopped)
	require.Contains(t, snapshotOf(t, registry, info.ID).StopReason, query.StopReasonReapedIdle)
	require.Equal(t, int64(1), registry.ReapedViews())
}

// An EventSource that drops reconnects on its own and names what it already
// holds. The grace exists so that retry finds the session it was reading.
func TestSessionAPIKeepsAFollowWhoseViewerReconnectsInsideTheGrace(t *testing.T) {
	mock := newTailMock("sess-api-reconnect")
	server, registry := followTestAPI(t, "reconnect follow", mock)

	info := startSession(t, server.Config.Handler, "/api/v1/profile/reconnect-follow/sessions?follow=true")
	events := server.URL + "/api/v1/sessions/" + info.ID + "/events"
	viewer := openSSE(t, events, "")
	waitSessionState(t, registry, info.ID, query.SessionRunning)

	mock.rows <- query.Row{"n": 1.0}
	require.Equal(t, "1", viewer.nextEventID(t))
	viewer.close()

	resumed := openSSE(t, events, "1")
	mock.rows <- query.Row{"n": 2.0}
	require.Equal(t, "2", resumed.nextEventID(t),
		"the retry resumed after the event it named, on the session it was already reading")
	require.Equal(t, query.SessionRunning, snapshotOf(t, registry, info.ID).State)
	require.Zero(t, registry.ReapedViews())
}

// A capture is bounded by its own duration, not by whether anyone is watching:
// a declared trace records for as long as it was asked to, and a connection
// trace has no viewer at all.
func TestSessionAPIDoesNotReapASessionNotStartedWithFollow(t *testing.T) {
	mock := newTailMock("sess-api-noreap")
	server, registry := followTestAPI(t, "declared trace", mock)

	info := startSession(t, server.Config.Handler, "/api/v1/profile/declared-trace/sessions")
	waitSessionState(t, registry, info.ID, query.SessionRunning)
	require.Equal(t, query.SessionRoleCapture, info.Role)

	time.Sleep(3 * viewerGrace) // nobody ever subscribes
	require.Equal(t, query.SessionRunning, snapshotOf(t, registry, info.ID).State)
	require.Zero(t, registry.ReapedViews())
}

// The reported symptom: one abandoned follow per page load, until the registry
// refuses the next one. A conflict now clears itself within a grace instead of
// standing until MaxDuration.
func TestSessionAPIReloadChurnDoesNotExhaustTheViewBudget(t *testing.T) {
	const (
		reloads = 20
		budget  = 4
	)
	mock := newTailMock("sess-api-churn")
	handler, registry := newSessionAPITest(t,
		query.RegistryOptions{MaxSessions: 5, MaxViews: budget, ViewGrace: viewerGrace},
		tailProfile("churn follow", mock.typ))

	path := "/api/v1/profile/churn-follow/sessions?follow=true"
	conflicts := 0
	for reload := 0; reload < reloads; reload++ {
		// Each load starts a follow and moves on without stopping it.
		response := doReq(handler, http.MethodPost, path)
		if response.Code == http.StatusConflict {
			conflicts++
			waitActiveViews(t, registry, 0)
			response = doReq(handler, http.MethodPost, path)
		}
		require.Equal(t, http.StatusCreated, response.Code, "reload %d: %s", reload, response.Body.String())
	}

	require.GreaterOrEqual(t, conflicts, 1, "a budget of %d is meant to be reached by %d reloads", budget, reloads)
	waitActiveViews(t, registry, 0)
	require.Equal(t, int64(reloads), registry.ReapedViews())
}

func waitActiveViews(t *testing.T, reg *query.SessionRegistry, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if reg.ActiveViews() == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("active views never fell to %d (now %d)", want, reg.ActiveViews())
}
