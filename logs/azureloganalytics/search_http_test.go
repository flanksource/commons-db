package azureloganalytics

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/connection"
	"github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/types"
)

const queryResponse = `{"tables":[{"name":"PrimaryResult",
  "columns":[{"name":"TimeGenerated","type":"datetime"},{"name":"Message","type":"string"},{"name":"Category","type":"string"},{"name":"Computer","type":"string"}],
  "rows":[["2024-01-01T00:00:00Z","first","audit","host-1"],["2024-01-01T00:00:01Z",null,"audit","host-2"],["2024-01-01T00:00:02Z","third",null,null]]}]}`

// newStubbedSearcher wires a Searcher to a fake Azure AD and Log Analytics.
// It returns the searcher and a pointer to the last query body received.
func newStubbedSearcher(t *testing.T, response string) (*Searcher, *map[string]any, *string) {
	t.Helper()
	var body map[string]any
	var scope string

	aad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		scope = r.PostForm.Get("scope")
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "tok", "token_type": "Bearer", "expires_in": 3600})
	}))
	t.Cleanup(aad.Close)
	service := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			http.Error(w, `{"error":{"code":"Unauthorized","message":"bad token"}}`, http.StatusUnauthorized)
			return
		}
		body = nil
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = w.Write([]byte(response))
	}))
	t.Cleanup(service.Close)

	conn := connection.AzureConnection{
		TenantID:     "tenant",
		ClientID:     &types.EnvVar{ValueStatic: "client"},
		ClientSecret: &types.EnvVar{ValueStatic: "secret"},
	}
	searcher := NewWithClientOptions(conn, nil, ClientOptions{
		Endpoint: service.URL + "/v1", Scope: "https://api.loganalytics.us/.default", Authority: aad.URL,
	})
	return searcher, &body, &scope
}

func TestSearchDecodesRowsIntoLogLines(t *testing.T) {
	g := NewWithT(t)
	searcher, body, scope := newStubbedSearcher(t, queryResponse)

	result, err := searcher.Search(context.New(), Request{
		WorkspaceID: "ws-1",
		Query:       "AzureActivity",
		Limit:       "2",
	})
	g.Expect(err).ToNot(HaveOccurred())

	g.Expect(*scope).To(Equal("https://api.loganalytics.us/.default"))
	g.Expect(*body).To(Equal(map[string]any{"query": "AzureActivity"}))
	g.Expect(result.Metadata).To(HaveKeyWithValue("totalRows", 3))
	g.Expect(result.Logs).To(HaveLen(2))
	g.Expect(result.Logs[0].Message).To(Equal("first"))
	g.Expect(result.Logs[0].Source).To(Equal("audit"))
	g.Expect(result.Logs[0].Host).To(Equal("host-1"))
	// A null Message column leaves the message empty, so it falls back to the stringified row.
	g.Expect(result.Logs[1].Message).ToNot(BeEmpty())
	g.Expect(result.Logs[1].Message).To(ContainSubstring("host-2"))
}

func TestSearchSendsTimespanAsStartSlashEnd(t *testing.T) {
	g := NewWithT(t)
	searcher, body, _ := newStubbedSearcher(t, `{"tables":[]}`)

	_, err := searcher.Search(context.New(), Request{WorkspaceID: "ws", Query: "q"})
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(*body).ToNot(HaveKey("timespan"))

	request := Request{WorkspaceID: "ws", Query: "q"}
	request.Start, request.End = "2024-01-01T00:00:00Z", "2024-01-02T00:00:00Z"
	_, err = searcher.Search(context.New(), request)
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(*body).To(HaveKeyWithValue("timespan", "2024-01-01T00:00:00Z/2024-01-02T00:00:00Z"))
}

func TestSearchSurfacesPartialQueryError(t *testing.T) {
	g := NewWithT(t)
	searcher, _, _ := newStubbedSearcher(t, `{"tables":[],"error":{"code":"PartialError","message":"boom"}}`)

	_, err := searcher.Search(context.New(), Request{WorkspaceID: "ws", Query: "q"})
	g.Expect(err).To(MatchError(ContainSubstring("PartialError")))
}
