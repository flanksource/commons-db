// Specs for the http trace kind: requests made through commons-db transports
// are stored as exchanges, masked, capped and deduplicated as the kind declares.

package http_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"io/fs"
	netHTTP "net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	. "github.com/onsi/gomega/gstruct"

	"github.com/flanksource/commons/har"

	"github.com/flanksource/commons-db/observability"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/recordstore"
	"github.com/flanksource/commons-db/tracing"
	"github.com/flanksource/commons-db/tracing/http"
	"github.com/flanksource/commons-db/tracing/tracingtest"
)

const secret = "hunter2-very-secret-value"

var _ = Describe("http trace kind", func() {
	var server *httptest.Server
	var env tracingtest.Env

	BeforeEach(func() {
		server = httptest.NewServer(netHTTP.HandlerFunc(func(w netHTTP.ResponseWriter, r *netHTTP.Request) {
			w.Header().Set("Content-Type", "application/json")
			if r.URL.Path == "/missing" {
				w.WriteHeader(netHTTP.StatusNotFound)
				return
			}
			if r.URL.Path == "/large" {
				_, _ = w.Write([]byte(`"` + strings.Repeat("x", 2<<20) + `"`))
				return
			}
			_, _ = w.Write([]byte(`{"ok": true, "items": [1, 2]}`))
		}))
		DeferCleanup(server.Close)
		env = tracingtest.NewEnv(map[string]tracing.TracePlugin{"http": http.Kind()})
	})

	send := func(feature, method, path, body string, header ...string) {
		client := &netHTTP.Client{Transport: observability.ApplyHTTPObservability(context.Background(), feature, nil, nil)}
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
		tracingtest.Running(session)
		traffic()
		session.Stop("spec")
		info := tracingtest.Ended(session)
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

	It("parses only its bodies as JSON, leaving a header that holds JSON as text", func() {
		_, rows := capture(`{"features": ["http"]}`, func() { send("http", "POST", "/api", `{"q": 1}`, "X-Filter", `{"a": 1}`) })
		Expect(rows).To(HaveLen(1))
		request := rows[0]["request"].(map[string]any)
		Expect(request["postData"].(map[string]any)["text"]).To(Equal(map[string]any{"q": json.Number("1")}))
		Expect(request["headers"]).To(ContainElement(map[string]any{"name": "X-Filter", "value": `{"a": 1}`}))
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
		Expect(tracingtest.Summary(info).Deduplicated).To(Equal(int64(2)))
	})

	It("ignores the features it does not observe, and records nothing once stopped", func() {
		session := env.Start("http", `{"features": ["prometheus"]}`)
		tracingtest.Running(session)
		send("loki", "GET", "/api", "")
		session.Stop("spec")
		info := tracingtest.Ended(session)
		send("prometheus", "GET", "/api", "")
		Expect(env.Rows(info)).To(BeEmpty())
	})

	window := func(info query.SessionInfo) tracing.TraceRef {
		return tracing.TraceRef{Kind: "http", Stream: info.Events.Stream}
	}

	refused := func() {
		client := &netHTTP.Client{Transport: observability.ApplyHTTPObservability(context.Background(), "http", nil, nil)}
		_, err := client.Get("http://127.0.0.1:1/refused")
		Expect(err).To(HaveOccurred())
	}

	It("exports a window as a HAR 1.2 file holding each exchange as it was made", func() {
		info, _ := capture(`{"features": ["http"]}`, func() {
			send("http", "POST", "/api", `{"q": 1}`)
			send("http", "GET", "/missing", "")
		})
		var out bytes.Buffer
		Expect(env.Runtime.Export(context.Background(), window(info), "har", &out)).To(Succeed())
		var file har.File
		Expect(json.Unmarshal(out.Bytes(), &file)).To(Succeed())
		Expect(file.Log.Version).To(Equal("1.2"))
		Expect(file.Log.Entries).To(HaveLen(2))
		posted := file.Log.Entries[0]
		Expect(posted.Request.Method).To(Equal("POST"))
		Expect(posted.Request.URL).To(Equal(server.URL + "/api"))
		Expect(posted.Request.PostData.Text).To(MatchJSON(`{"q": 1}`))
		Expect(posted.Response.Content.Text).To(MatchJSON(`{"ok": true, "items": [1, 2]}`))
		Expect(time.Parse(time.RFC3339, posted.StartedDateTime)).To(BeTemporally("~", time.Now(), time.Minute))
		Expect(file.Log.Entries[1].Response.Status).To(Equal(netHTTP.StatusNotFound))
	})

	It("exports what an exchange observed beyond har.Entry: timings, server IP, a tunnel's bytes, encoding and comment", func() {
		started := time.Date(2026, 8, 2, 10, 30, 0, 500*int(time.Millisecond), time.UTC)
		exchange := http.Exchange{
			StartedAt: started, Method: netHTTP.MethodConnect, URL: "https://api.example.com:443", Status: netHTTP.StatusOK,
			Request:    har.Request{Method: netHTTP.MethodConnect, URL: "https://api.example.com:443"},
			Response:   har.Response{Status: netHTTP.StatusOK},
			DurationMs: 250, Timings: har.Timings{Send: -1, Wait: -1, Receive: 250}, ServerIPAddress: "192.0.2.10",
			Tunnelled: true, BytesIn: 4096, BytesOut: 512, Encoding: "base64", Comment: "event stream still open when recorded",
		}
		encoded, err := json.Marshal(http.ToEntry(exchange))
		Expect(err).ToNot(HaveOccurred())
		Expect(encoded).To(MatchJSON(`{
			"startedDateTime": "2026-08-02T10:30:00.5Z", "time": 250,
			"request": {"method": "CONNECT", "url": "https://api.example.com:443", "httpVersion": "", "cookies": null,
				"headers": null, "queryString": null, "headersSize": 0, "bodySize": 0},
			"response": {"status": 200, "statusText": "", "httpVersion": "", "cookies": null, "headers": null,
				"content": {"size": 0}, "redirectURL": "", "headersSize": 0, "bodySize": 0},
			"cache": {}, "timings": {"send": -1, "wait": -1, "receive": 250},
			"serverIPAddress": "192.0.2.10", "comment": "event stream still open when recorded",
			"_tunnelled": true, "_bytesIn": 4096, "_bytesOut": 512, "_encoding": "base64"
		}`))
	})

	It("summarizes a window as the variables a fixture asserts on", func() {
		info, _ := capture(`{"features": ["http"]}`, func() {
			send("http", "POST", "/api", `{"q": 1}`)
			send("http", "GET", "/missing", "")
			refused()
		})
		summary, err := env.Runtime.Summarize(context.Background(), window(info))
		Expect(err).ToNot(HaveOccurred())
		host := strings.Split(strings.TrimPrefix(server.URL, "http://"), ":")[0]
		Expect(summary).To(MatchAllKeys(Keys{
			"count": Equal(3), "entries": Equal(3), "errors": Equal(2),
			"hosts":    Equal([]string{host}),
			"methods":  Equal(map[string]int{"POST": 1, "GET": 2}),
			"statuses": Equal(map[string]int{"200": 1, "404": 1, "0": 1}),
			"requests": ConsistOf(
				MatchAllKeys(Keys{"method": Equal("POST"), "url": Equal(server.URL + "/api"), "host": Equal(host), "path": Equal("/api"),
					"status": Equal(200), "duration_ms": BeNumerically(">=", 0), "error": BeEmpty(),
					"mime": Equal("application/json"), "tunnelled": BeFalse(),
					"request_headers": Not(BeNil()), "response_headers": HaveKeyWithValue("Content-Type", "application/json"),
					"query": BeEmpty(), "body": MatchJSON(`{"q": 1}`),
					"response_size": BeNumerically(">", 0), "response_body": MatchJSON(`{"ok": true, "items": [1, 2]}`),
					"response_json": Equal(map[string]any{"ok": true, "items": []any{float64(1), float64(2)}}),
				}),
				HaveKeyWithValue("status", 404),
				HaveKeyWithValue("error", Not(BeEmpty())),
			),
		}))
	})

	It("refuses params naming no feature, or a dedup window that is not a duration", func() {
		plugin := http.Kind()
		Expect(plugin.ValidateParams(json.RawMessage(`{}`))).To(MatchError(ContainSubstring("feature")))
		Expect(plugin.ValidateParams(json.RawMessage(`{"features": [" "]}`))).To(MatchError(ContainSubstring("feature")))
		Expect(plugin.ValidateParams(json.RawMessage(`{"features": ["http"], "dedupWindow": "soon"}`))).
			To(MatchError(ContainSubstring("dedupWindow")))
	})
})
