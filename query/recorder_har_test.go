package query_test

import (
	"net/http"
	"net/http/httptest"

	dbconnection "github.com/flanksource/commons-db/connection"
	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/query"
	commons "github.com/flanksource/commons/context"
	"github.com/flanksource/commons/har"
	"github.com/flanksource/commons/logger"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// harProvider issues one real HTTP request through the context's observability
// transport, which is the only way to prove entries travel the path they travel
// in production rather than a path the test built.
type harProvider struct {
	typ string
	url string
}

func (p *harProvider) Type() string { return p.typ }

func (p *harProvider) Execute(ctx dbcontext.Context, req query.ProviderRequest) ([]query.Row, error) {
	req.Diagnostics.RecordRequest(req.Query, nil, nil)
	client := &http.Client{
		Transport: dbconnection.ApplyHTTPObservability(ctx, p.typ, http.DefaultTransport, nil),
	}
	response, err := client.Get(p.url)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	return []query.Row{{"status": response.StatusCode}}, nil
}

var _ = Describe("Recorder HAR capture", func() {
	var server *httptest.Server

	BeforeEach(func() {
		server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"hits":1}`))
		}))
		DeferCleanup(server.Close)
	})

	// The process logger stays at Info throughout. That is the production shape:
	// a browser opening a console must not start writing every request's headers
	// and bodies to the operator's terminal.
	quietContext := func() (dbcontext.Context, *logger.BufferedLogger) {
		buffer := logger.NewBufferedLogger(50)
		buffer.SetLogLevel(logger.Info)
		ctx := dbcontext.New(commons.WithLogger(buffer)).
			WithConnectionResolver(func(string) (*models.Connection, error) {
				return &models.Connection{Name: "search", Type: models.ConnectionTypeOpenSearch}, nil
			})
		return ctx, buffer
	}

	armedContext := func(_ *harProvider, level logger.LogLevel) (dbcontext.Context, *query.Recorder) {
		ctx, _ := quietContext()
		recorder := query.NewRecorder(query.RecorderOptions{ID: "har-1", Level: level})
		return query.WithRecorder(ctx, recorder), recorder
	}

	run := func(ctx dbcontext.Context, provider *harProvider) {
		query.RegisterProvider(provider)
		_, err := query.Execute(ctx, query.Profile{
			Name:     "search",
			Provider: query.ProviderConfig{Type: provider.typ, Connection: "connection://search"},
			Query:    "/_search",
		})
		Expect(err).ToNot(HaveOccurred())
	}

	It("delivers each entry to the record exactly once", func() {
		provider := &harProvider{typ: "recorder-har-once", url: server.URL}
		ctx, recorder := armedContext(provider, logger.Trace)
		run(ctx, provider)
		recorder.Finish(query.FinishOptions{Status: 200})

		detail := recorder.Detail()
		Expect(detail.HAR).ToNot(BeNil())
		Expect(detail.HAR.Log.Entries).To(HaveLen(1))
		Expect(detail.HAR.Log.Version).To(Equal("1.2"))
		Expect(detail.Summary.Counts.HAREntries).To(Equal(1))
	})

	// The recorder must not take the context's parent-collector slot. A CLI
	// --har export installs one there, and displacing it would produce a silently
	// empty file.
	It("leaves an already-installed parent collector receiving its entries", func() {
		provider := &harProvider{typ: "recorder-har-parent", url: server.URL}
		ctx, recorder := armedContext(provider, logger.Trace)
		parent := har.NewCollector(har.DefaultConfig())
		run(ctx.WithHARCollector(parent), provider)
		recorder.Finish(query.FinishOptions{Status: 200})

		Expect(parent.Entries()).To(HaveLen(1), "the export collector still sees the request")
		Expect(recorder.Detail().HAR.Log.Entries).To(HaveLen(1), "and the record is not doubled")
	})

	It("captures no bodies below trace, so arming at debug buffers no payloads", func() {
		provider := &harProvider{typ: "recorder-har-debug", url: server.URL}
		ctx, recorder := armedContext(provider, logger.Debug)
		run(ctx, provider)
		recorder.Finish(query.FinishOptions{Status: 200})

		detail := recorder.Detail()
		Expect(detail.HAR).ToNot(BeNil())
		Expect(detail.HAR.Log.Entries).To(HaveLen(1))
		Expect(detail.HAR.Log.Entries[0].Response.Content.Text).To(BeEmpty())
	})

	It("captures the response body at trace", func() {
		provider := &harProvider{typ: "recorder-har-trace", url: server.URL}
		ctx, recorder := armedContext(provider, logger.Trace2)
		run(ctx, provider)
		recorder.Finish(query.FinishOptions{Status: 200})

		Expect(recorder.Detail().HAR.Log.Entries[0].Response.Content.Text).To(ContainSubstring(`"hits"`))
	})

	It("records nothing for an unarmed run", func() {
		provider := &harProvider{typ: "recorder-har-unarmed", url: server.URL}
		ctx, _ := quietContext()
		run(ctx, provider)

		Expect(query.RecorderFrom(ctx)).To(BeNil())
	})

	// The whole point of arming per-request rather than raising the process
	// logger: one browser tab must not change what every other request writes to
	// the operator's terminal.
	It("captures bodies for the console while the operator's log stays quiet", func() {
		provider := &harProvider{typ: "recorder-har-quiet", url: server.URL}
		ctx, buffer := quietContext()
		recorder := query.NewRecorder(query.RecorderOptions{ID: "har-quiet", Level: logger.Trace2})
		run(query.WithRecorder(ctx, recorder), provider)
		recorder.Finish(query.FinishOptions{Status: 200})

		Expect(recorder.Detail().HAR.Log.Entries[0].Response.Content.Text).To(ContainSubstring(`"hits"`),
			"the console asked for bodies and gets them")
		Expect(buffer.GetLogs()).To(BeEmpty(), "and the terminal never saw them")

	})

	It("still logs for an operator at trace when no console is open", func() {
		provider := &harProvider{typ: "recorder-har-operator", url: server.URL}
		buffer := logger.NewBufferedLogger(50)
		buffer.SetLogLevel(logger.Trace2)
		ctx := dbcontext.New(commons.WithLogger(buffer)).
			WithConnectionResolver(func(string) (*models.Connection, error) {
				return &models.Connection{Name: "search", Type: models.ConnectionTypeOpenSearch}, nil
			})
		run(ctx, provider)

		Expect(buffer.GetLogs()).ToNot(BeEmpty())
	})
})
