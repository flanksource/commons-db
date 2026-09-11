package profiles

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
)

// hookedProvider records that it read rows, in the same log the hook writes
// to, so a spec can tell "the hook ran first" from "the hook ran at all".
type hookedProvider struct {
	events *[]string
	err    *error
}

func (hookedProvider) Type() string { return "hooked-mock" }

func (p hookedProvider) Execute(dbcontext.Context, query.ProviderRequest) ([]query.Row, error) {
	*p.events = append(*p.events, "execute")
	if p.err != nil && *p.err != nil {
		return nil, *p.err
	}
	return []query.Row{{"name": "a"}}, nil
}

func hookedProfile() query.Profile {
	return query.Profile{
		Name:     "stream-results",
		Provider: query.ProviderConfig{Type: "hooked-mock"},
		Query:    "select name",
		Params:   []query.ParamDef{{Name: "stream", Required: true}},
		Columns:  []query.ColumnDef{{Name: "name", Type: query.ColumnTypeString}},
		Replay:   &query.ReplaySpec{URL: "'https://example.com'"},
	}
}

var _ = Describe("BeforeExecute", func() {
	var (
		events   []string
		requests [][]ReadRequest
		hookErr  error
		queryErr error
		service  *Service
		handler  http.Handler
	)

	BeforeEach(func() {
		events, requests, hookErr, queryErr = nil, nil, nil, nil
		query.RegisterProvider(hookedProvider{events: &events, err: &queryErr})
		store, err := NewFileStore(GinkgoT().TempDir())
		Expect(err).ToNot(HaveOccurred())
		Expect(store.Save(context.Background(), hookedProfile())).To(Succeed())
		service, err = New(Options{
			Store:      func() (Store, error) { return store, nil },
			Context:    func() dbcontext.Context { return dbcontext.New() },
			DecodeBody: func(_ context.Context, body map[string]any) (map[string]any, error) { return body, nil },
			BeforeExecute: func(_ context.Context, reads []ReadRequest) (func(), error) {
				events = append(events, "hook")
				requests = append(requests, reads)
				return func() { events = append(events, "release") }, hookErr
			},
		})
		Expect(err).ToNot(HaveOccurred())
		handler, err = service.Handler("/api/v1", &nextMarker{})
		Expect(err).ToNot(HaveOccurred())
	})

	execError := func(status int, path string) execError {
		response := get(handler, path, "application/json")
		Expect(response.Code).To(Equal(status), response.Body.String())
		var body execError
		Expect(json.Unmarshal(response.Body.Bytes(), &body)).To(Succeed())
		return body
	}

	It("runs before a profile's rows are read over HTTP, with the resolved profile and the request's params", func() {
		response := get(handler, "/api/v1/profile/stream-results?stream=run-1", "application/json")
		Expect(response.Code).To(Equal(http.StatusOK), response.Body.String())
		Expect(events).To(Equal([]string{"hook", "execute", "release"}))
		Expect(requests).To(Equal([][]ReadRequest{{{Profile: hookedProfile(), Params: map[string]any{"stream": "run-1"}}}}))
	})

	It("answers 404 and reads nothing when the hook reports the data the request names missing", func() {
		hookErr = fmt.Errorf("stream %q: %w", "run-9", ErrProfileDataNotFound)
		body := execError(http.StatusNotFound, "/api/v1/profile/stream-results?stream=run-9")
		Expect(body.Code).To(Equal("profile_data_not_found"))
		Expect(body.Message).To(ContainSubstring("run-9"))
		Expect(events).To(Equal([]string{"hook", "release"}))
	})

	It("answers 410 when the hook reports the data expired", func() {
		hookErr = fmt.Errorf("stream run-9: %w", dbcontext.ErrConnectionExpired)
		Expect(execError(http.StatusGone, "/api/v1/profile/stream-results?stream=run-9").Code).To(Equal("profile_data_expired"))
	})

	It("answers 400 when the hook reports the request itself invalid, and reads nothing", func() {
		hookErr = fmt.Errorf("stream %q: %w", "run/1", ErrProfileRequestInvalid)
		body := execError(http.StatusBadRequest, "/api/v1/profile/stream-results?stream=run/1")
		Expect(body.Code).To(Equal("invalid_params"))
		Expect(body.Message).To(ContainSubstring("run/1"))
		Expect(events).To(Equal([]string{"hook", "release"}))
	})

	// A store that is down is the server's fault, not the caller's: a 400 would
	// tell a client to change a request that was fine.
	It("answers 500 for any other hook failure, and reads nothing", func() {
		hookErr = errors.New("index unavailable")
		Expect(execError(http.StatusInternalServerError, "/api/v1/profile/stream-results?stream=run-1").Code).To(Equal("prepare_failed"))
		Expect(events).To(Equal([]string{"hook", "release"}))
	})

	It("releases prepared data when the backend read fails", func() {
		queryErr = errors.New("backend unavailable")
		response := get(handler, "/api/v1/profile/stream-results?stream=run-1", "application/json")
		Expect(response.Code).To(Equal(http.StatusBadRequest), response.Body.String())
		Expect(events).To(Equal([]string{"hook", "execute", "release"}))
	})

	It("normalises a successful hook's cleanup to exactly once", func() {
		released := 0
		release, err := PrepareReads(context.Background(), func(context.Context, []ReadRequest) (func(), error) {
			return func() { released++ }, nil
		}, []ReadRequest{{Profile: hookedProfile()}})
		Expect(err).ToNot(HaveOccurred())
		release()
		release()
		Expect(released).To(Equal(1))
	})

	DescribeTable("guards every other read of a profile's data",
		func(read func(*Service) error) {
			hookErr = errors.New("index unavailable")
			Expect(read(service)).To(MatchError(hookErr))
			Expect(events).To(Equal([]string{"hook", "release"}))
		},
		Entry("a run", func(s *Service) error {
			_, err := s.Run(context.Background(), "stream-results", RunFlags{Params: []string{"stream=run-1"}})
			return err
		}),
		Entry("a surface listing", func(s *Service) error {
			_, err := s.executeRows(context.Background(), "stream-results", map[string]string{"stream": "run-1"})
			return err
		}),
		Entry("an inspection", func(s *Service) error {
			_, err := s.Inspect(context.Background(), "stream-results", InspectFlags{Params: []string{"stream=run-1"}})
			return err
		}),
		Entry("a replay preview", func(s *Service) error {
			_, err := s.Replay(context.Background(), "stream-results", ReplayFlags{Params: []string{"stream=run-1"}})
			return err
		}),
		Entry("a reconciliation", func(s *Service) error {
			_, err := s.Reconcile(context.Background(), "stream-results", ReconcileFlags{
				Dest: "stream-results", KeyColumns: []string{"name"},
				SourceFilters: []string{"stream=run-1"}, DestFilters: []string{"stream=run-2"},
			})
			return err
		}),
		// A filter value lookup is guarded too; it answers over HTTP with the
		// status the failure names (before_execute_lookup_test.go).
	)

	It("prepares both reconciliation sides in one batch and releases them after both reads", func() {
		_, err := service.Reconcile(context.Background(), "stream-results", ReconcileFlags{
			Dest: "stream-results", KeyColumns: []string{"name"},
			SourceFilters: []string{"stream=run-1"}, DestFilters: []string{"stream=run-2"},
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(requests).To(HaveLen(1))
		Expect(requests[0]).To(HaveLen(2))
		Expect(requests[0][0].Params).To(Equal(map[string]any{"stream": "run-1"}))
		Expect(requests[0][1].Params).To(Equal(map[string]any{"stream": "run-2"}))
		Expect(events).To(Equal([]string{"hook", "execute", "execute", "release"}))
	})
})
