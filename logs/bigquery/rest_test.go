package bigquery

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/connection"
	"github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/logs"
	"github.com/flanksource/commons-db/types"
)

const mintedToken = "minted-access-token"

// serviceAccountJSON builds a service-account credential whose token endpoint is tokenURI.
func serviceAccountJSON(g *WithT, tokenURI string) string {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	g.Expect(err).NotTo(HaveOccurred())
	der, err := x509.MarshalPKCS8PrivateKey(key)
	g.Expect(err).NotTo(HaveOccurred())
	credential, err := json.Marshal(map[string]string{
		"type":           "service_account",
		"project_id":     "proj",
		"private_key_id": "kid",
		"private_key":    string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})),
		"client_email":   "sa@proj.iam.gserviceaccount.com",
		"token_uri":      tokenURI,
	})
	g.Expect(err).NotTo(HaveOccurred())
	return string(credential)
}

// TestSearch_RESTBackend runs the searcher end to end against a fake BigQuery
// REST endpoint: service-account credentials are exchanged for a bearer token,
// the query is submitted, and the schema-typed rows map onto log lines.
func TestSearch_RESTBackend(t *testing.T) {
	g := NewWithT(t)

	var mu sync.Mutex
	var queryAuth string
	bigQuery := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/token":
			_, _ = w.Write([]byte(`{"access_token":"` + mintedToken + `","token_type":"Bearer","expires_in":3600}`))
		case "/bigquery/v2/projects/proj/queries":
			mu.Lock()
			queryAuth = r.Header.Get("Authorization")
			mu.Unlock()
			_, _ = w.Write([]byte(`{"jobComplete":true,"jobReference":{"projectId":"proj","jobId":"j1"},"totalRows":"2",
				"schema":{"fields":[{"name":"msg","type":"STRING"},{"name":"ts","type":"TIMESTAMP"},{"name":"sev","type":"STRING"},{"name":"n","type":"INTEGER"}]},
				"rows":[{"f":[{"v":"hello"},{"v":"1.7040672E9"},{"v":"ERROR"},{"v":"7"}]},{"f":[{"v":null},{"v":"1.7040673E9"},{"v":null},{"v":"8"}]}]}`))
		case "/bigquery/v2/projects/proj/jobs/j1":
			_, _ = w.Write([]byte(`{"statistics":{"creationTime":"1704067200000","startTime":"1704067201000","endTime":"1704067203000","query":{"totalBytesProcessed":"2048","totalBytesBilled":"10485760","totalSlotMs":"77"}}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer bigQuery.Close()

	conn := connection.GCPConnection{
		Project:     "proj",
		Endpoint:    bigQuery.URL + "/bigquery/v2/",
		Credentials: &types.EnvVar{ValueStatic: serviceAccountJSON(g, bigQuery.URL+"/token")},
	}
	searcher := New(conn, &logs.FieldMappingConfig{Message: []string{"msg"}, Timestamp: []string{"ts"}, Severity: []string{"sev"}})
	defer searcher.Close()

	result, err := searcher.Search(context.New(), Request{Query: "SELECT msg, ts, sev, n FROM logs"})

	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(queryAuth).To(Equal("Bearer " + mintedToken))
	g.Expect(result.Logs).To(HaveLen(2))
	first := result.Logs[0]
	g.Expect(first.Message).To(Equal("hello"))
	g.Expect(first.Severity).To(Equal("ERROR"))
	g.Expect(first.Labels).To(HaveKeyWithValue("n", "7"))
	g.Expect(result.Logs[1].Message).NotTo(BeEmpty(), "a row with no mapped message falls back to the stringified row")
	g.Expect(result.Metadata).To(HaveKeyWithValue("totalRows", int64(2)))
	g.Expect(result.Metadata).To(HaveKeyWithValue("bytesProcessed", int64(2048)))
	g.Expect(result.Metadata).To(HaveKeyWithValue("bytesBilled", int64(10485760)))
	g.Expect(result.Metadata).To(HaveKeyWithValue("totalSlotMs", int64(77)))
	g.Expect(result.Metadata).To(HaveKeyWithValue("executionTime", "2s"))
}

func TestSearch_RequiresProject(t *testing.T) {
	g := NewWithT(t)

	_, err := New(connection.GCPConnection{}, nil).Search(context.New(), Request{Query: "SELECT 1"})

	g.Expect(err).To(MatchError(ContainSubstring("project is required")))
}
