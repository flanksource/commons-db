// Specs for serving a trace store through query: a host mounts the handler
// under a path of its own and reads, exports and checkpoints the captures in
// it, but cannot start or stop one.
package traceserve_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/cmd/query/traceserve"
	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/recordstore"
	"github.com/flanksource/commons-db/tracing"
	"github.com/flanksource/commons-db/tracing/process"
	"github.com/flanksource/commons-db/tracing/terminal"
)

const (
	mount      = "/query"
	castStream = "cast-1"
)

// castEvents are a terminal session of three output chunks and its exit, as
// seqs 1 to 4.
func castEvents(started time.Time) []terminal.Event {
	exit := 0
	return []terminal.Event{
		{At: started, Offset: 0, Kind: terminal.KindOutput, Data: "one\r\n", Width: 80, Height: 24},
		{At: started.Add(time.Second), Offset: 1, Kind: terminal.KindOutput, Data: "two\r\n", Width: 80, Height: 24},
		{At: started.Add(2 * time.Second), Offset: 2, Kind: terminal.KindOutput, Data: "three\r\n", Width: 80, Height: 24},
		{At: started.Add(3 * time.Second), Offset: 3, Kind: terminal.KindExit, ExitCode: &exit, Width: 80, Height: 24, Command: "echo"},
	}
}

func storeKinds() *tracing.Kinds {
	kinds := tracing.NewKinds()
	sampler := process.NewSampler()
	Expect(kinds.RegisterKind(terminal.Kind, terminal.NewKind())).To(Succeed())
	Expect(kinds.RegisterKind(process.TreeKind, process.NewTreeKind(sampler, process.TreeOptions{}))).To(Succeed())
	return kinds
}

type mounted struct {
	runtime *tracing.Runtime
	server  *httptest.Server
	close   func()
}

// serve opens the store in dir and mounts its handler under mount.
func serve(dir string) mounted {
	runtime, closeStore, err := traceserve.Open(traceserve.OpenOptions{Dir: dir, Kinds: storeKinds(), Retention: time.Hour})
	Expect(err).ToNot(HaveOccurred())
	var once sync.Once
	closeAll := func() {
		once.Do(func() {
			Expect(runtime.Sessions.StopAll(context.Background())).To(Succeed())
			Expect(closeStore()).To(Succeed())
		})
	}
	DeferCleanup(closeAll)
	handler, err := traceserve.Handler(traceserve.Options{Runtime: runtime, Authorize: traceserve.ReadOnly})
	Expect(err).ToNot(HaveOccurred())
	server := httptest.NewServer(http.StripPrefix(mount, handler))
	server.Client().CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	DeferCleanup(server.Close)
	return mounted{runtime: runtime, server: server, close: closeAll}
}

func (m mounted) do(method, path string) (int, string, http.Header) {
	request, err := http.NewRequest(method, m.server.URL+mount+path, nil)
	Expect(err).ToNot(HaveOccurred())
	request.Header.Set("Accept", "application/json")
	response, err := m.server.Client().Do(request)
	Expect(err).ToNot(HaveOccurred())
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(response.Body)
	Expect(err).ToNot(HaveOccurred())
	return response.StatusCode, string(body), response.Header
}

// rows reads kind's result profile: the columns it renders.
func (m mounted) rows(kind, query string) []map[string]any {
	status, body, _ := m.do(http.MethodGet, "/api/v1/profile/"+url.PathEscape(traceserve.ProfilePrefix+kind)+"?"+query)
	Expect(status).To(Equal(http.StatusOK), body)
	var rows []map[string]any
	Expect(json.Unmarshal([]byte(body), &rows)).To(Succeed(), body)
	return rows
}

// records exports kind's window as NDJSON: the records as the stream holds them.
func (m mounted) records(kind, query string) []map[string]any {
	status, body, _ := m.do(http.MethodGet, "/api/v1/traces/"+kind+"/export?format=ndjson&"+query)
	Expect(status).To(Equal(http.StatusOK), body)
	var records []map[string]any
	scanner := bufio.NewScanner(strings.NewReader(body))
	scanner.Buffer(make([]byte, 0, 1<<20), 16<<20)
	for scanner.Scan() {
		var record map[string]any
		Expect(json.Unmarshal(scanner.Bytes(), &record)).To(Succeed(), scanner.Text())
		records = append(records, record)
	}
	Expect(scanner.Err()).ToNot(HaveOccurred())
	return records
}

func column(rows []map[string]any, name string) []any {
	values := make([]any, 0, len(rows))
	for _, row := range rows {
		values = append(values, row[name])
	}
	return values
}

