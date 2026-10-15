// Specs for the http_proxy trace kind: a client pointed at the exported proxy
// address has its plain exchanges recorded in full, its TLS tunnels recorded
// by their bytes, or, under mitm, decrypted with the CA the capture exports.

package httpproxy_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"net"
	nethttp "net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	. "github.com/onsi/gomega/gstruct"

	"github.com/flanksource/commons/har"

	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/tracing"
	tracehttp "github.com/flanksource/commons-db/tracing/http"
	"github.com/flanksource/commons-db/tracing/httpproxy"
	"github.com/flanksource/commons-db/tracing/tracingtest"
)

const secret = "hunter2-very-secret-value"

// trustEnv are the variables a mitm capture points a child's TLS stack at its
// CA with.
var trustEnv = []string{"SSL_CERT_FILE", "CURL_CA_BUNDLE", "NODE_EXTRA_CA_CERTS", "REQUESTS_CA_BUNDLE", "GIT_SSL_CAINFO", "AWS_CA_BUNDLE"}

var _ = Describe("http_proxy trace kind", func() {
	var env tracingtest.Env

	BeforeEach(func() {
		env = tracingtest.NewEnv(map[string]tracing.TracePlugin{httpproxy.Kind: httpproxy.NewKind()})
	})

	start := func(params string) *query.ManagedSession {
		managed, err := env.Runtime.Start(dbcontext.New(), tracing.StartRequest{Kind: httpproxy.Kind, Params: json.RawMessage(params)})
		Expect(err).ToNot(HaveOccurred())
		tracingtest.Running(managed.Session())
		return managed
	}

	// client is routed through managed's proxy, as a child honouring the
	// exported HTTP_PROXY is.
	client := func(managed *query.ManagedSession, tlsConfig *tls.Config) (*nethttp.Client, *nethttp.Transport) {
		proxyURL, err := url.Parse(managed.Status().Exports["HTTP_PROXY"])
		Expect(err).ToNot(HaveOccurred())
		transport := &nethttp.Transport{Proxy: nethttp.ProxyURL(proxyURL), TLSClientConfig: tlsConfig}
		return &nethttp.Client{Transport: transport, Timeout: 10 * time.Second}, transport
	}

	read := func(response *nethttp.Response, err error) string {
		Expect(err).ToNot(HaveOccurred())
		body, err := io.ReadAll(response.Body)
		Expect(err).ToNot(HaveOccurred())
		Expect(response.Body.Close()).To(Succeed())
		return string(body)
	}

	exchanges := func(ref tracing.TraceRef) []tracehttp.Exchange {
		var read []tracehttp.Exchange
		Expect(tracing.ReadRecords(context.Background(), env.Runtime, ref, func(exchange tracehttp.Exchange) error {
			read = append(read, exchange)
			return nil
		})).To(Succeed())
		return read
	}

	whole := func(managed *query.ManagedSession) tracing.TraceRef {
		return tracing.TraceRef{Kind: httpproxy.Kind, Stream: managed.Session().Snapshot().Events.Stream}
	}

	stop := func(managed *query.ManagedSession) []tracehttp.Exchange {
		managed.Session().Stop("spec")
		info := tracingtest.Ended(managed.Session())
		Expect(info.Error).To(BeEmpty())
		Expect(info.Warning).To(BeEmpty())
		return exchanges(whole(managed))
	}

	It("exports the proxy's address to the processes it observes as soon as it starts, bypassing only itself and noProxy", func() {
		managed := start(`{"noProxy": ["127.0.0.1:8080"]}`)
		exports := managed.Status().Exports
		addr := strings.TrimPrefix(exports["HTTP_PROXY"], "http://")
		Expect(addr).To(MatchRegexp(`^127\.0\.0\.1:\d+$`))
		Expect(exports).To(Equal(map[string]string{
			"HTTP_PROXY": "http://" + addr, "http_proxy": "http://" + addr,
			"HTTPS_PROXY": "http://" + addr, "https_proxy": "http://" + addr,
			"NO_PROXY": addr + ",127.0.0.1:8080", "no_proxy": addr + ",127.0.0.1:8080",
		}), "a connect capture exports no CA to trust, and leaves loopback at large proxied")
		Expect(managed.Status().Handle).To(Equal(addr))
		Expect(managed.Status().Metadata).To(ContainElement(MatchFields(IgnoreExtras, Fields{"Name": Equal("mode"), "Value": Equal("connect")})))
	})

	It("records a plain exchange in full, redacting credential headers and those named in redact", func() {
		upstream := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, _ *nethttp.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Set-Cookie", "session="+secret)
			_, _ = io.WriteString(w, `{"ok":true}`)
		}))
		DeferCleanup(upstream.Close)
		managed := start(`{"redact": ["X-Tenant"]}`)
		proxied, _ := client(managed, nil)

		request, err := nethttp.NewRequest(nethttp.MethodPost, upstream.URL+"/v1/things?page=2", strings.NewReader(`{"name":"widget"}`))
		Expect(err).ToNot(HaveOccurred())
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", "Bearer "+secret)
		request.Header.Set("X-Tenant", secret)
		request.Header.Set("Accept", "application/json")
		before := time.Now()
		Expect(read(proxied.Do(request))).To(MatchJSON(`{"ok":true}`), "capturing the body must not consume it")

		recorded := stop(managed)
		Expect(recorded).To(HaveLen(1))
		exchange := recorded[0]
		Expect(exchange.StartedAt).To(BeTemporally(">=", before.Truncate(time.Millisecond)))
		Expect(exchange.Method).To(Equal(nethttp.MethodPost))
		Expect(exchange.URL).To(Equal(upstream.URL + "/v1/things?page=2"))
		Expect(exchange.Status).To(Equal(nethttp.StatusOK))
		Expect(exchange.Request.QueryString).To(Equal([]har.QueryString{{Name: "page", Value: "2"}}))
		Expect(exchange.Request.PostData.Text).To(MatchJSON(`{"name":"widget"}`))
		Expect(exchange.Response.Content.Text).To(MatchJSON(`{"ok":true}`))
		Expect(exchange.Response.Content.MimeType).To(Equal("application/json"))
		Expect(exchange.Request.Headers).To(ContainElements(
			har.Header{Name: "Accept", Value: "application/json"}, har.Header{Name: "X-Tenant", Value: "[redacted]"}))
		stored, err := json.Marshal(env.Rows(managed.Session().Snapshot()))
		Expect(err).ToNot(HaveOccurred())
		Expect(string(stored)).ToNot(ContainSubstring(secret))
	})

	It("records only the hosts and paths its globs name, still proxying the rest", func() {
		upstream := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, _ *nethttp.Request) {
			_, _ = io.WriteString(w, "ok")
		}))
		DeferCleanup(upstream.Close)

		byPath := start(`{"paths": ["/api/*"]}`)
		proxied, _ := client(byPath, nil)
		for _, path := range []string{"/src/index.tsx", "/api/status", "/api/sessions/s1"} {
			Expect(read(proxied.Get(upstream.URL + path))).To(Equal("ok"))
		}
		var paths []string
		for _, exchange := range stop(byPath) {
			paths = append(paths, strings.TrimPrefix(exchange.URL, upstream.URL))
		}
		Expect(paths).To(Equal([]string{"/api/status"}))

		byHost := start(`{"hosts": ["*.example.com"]}`)
		proxied, _ = client(byHost, nil)
		Expect(read(proxied.Get(upstream.URL))).To(Equal("ok"))
		Expect(stop(byHost)).To(BeEmpty())
	})

	It("keeps no more of a body than bodies allows, and none when bodies is 0, while the client gets all of it", func() {
		payload := strings.Repeat("x", 500)
		upstream := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, _ *nethttp.Request) {
			_, _ = io.WriteString(w, payload)
		}))
		DeferCleanup(upstream.Close)

		capped := start(`{"bodies": "100"}`)
		proxied, _ := client(capped, nil)
		Expect(read(proxied.Get(upstream.URL))).To(Equal(payload))
		recorded := stop(capped)
		Expect(recorded).To(HaveLen(1))
		Expect(recorded[0].Response.Content).To(Equal(har.Content{Size: 500, MimeType: "text/plain; charset=utf-8", Text: payload[:100], Truncated: true}))

		none := start(`{"bodies": "0"}`)
		proxied, _ = client(none, nil)
		Expect(read(proxied.Get(upstream.URL))).To(Equal(payload))
		recorded = stop(none)
		Expect(recorded).To(HaveLen(1))
		Expect(recorded[0].Status).To(Equal(nethttp.StatusOK))
		Expect(recorded[0].Response.Content.Text).To(BeEmpty())
	})

	Describe("an event stream", func() {
		const first, second = "event: entry\ndata: {\"id\":\"a\"}\n\n", "event: entry\ndata: {\"id\":\"b\"}\n\n"
		var upstream *httptest.Server
		var release func()

		BeforeEach(func() {
			gate := make(chan struct{})
			var once sync.Once
			release = func() { once.Do(func() { close(gate) }) }
			upstream = httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, _ *nethttp.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, first)
				w.(nethttp.Flusher).Flush()
				<-gate
				_, _ = io.WriteString(w, second)
			}))
			DeferCleanup(upstream.Close)
			DeferCleanup(release)
		})

		// open reads the stream's first frame through the proxy, while the
		// stream is still open.
		open := func(managed *query.ManagedSession) *nethttp.Response {
			proxied, _ := client(managed, nil)
			response, err := proxied.Get(upstream.URL + "/stream")
			Expect(err).ToNot(HaveOccurred())
			DeferCleanup(response.Body.Close)
			frame := make([]byte, len(first))
			_, err = io.ReadFull(response.Body, frame)
			Expect(err).ToNot(HaveOccurred(), "the first frame must reach the client while the stream is open")
			Expect(string(frame)).To(Equal(first))
			return response
		}

		It("is recorded whole once it ends", func() {
			managed := start(`{"bodies": "64KiB"}`)
			response := open(managed)
			release()
			Expect(read(response, nil)).To(Equal(second))
			Eventually(func() []tracehttp.Exchange { return exchanges(whole(managed)) }).Should(HaveLen(1))
			recorded := stop(managed)
			Expect(recorded[0].Response.Content).To(Equal(har.Content{Size: int64(len(first + second)), MimeType: "text/event-stream", Text: first + second}))
			Expect(recorded[0].Comment).To(BeEmpty())
		})

		It("keeps no more text than bodies allows but counts every byte", func() {
			managed := start(`{"bodies": "16"}`)
			response := open(managed)
			release()
			Expect(read(response, nil)).To(Equal(second))
			Eventually(func() []tracehttp.Exchange { return exchanges(whole(managed)) }).Should(HaveLen(1))
			recorded := stop(managed)
			Expect(recorded[0].Response.Content).To(Equal(har.Content{
				Size: int64(len(first + second)), MimeType: "text/event-stream", Text: (first + second)[:16], Truncated: true,
			}))
		})

		It("still open when the capture stops is recorded with what has passed, and says so", func() {
			managed := start(`{}`)
			open(managed)
			recorded := stop(managed)
			Expect(recorded).To(HaveLen(1))
			Expect(recorded[0].Response.Content).To(Equal(har.Content{Size: int64(len(first)), MimeType: "text/event-stream", Text: first}))
			Expect(recorded[0].Comment).To(Equal("event stream still open when recorded"))
		})
	})

	It("records a CONNECT tunnel by the bytes that crossed it, never claiming its payload", func() {
		upstream := httptest.NewTLSServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, _ *nethttp.Request) {
			_, _ = io.WriteString(w, "over tls")
		}))
		DeferCleanup(upstream.Close)
		managed := start(`{}`)
		proxied, transport := client(managed, upstream.Client().Transport.(*nethttp.Transport).TLSClientConfig)
		Expect(read(proxied.Get(upstream.URL))).To(Equal("over tls"))
		transport.CloseIdleConnections()

		recorded := stop(managed)
		Expect(recorded).To(HaveLen(1))
		host := strings.TrimPrefix(upstream.URL, "https://")
		Expect(recorded[0]).To(MatchFields(IgnoreExtras, Fields{
			"Method": Equal(nethttp.MethodConnect), "URL": Equal("https://" + host), "Host": Equal(host),
			"Status": Equal(nethttp.StatusOK), "Tunnelled": BeTrue(), "BytesIn": BeNumerically(">", 0), "BytesOut": BeNumerically(">", 0),
			"ServerIPAddress": Equal("127.0.0.1"), "Timings": MatchFields(IgnoreExtras, Fields{"Send": Equal(-1.0)}),
		}))
		Expect(recorded[0].Response.Content.Text).To(BeEmpty())
	})

	Describe("in mitm mode", func() {
		var upstream *httptest.Server

		BeforeEach(func() {
			upstream = httptest.NewTLSServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"path":"`+r.URL.Path+`"}`)
			}))
			DeferCleanup(upstream.Close)
		})

		It("decrypts an HTTPS exchange for a client trusting the exported CA, which only the capture's life outlives", func() {
			managed := start(`{"mode": "mitm"}`)
			exports := managed.Status().Exports
			caPath := exports["SSL_CERT_FILE"]
			for _, name := range trustEnv {
				Expect(exports).To(HaveKeyWithValue(name, caPath))
			}
			Expect(managed.Status().Metadata).To(ContainElement(MatchFields(IgnoreExtras, Fields{"Name": Equal("ca"), "Value": Equal(caPath)})))

			pemBytes, err := os.ReadFile(caPath)
			Expect(err).ToNot(HaveOccurred())
			block, rest := pem.Decode(pemBytes)
			Expect(block.Type).To(Equal("CERTIFICATE"))
			Expect(rest).To(BeEmpty(), "the private key must never leave the process")
			ca, err := x509.ParseCertificate(block.Bytes)
			Expect(err).ToNot(HaveOccurred())
			Expect(ca.IsCA).To(BeTrue())
			Expect(ca.NotAfter).To(BeTemporally("~", time.Now().Add(time.Hour), time.Minute))

			pool := x509.NewCertPool()
			pool.AddCert(ca)
			proxied, _ := client(managed, &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12})
			Expect(read(proxied.Get(upstream.URL + "/v1/secrets"))).To(MatchJSON(`{"path":"/v1/secrets"}`))

			recorded := stop(managed)
			Expect(recorded).To(HaveLen(1))
			Expect(recorded[0].Method).To(Equal(nethttp.MethodGet))
			Expect(recorded[0].URL).To(ContainSubstring("/v1/secrets"))
			Expect(recorded[0].Tunnelled).To(BeFalse())
			Expect(recorded[0].Response.Content.Text).To(MatchJSON(`{"path":"/v1/secrets"}`))
			Expect(filepath.Dir(caPath)).ToNot(BeADirectory(), "the CA is removed with the capture")
		})

		It("leaves a host outside hosts encrypted: the client sees the server's own certificate", func() {
			managed := start(`{"mode": "mitm", "hosts": ["*.example.com"]}`)
			pool := x509.NewCertPool()
			pool.AddCert(upstream.Certificate())
			proxied, _ := client(managed, &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12})
			Expect(read(proxied.Get(upstream.URL + "/untouched"))).To(MatchJSON(`{"path":"/untouched"}`))
			Expect(stop(managed)).To(BeEmpty())
		})
	})

	It("exports a marked window as HAR holding exactly its exchanges, and summarizes tunnels by their bytes", func() {
		upstream := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, _ *nethttp.Request) {
			_, _ = io.WriteString(w, "ok")
		}))
		DeferCleanup(upstream.Close)
		tlsUpstream := httptest.NewTLSServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, _ *nethttp.Request) {
			_, _ = io.WriteString(w, "over tls")
		}))
		DeferCleanup(tlsUpstream.Close)
		managed := start(`{}`)
		proxied, _ := client(managed, nil)
		mark := func() int64 {
			seq, err := env.Runtime.Mark(context.Background(), managed)
			Expect(err).ToNot(HaveOccurred())
			return seq
		}

		Expect(read(proxied.Get(upstream.URL + "/before"))).To(Equal("ok"))
		from := mark()
		Expect(read(proxied.Get(upstream.URL + "/first"))).To(Equal("ok"))
		Expect(read(proxied.Post(upstream.URL+"/second", "text/plain", strings.NewReader("body")))).To(Equal("ok"))
		to := mark()
		Expect(read(proxied.Get(upstream.URL + "/after"))).To(Equal("ok"))
		tunnel, transport := client(managed, tlsUpstream.Client().Transport.(*nethttp.Transport).TLSClientConfig)
		Expect(read(tunnel.Get(tlsUpstream.URL))).To(Equal("over tls"))
		transport.CloseIdleConnections()
		stop(managed)

		window := whole(managed)
		window.AfterSeq, window.ToSeq = from, to
		var out bytes.Buffer
		Expect(env.Runtime.Export(context.Background(), window, tracehttp.HAR.Name, &out)).To(Succeed())
		var file struct {
			Log struct{ Entries []tracehttp.Entry } `json:"log"`
		}
		Expect(json.Unmarshal(out.Bytes(), &file)).To(Succeed())
		var urls []string
		for _, entry := range file.Log.Entries {
			urls = append(urls, entry.Request.Method+" "+strings.TrimPrefix(entry.Request.URL, upstream.URL))
		}
		Expect(urls).To(Equal([]string{"GET /first", "POST /second"}))

		summary, err := env.Runtime.Summarize(context.Background(), whole(managed))
		Expect(err).ToNot(HaveOccurred())
		Expect(summary).To(MatchKeys(IgnoreExtras, Keys{
			"count": Equal(5), "entries": Equal(5), "errors": Equal(0), "tunnelled": Equal(1),
			"bytes_in": BeNumerically(">", 0), "bytes_out": BeNumerically(">", 0),
			"methods": Equal(map[string]int{"GET": 3, "POST": 1, "CONNECT": 1}),
		}))
	})

	It("stops listening once the capture ends", func() {
		managed := start(`{}`)
		addr := managed.Status().Handle
		stop(managed)
		_, err := net.DialTimeout("tcp", addr, time.Second)
		Expect(err).To(HaveOccurred())
	})

	It("refuses a mode, size, glob or name it could not run with", func() {
		plugin := httpproxy.NewKind()
		Expect(plugin.ValidateParams(json.RawMessage(`{"bodies": "4MiB", "mode": "mitm"}`))).To(Succeed())
		for params, problem := range map[string]string{
			`{"mode": "transparent"}`: "mode",
			`{"mode": ""}`:            "mode",
			`{"bodies": "lots"}`:      "bodies",
			`{"bodies": "-1"}`:        "bodies",
			`{"bodies": "5MiB"}`:      "bodies",
			`{"hosts": ["["]}`:        "hosts",
			`{"paths": ["/api/["]}`:   "paths",
			`{"redact": [" "]}`:       "redact",
			`{"noProxy": [""]}`:       "noProxy",
		} {
			Expect(plugin.ValidateParams(json.RawMessage(params))).To(MatchError(ContainSubstring(problem)), params)
		}
	})
})
