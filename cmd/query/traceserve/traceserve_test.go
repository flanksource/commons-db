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
	"path/filepath"
	"strings"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/cmd/query/traceserve"
	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/recordstore"
	"github.com/flanksource/commons-db/tracing"
	"github.com/flanksource/commons-db/tracing/stacks"
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
	dumpers, err := stacks.NewDumpers(stacks.InProcess())
	Expect(err).ToNot(HaveOccurred())
	Expect(kinds.RegisterKind(terminal.Kind, terminal.NewKind())).To(Succeed())
	Expect(kinds.RegisterKind(stacks.Kind, stacks.NewKind(dumpers))).To(Succeed())
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

	It("reads a window whose records are older than a day, with no time range named", func() {
		const oldStream = "cast-old"
		_, err := recordstore.AppendTyped(context.Background(), store.runtime.Results.Backend, oldStream, terminal.Kind, castEvents(time.Now().UTC().Add(-72*time.Hour)))
		Expect(err).ToNot(HaveOccurred())
		rows := store.rows(terminal.Kind, "stream="+oldStream+"&toSeq=3&limit=100")
		Expect(column(rows, "seq")).To(ConsistOf(float64(1), float64(2), float64(3)), "the window is not clipped to a default time range")
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

	It("serves the profile OpenAPI document and nothing of the query app", func() {
		status, body, _ := store.do(http.MethodGet, "/api/openapi.json")
		Expect(status).To(Equal(http.StatusOK))
		Expect(body).To(ContainSubstring("traces/terminal"))

		for _, path := range []string{"/", "/profile-traces-terminal?stream=" + castStream, "/traces/terminal?stream=" + castStream, "/api/v1/profiles"} {
			status, body, _ = store.do(http.MethodGet, path)
			Expect(status).To(Equal(http.StatusNotFound), path)
			Expect(body).ToNot(ContainSubstring("<html"), path)
		}
	})

	It("refuses to start or stop a capture, but checkpoints a running one into its records", func() {
		status, _, _ := store.do(http.MethodPost, "/api/v1/traces/"+stacks.Kind+"/sessions")
		Expect(status).To(Equal(http.StatusForbidden))

		managed, err := store.runtime.Start(dbcontext.New(), tracing.StartRequest{
			Kind: stacks.Kind, Params: json.RawMessage(fmt.Sprintf(`{"rootPid": %d}`, os.Getpid())),
		})
		Expect(err).ToNot(HaveOccurred())
		session := managed.Session()
		status, body, _ := store.do(http.MethodPost, "/api/v1/sessions/"+session.ID()+"/checkpoint")
		Expect(status).To(Equal(http.StatusOK), body)
		status, _, _ = store.do(http.MethodPost, "/api/v1/sessions/"+session.ID()+"/stop")
		Expect(status).To(Equal(http.StatusForbidden))

		records := store.records(stacks.Kind, "stream="+session.Snapshot().Events.Stream)
		Expect(records).To(ContainElement(And(
			HaveKeyWithValue("transport", stacks.TransportInProcess), HaveKeyWithValue("raw", HavePrefix("goroutine ")),
		)), "the checkpoint dumped the host's goroutines")
	})

	It("reopens the store a closed host left, read-only, with its records", func() {
		store.close()
		handler, closeStore, err := traceserve.Reopen(traceserve.ReopenOptions{Dir: dir, Kinds: storeKinds()})
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(closeStore)
		server := httptest.NewServer(http.StripPrefix(mount, handler))
		DeferCleanup(server.Close)
		reopened := mounted{server: server}
		Expect(reopened.records(terminal.Kind, "stream="+castStream)).To(HaveLen(4))
		status, _, _ := reopened.do(http.MethodPost, "/api/v1/traces/"+stacks.Kind+"/sessions")
		Expect(status).To(Equal(http.StatusForbidden))
	})
})

var _ = Describe("reopening a trace store directory", func() {
	It("is os.ErrNotExist for a store that is gone, and creates nothing", func() {
		dir := filepath.Join(GinkgoT().TempDir(), "pruned")
		_, _, err := traceserve.Reopen(traceserve.ReopenOptions{Dir: dir, Kinds: storeKinds()})
		Expect(err).To(MatchError(os.ErrNotExist))
		Expect(dir).ToNot(BeADirectory())
	})

	It("refuses a path that is not a directory, or no kinds", func() {
		file := filepath.Join(GinkgoT().TempDir(), "store")
		Expect(os.WriteFile(file, []byte("not a store"), 0o600)).To(Succeed())
		_, _, err := traceserve.Reopen(traceserve.ReopenOptions{Dir: file, Kinds: storeKinds()})
		Expect(err).To(MatchError(ContainSubstring("is not a directory")))

		_, _, err = traceserve.Reopen(traceserve.ReopenOptions{Dir: GinkgoT().TempDir()})
		Expect(err).To(MatchError(ContainSubstring("kinds")))
	})
})
