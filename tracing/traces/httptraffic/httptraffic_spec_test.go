// Specs for the http trace kind: requests made through commons-db transports
// are stored as exchanges, masked, capped and deduplicated as the kind declares.

package httptraffic_test

import (
	"context"
	"encoding/json"
	"io"
	"io/fs"
	netHTTP "net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/connection"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/recordstore"

	"github.com/flanksource/commons-db/tracing/traces"
	"github.com/flanksource/commons-db/tracing/traces/httptraffic"
	"github.com/flanksource/commons-db/tracing/traces/tracestest"
)

const secret = "hunter2-very-secret-value"

var _ = Describe("http trace kind", func() {
	var server *httptest.Server
	var env tracestest.Env

	BeforeEach(func() {
		server = httptest.NewServer(netHTTP.HandlerFunc(func(w netHTTP.ResponseWriter, r *netHTTP.Request) {
			w.Header().Set("Content-Type", "application/json")
			if r.URL.Path == "/large" {
				_, _ = w.Write([]byte(`"` + strings.Repeat("x", 2<<20) + `"`))
				return
			}
			_, _ = w.Write([]byte(`{"ok": true, "items": [1, 2]}`))
		}))
		DeferCleanup(server.Close)
		env = tracestest.NewEnv(map[string]traces.TracePlugin{"http": httptraffic.Kind()})
	})

	send := func(feature, method, path, body string, header ...string) {
		client := &netHTTP.Client{Transport: connection.ApplyHTTPObservability(context.Background(), feature, nil, nil)}
		request, err := netHTTP.NewRequest(method, server.URL+path, strings.NewReader(body))
		Expect(err).ToNot(HaveOccurred())
		if body != "" {
			request.Header.Set("Content-Type", "application/json")
		}
		for i := 0; i+1 < len(header); i += 2 {
			request.Header.Set(header[i], header[i+1])
		}
		response, err := client.Do(request)
		Expect(err).ToNot(HaveOccurred())
		_, _ = io.Copy(io.Discard, response.Body)
		Expect(response.Body.Close()).To(Succeed())
	}

	capture := func(params string, traffic func()) (query.SessionInfo, []recordstore.Row) {
		session := env.Start("http", params)
		tracestest.Running(session)
		traffic()
		session.Stop("spec")
		info := tracestest.Ended(session)
		Expect(info.Error).To(BeEmpty())
		Expect(info.Warning).To(BeEmpty())
		return info, env.Rows(info)
	}

	It("stores each exchange of an observed feature, its JSON body as structure", func() {
		_, rows := capture(`{"features": ["prometheus"]}`, func() { send("prometheus", "GET", "/api/v1/query?q=up", "") })
		Expect(rows).To(HaveLen(1))
		row := rows[0]
		Expect(row["feature"]).To(Equal("prometheus"))
		Expect(row["method"]).To(Equal("GET"))
		Expect(row["url"]).To(Equal(server.URL + "/api/v1/query?q=up"))
		Expect(row["host"]).To(Equal(strings.TrimPrefix(server.URL, "http://")))
		Expect(row["status"]).To(BeEquivalentTo(200))
		response := row["response"].(map[string]any)
		Expect(response["content"].(map[string]any)["text"]).To(Equal(map[string]any{"ok": true, "items": []any{json.Number("1"), json.Number("2")}}))
	})

	It("never stores a secret: not in headers, the URL or a body, and not in the store's files", func() {
		_, rows := capture(`{"features": ["http"]}`, func() {
			send("http", "POST", "/login?token="+secret, `{"user": "bob", "password": "`+secret+`"}`,
				"Authorization", "Bearer "+secret, "Cookie", "session="+secret)
		})
		Expect(rows).To(HaveLen(1))
		stored, err := json.Marshal(rows)
		Expect(err).ToNot(HaveOccurred())
		Expect(string(stored)).ToNot(ContainSubstring(secret))
		Expect(string(stored)).To(ContainSubstring("bob"))

		Expect(filepath.WalkDir(env.Dir, func(path string, entry fs.DirEntry, err error) error {
			if err != nil || !entry.Type().IsRegular() {
				return err
			}
			content, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			Expect(string(content)).ToNot(ContainSubstring(secret), path)
			return nil
		})).To(Succeed())
	})

	It("stores a body no larger than HAR capture allows, marking it truncated", func() {
		_, rows := capture(`{"features": ["http"]}`, func() { send("http", "GET", "/large", "") })
		Expect(rows).To(HaveLen(1))
		content := rows[0]["response"].(map[string]any)["content"].(map[string]any)
		Expect(content["truncated"]).To(BeTrue())
		Expect(len(content["text"].(string))).To(BeNumerically("<", 2<<20))
	})

	It("keeps repeated exchanges apart unless the session deduplicates within a window", func() {
		repeat := func() {
			for range 3 {
				send("http", "GET", "/api", "")
			}
		}
		_, rows := capture(`{"features": ["http"]}`, repeat)
		Expect(rows).To(HaveLen(3))

		info, rows := capture(`{"features": ["http"], "dedupWindow": "1h"}`, repeat)
		Expect(rows).To(HaveLen(1))
		Expect(tracestest.Summary(info).Deduplicated).To(Equal(int64(2)))
	})

	It("ignores the features it does not observe, and records nothing once stopped", func() {
		session := env.Start("http", `{"features": ["prometheus"]}`)
		tracestest.Running(session)
		send("loki", "GET", "/api", "")
		session.Stop("spec")
		info := tracestest.Ended(session)
		send("prometheus", "GET", "/api", "")
		Expect(env.Rows(info)).To(BeEmpty())
	})

	It("refuses params naming no feature, or a dedup window that is not a duration", func() {
		plugin := httptraffic.Kind()
		Expect(plugin.ValidateParams(json.RawMessage(`{}`))).To(MatchError(ContainSubstring("feature")))
		Expect(plugin.ValidateParams(json.RawMessage(`{"features": [" "]}`))).To(MatchError(ContainSubstring("feature")))
		Expect(plugin.ValidateParams(json.RawMessage(`{"features": ["http"], "dedupWindow": "soon"}`))).
			To(MatchError(ContainSubstring("dedupWindow")))
	})
})
