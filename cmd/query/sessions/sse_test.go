package sessions

import (
	stdcontext "context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/flanksource/commons-db/query"
	"github.com/stretchr/testify/require"
)

func doSSEReq(h http.Handler, path string, header map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	for k, v := range header {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestSessionAPIFramesCarryTheirSequenceAsID(t *testing.T) {
	query.RegisterProvider(&sessionStreamMock{typ: "sess-api-sse-id", rows: []query.Row{{"n": 1.0}, {"n": 2.0}}})
	h, reg := newSessionAPITest(t, 5, traceTestProfile("sse ids", "sess-api-sse-id"))

	info := startSession(t, h, "/api/v1/profile/sse-ids/sessions")
	waitSessionState(t, reg, info.ID, query.SessionCompleted)

	body := doSSEReq(h, "/api/v1/sessions/"+info.ID+"/events", nil).Body.String()
	require.Contains(t, body, "id: 1\nevent: event\n")
	require.Contains(t, body, "id: 2\nevent: event\n")
	require.NotContains(t, body, "id: 0", "the terminal done frame is not a resume point")
}

func TestSessionAPIResumesFromLastEventID(t *testing.T) {
	query.RegisterProvider(&sessionStreamMock{typ: "sess-api-sse-resume", rows: []query.Row{{"n": 1.0}, {"n": 2.0}, {"n": 3.0}}})
	h, reg := newSessionAPITest(t, 5, traceTestProfile("sse resume", "sess-api-sse-resume"))

	info := startSession(t, h, "/api/v1/profile/sse-resume/sessions")
	waitSessionState(t, reg, info.ID, query.SessionCompleted)

	body := doSSEReq(h, "/api/v1/sessions/"+info.ID+"/events",
		map[string]string{"Last-Event-ID": "2"}).Body.String()
	require.NotContains(t, body, "id: 1\n")
	require.NotContains(t, body, "id: 2\n")
	require.Contains(t, body, "id: 3\n", "a resume replays the ring from the named sequence forward")
	require.Contains(t, body, "event: done")
}

func TestSessionAPIRejectsAnUnreadableLastEventID(t *testing.T) {
	query.RegisterProvider(&sessionStreamMock{typ: "sess-api-sse-badid", rows: []query.Row{{"n": 1.0}}})
	h, reg := newSessionAPITest(t, 5, traceTestProfile("sse bad id", "sess-api-sse-badid"))

	info := startSession(t, h, "/api/v1/profile/sse-bad-id/sessions")
	waitSessionState(t, reg, info.ID, query.SessionCompleted)

	rec := doSSEReq(h, "/api/v1/sessions/"+info.ID+"/events", map[string]string{"Last-Event-ID": "abc"})
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "abc")
}

// A tail can sit silent for minutes; the comment frames are what keeps the
// proxies in between from calling the connection dead.
func TestSSEKeepalivePingsAnIdleStream(t *testing.T) {
	session := query.NewSession(query.SessionOptions{
		ID:      "keepalive",
		Profile: traceTestProfile("idle", "sess-api-idle"),
	})

	ctx, cancel := stdcontext.WithTimeout(stdcontext.Background(), 80*time.Millisecond)
	defer cancel()
	rec := httptest.NewRecorder()
	sseStream{session: session, keepalive: 10 * time.Millisecond}.run(rec, httptest.NewRequest(http.MethodGet, "/", nil).WithContext(ctx))

	require.GreaterOrEqual(t, strings.Count(rec.Body.String(), ": ping\n\n"), 2)
}

// The keepalive shares the stream's one goroutine, so a ping can never land in
// the middle of an event frame.
func TestSSEKeepaliveNeverSplitsAnEventFrame(t *testing.T) {
	session := query.NewSession(query.SessionOptions{
		ID:      "interleave",
		Profile: traceTestProfile("busy", "sess-api-busy"),
	})
	go func() {
		for i := 0; i < 50; i++ {
			session.Emit(query.Event{Row: query.Row{"n": i}})
			time.Sleep(time.Millisecond)
		}
	}()

	ctx, cancel := stdcontext.WithTimeout(stdcontext.Background(), 80*time.Millisecond)
	defer cancel()
	rec := httptest.NewRecorder()
	sseStream{session: session, keepalive: time.Millisecond}.run(rec, httptest.NewRequest(http.MethodGet, "/", nil).WithContext(ctx))

	frames := strings.Split(strings.TrimSpace(rec.Body.String()), "\n\n")
	require.NotEmpty(t, frames)
	for _, frame := range frames {
		if strings.HasPrefix(frame, ":") {
			require.Equal(t, ": ping", frame)
			continue
		}
		require.Regexp(t, `^id: \d+\nevent: event\ndata: \{.*\}$`, frame)
	}
}
