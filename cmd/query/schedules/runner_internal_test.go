package schedules

import (
	"context"
	"errors"

	"github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/cmd/query/profiles"
	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
)

// scheduledProvider records that it read rows in the log the hook writes to,
// so a spec can tell "the hook ran first" from "the hook ran at all".
type scheduledProvider struct{ events *[]string }

func (scheduledProvider) Type() string { return "scheduled-mock" }

func (p scheduledProvider) Execute(dbcontext.Context, query.ProviderRequest) ([]query.Row, error) {
	*p.events = append(*p.events, "execute")
	return []query.Row{{"name": "a"}}, nil
}

var _ = ginkgo.Describe("a scheduled query", func() {
	var (
		events  []string
		params  []map[string]any
		hookErr error
		runner  *Runner
	)

	ginkgo.BeforeEach(func() {
		events, params, hookErr = nil, nil, nil
		query.RegisterProvider(scheduledProvider{events: &events})
		store, err := profiles.NewFileStore(ginkgo.GinkgoT().TempDir())
		Expect(err).ToNot(HaveOccurred())
		Expect(store.Save(context.Background(), query.Profile{
			Name: "stream-results", Provider: query.ProviderConfig{Type: "scheduled-mock"}, Query: "select name",
			Params:  []query.ParamDef{{Name: "stream", Required: true}},
			Columns: []query.ColumnDef{{Name: "name", Type: query.ColumnTypeString}},
		})).To(Succeed())
		runner, err = NewRunner(RunnerOptions{
			Store:    func() (*Store, error) { return nil, errors.New("no schedule store in this spec") },
			Profiles: func() (profiles.Store, error) { return store, nil },
			Context:  func() dbcontext.Context { return dbcontext.New() },
			BeforeExecute: func(_ context.Context, reads []profiles.ReadRequest) (func(), error) {
				events = append(events, "hook:"+reads[0].Profile.Name)
				params = append(params, reads[0].Params)
				return func() { events = append(events, "release") }, hookErr
			},
		})
		Expect(err).ToNot(HaveOccurred())
	})

	schedule := Schedule{Name: "nightly", Query: &QuerySpec{Profile: "stream-results", Params: map[string]string{"stream": "run-1"}}}

	ginkgo.It("prepares the profile's data before reading it, with the schedule's params", func() {
		result, _, err := runner.read(dbcontext.New(), schedule)
		Expect(err).ToNot(HaveOccurred())
		Expect(result.Rows).To(HaveLen(1))
		Expect(events).To(Equal([]string{"hook:stream-results", "execute", "release"}))
		Expect(params).To(Equal([]map[string]any{{"stream": "run-1"}}))
	})

	ginkgo.It("reads nothing when the data cannot be prepared", func() {
		hookErr = errors.New("index unavailable")
		_, _, err := runner.read(dbcontext.New(), schedule)
		Expect(err).To(MatchError(ContainSubstring("index unavailable")))
		Expect(events).To(Equal([]string{"hook:stream-results", "release"}))
	})
})
