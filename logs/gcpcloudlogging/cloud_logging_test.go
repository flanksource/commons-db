package gcpcloudlogging

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/connection"
	"github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/logs"
	"github.com/flanksource/commons-db/types"
)

func serviceAccountJSON(g *WithT, tokenURI string) string {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	g.Expect(err).NotTo(HaveOccurred())
	der, err := x509.MarshalPKCS8PrivateKey(key)
	g.Expect(err).NotTo(HaveOccurred())
	credential, err := json.Marshal(map[string]string{
		"type":         "service_account",
		"project_id":   "proj",
		"private_key":  string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})),
		"client_email": "sa@proj.iam.gserviceaccount.com",
		"token_uri":    tokenURI,
	})
	g.Expect(err).NotTo(HaveOccurred())
	return string(credential)
}

// TestSearch_RESTBackend runs the searcher against a fake Cloud Logging REST
// endpoint: the request carries the project, filter and newest-first order, the
// limit stops paging, and entries map onto log lines.
func TestSearch_RESTBackend(t *testing.T) {
	g := NewWithT(t)

	var mu sync.Mutex
	var listRequests []map[string]any
	var listAuth string
	logging := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/token":
			_, _ = w.Write([]byte(`{"access_token":"minted","token_type":"Bearer","expires_in":3600}`))
		case "/v2/entries:list":
			body, _ := io.ReadAll(r.Body)
			var request map[string]any
			g.Expect(json.Unmarshal(body, &request)).To(Succeed())
			mu.Lock()
			listRequests = append(listRequests, request)
			listAuth = r.Header.Get("Authorization")
			mu.Unlock()
			_, _ = w.Write([]byte(`{"entries":[
				{"insertId":"a","logName":"projects/proj/logs/app","severity":"ERROR","timestamp":"2024-01-01T00:00:00Z","textPayload":"boom","resource":{"type":"k8s","labels":{"pod_name":"web-1"}},"labels":{"team":"core"}},
				{"insertId":"skipped"},
				{"insertId":"b","severity":"DEFAULT","jsonPayload":{"message":"structured","user":"u1"}},
				{"insertId":"c","textPayload":"beyond the limit"}],"nextPageToken":"never-followed"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer logging.Close()

	conn := connection.GCPConnection{
		Project:     "proj",
		Endpoint:    logging.URL,
		Credentials: &types.EnvVar{ValueStatic: serviceAccountJSON(g, logging.URL+"/token")},
	}
	searcher, err := New(context.New(), conn, nil)
	g.Expect(err).NotTo(HaveOccurred())

	result, err := searcher.Search(context.New(), Request{Filter: "severity>=ERROR", LogsRequestBase: logs.LogsRequestBase{Limit: "2"}})

	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(listAuth).To(Equal("Bearer minted"))
	g.Expect(listRequests).To(HaveLen(1), "reaching the limit stops paging")
	g.Expect(listRequests[0]).To(HaveKeyWithValue("filter", MatchRegexp(`^\(severity>=ERROR\) AND timestamp >= "`)))
	delete(listRequests[0], "filter")
	g.Expect(listRequests[0]).To(Equal(map[string]any{
		"resourceNames": []any{"projects/proj"}, "orderBy": "timestamp desc", "pageSize": float64(2),
	}))
	g.Expect(result.Logs).To(HaveLen(2))
	first := result.Logs[0]
	g.Expect([]string{first.ID, first.Message, first.Severity, first.Source}).To(Equal([]string{"a", "boom", "Error", "projects/proj/logs/app"}))
	g.Expect(first.Host).To(Equal("web-1"))
	g.Expect(first.Labels).To(HaveKeyWithValue("label.team", "core"))
	second := result.Logs[1]
	g.Expect([]string{second.ID, second.Message, second.Severity}).To(Equal([]string{"b", "structured", ""}))
	g.Expect(second.Labels).To(HaveKeyWithValue("user", "u1"))
}

// TestDefaultTimestampFilter pins logadmin's implicit 24h window: it is added
// unless the filter mentions "timestamp" in any case.
func TestDefaultTimestampFilter(t *testing.T) {
	now := time.Date(2024, 5, 2, 10, 30, 0, 0, time.UTC)
	const window = `timestamp >= "2024-05-01T10:30:00Z"`
	for name, tc := range map[string]struct{ filter, want string }{
		"empty":                {"", window},
		"no timestamp term":    {"(severity>=ERROR)", "(severity>=ERROR) AND " + window},
		"explicit timestamp":   {`(timestamp >= "2024-01-01T00:00:00Z")`, `(timestamp >= "2024-01-01T00:00:00Z")`},
		"upper-case timestamp": {`TIMESTAMP>"x"`, `TIMESTAMP>"x"`},
		"payload substring":    {`textPayload: "timestamp"`, `textPayload: "timestamp"`},
	} {
		t.Run(name, func(t *testing.T) {
			NewWithT(t).Expect(defaultTimestampFilter(tc.filter, now)).To(Equal(tc.want))
		})
	}
}

func TestBuildFilter_DefaultWindow(t *testing.T) {
	g := NewWithT(t)

	plain, err := buildFilter(Request{Filter: "severity>=ERROR"})
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(plain).To(MatchRegexp(`^\(severity>=ERROR\) AND timestamp >= "\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z"$`))

	bounded, err := buildFilter(Request{Filter: "severity>=ERROR", LogsRequestBase: logs.LogsRequestBase{Start: "2024-01-01T00:00:00Z"}})
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(bounded).To(Equal(`(severity>=ERROR) AND (timestamp >= "2024-01-01T00:00:00Z")`))
}

func TestNew_RequiresProject(t *testing.T) {
	g := NewWithT(t)

	_, err := New(context.New(), connection.GCPConnection{}, nil)

	g.Expect(err).To(MatchError(ContainSubstring("project is required")))
}