var _ = Describe("a trace store served through query", func() {
	var dir string
	var store mounted

	BeforeEach(func() {
		dir = GinkgoT().TempDir()
		store = serve(dir)
		_, err := recordstore.AppendTyped(context.Background(), store.runtime.Results.Backend, castStream, terminal.Kind, castEvents(time.Now().UTC()))
		Expect(err).ToNot(HaveOccurred())
	})

	It("reads a window of a stream through its kind's result profile and its export", func() {
		rows := store.rows(terminal.Kind, "stream="+castStream+"&afterSeq=1&toSeq=3&limit=100")
		Expect(column(rows, "seq")).To(ConsistOf(float64(2), float64(3)))
		Expect(column(rows, "offset")).To(ConsistOf(float64(1), float64(2)))

		records := store.records(terminal.Kind, "stream="+castStream+"&afterSeq=1&toSeq=3")
		Expect(column(records, "data")).To(Equal([]any{"two\r\n", "three\r\n"}), "the export holds the columns the profile hides")
	})

	It("downloads a window in the kind's own format", func() {
		status, body, header := store.do(http.MethodGet, "/api/v1/traces/terminal/export?format=cast&stream="+castStream+"&afterSeq=1")
		Expect(status).To(Equal(http.StatusOK), body)
		Expect(header.Get("Content-Disposition")).To(Equal(`attachment; filename="terminal-` + castStream + `.cast"`))
		lines := strings.Split(strings.TrimSpace(body), "\n")
		var castHeader map[string]any
		Expect(json.Unmarshal([]byte(lines[0]), &castHeader)).To(Succeed(), lines[0])
		Expect(castHeader).To(HaveKeyWithValue("version", float64(2)))
		events := strings.Join(lines[1:], "\n")
		Expect(events).To(ContainSubstring("two"))
		Expect(events).ToNot(ContainSubstring("one"), "seq 1 is outside the window")
	})

	It("serves the query app under the path the host mounts it at", func() {
		status, body, _ := store.do(http.MethodGet, "/profile-traces-terminal?stream="+castStream)
		Expect(status).To(Equal(http.StatusOK))
		Expect(body).To(ContainSubstring(`<base href="` + mount + `/"`))

		status, body, _ = store.do(http.MethodGet, "/api/openapi.json")
		Expect(status).To(Equal(http.StatusOK))
		Expect(body).To(ContainSubstring("traces/terminal"))

		status, body, _ = store.do(http.MethodGet, "/api/v1/profiles")
		Expect(status).To(Equal(http.StatusNotFound), "an API route it does not serve is not the app")
		Expect(body).ToNot(ContainSubstring("<html"))
	})

	It("links a kind's window to the app's page for it, under the mount", func() {
		window := "stream=" + castStream + "&afterSeq=1&toSeq=3"
		status, _, header := store.do(http.MethodGet, "/traces/http_proxy?"+window)
		Expect(status).To(Equal(http.StatusFound))
		location, err := url.Parse(store.server.URL + mount + "/traces/http_proxy")
		Expect(err).ToNot(HaveOccurred())
		Expect(location.ResolveReference(&url.URL{Path: strings.SplitN(header.Get("Location"), "?", 2)[0]}).Path).To(Equal(mount + "/profile-traces-http-proxy"))
		Expect(header.Get("Location")).To(HaveSuffix("?" + window))
	})

	It("refuses to start or stop a capture, but checkpoints a running one into its records", func() {
		status, _, _ := store.do(http.MethodPost, "/api/v1/traces/"+process.TreeKind+"/sessions")
		Expect(status).To(Equal(http.StatusForbidden))

		managed, err := store.runtime.Start(dbcontext.New(), tracing.StartRequest{
			Kind: process.TreeKind, Params: json.RawMessage(fmt.Sprintf(`{"rootPid": %d}`, os.Getpid())),
		})
		Expect(err).ToNot(HaveOccurred())
		session := managed.Session()
		status, body, _ := store.do(http.MethodPost, "/api/v1/sessions/"+session.ID()+"/checkpoint")
		Expect(status).To(Equal(http.StatusOK), body)
		status, _, _ = store.do(http.MethodPost, "/api/v1/sessions/"+session.ID()+"/stop")
		Expect(status).To(Equal(http.StatusForbidden))

		var root map[string]any
		for _, record := range store.records(process.TreeKind, "stream="+session.Snapshot().Events.Stream) {
			if record["isRoot"] == true {
				root = record
			}
		}
		Expect(root).ToNot(BeNil())
		Expect(root["stack"]).To(ContainSubstring("goroutine"), "the checkpoint dumped the host's goroutines onto its root")
	})

	It("reopens the store a closed host left, with its records", func() {
		store.close()
		Expect(serve(dir).records(terminal.Kind, "stream="+castStream)).To(HaveLen(4))
	})
})
