package profiles

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/flanksource/clicky/entity"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
)

// hookedLookupProvider records a filter value lookup in the log the hook writes
// to, so a spec can tell "the hook ran first" from "the lookup read anyway".
type hookedLookupProvider struct{ events *[]string }

func (hookedLookupProvider) Type() string { return "clickhouse" }

func (hookedLookupProvider) Execute(dbcontext.Context, query.ProviderRequest) ([]query.Row, error) {
	return nil, nil
}

func (p hookedLookupProvider) LookupFilterValues(
	dbcontext.Context, query.ProviderRequest, query.ColumnFilterBinding, string, int,
) ([]query.FilterOption, *query.Total, error) {
	*p.events = append(*p.events, "lookup")
	return []query.FilterOption{{Value: "api"}}, &query.Total{Value: 1, Exact: true}, nil
}

// A filter lookup reads the profile's data as much as an execution does, so the
// hook guards it too — and a failed hook has to mean the same thing to a caller
// on either path. Before, the lookup answered every failure as a 500
// internal_error, which told a browser whose stream had expired that the server
// was broken.
var _ = Describe("BeforeExecute ahead of a filter lookup over HTTP", func() {
	const lookupPath = "/api/v1/profile/profile-stream-results?stream=run-9&__lookup=filters"
	var (
		events  []string
		hookErr error
		handler http.Handler
	)

	BeforeEach(func() {
		events, hookErr = nil, nil
		query.RegisterProvider(hookedLookupProvider{events: &events})
		profile := lookupProfile("stream-results")
		profile.Params = []query.ParamDef{{Name: "stream", Required: true}}
		store, err := NewFileStore(GinkgoT().TempDir())
		Expect(err).ToNot(HaveOccurred())
		Expect(store.Save(context.Background(), profile)).To(Succeed())
		service, err := New(Options{
			Store:      func() (Store, error) { return store, nil },
			Context:    func() dbcontext.Context { return dbcontext.New() },
			DecodeBody: func(_ context.Context, body map[string]any) (map[string]any, error) { return body, nil },
			BeforeExecute: func(_ context.Context, reads []ReadRequest) (func(), error) {
				events = append(events, "hook:"+reads[0].Profile.Name)
				return func() { events = append(events, "release") }, hookErr
			},
		})
		Expect(err).ToNot(HaveOccurred())
		service.RegisterFamily()
		DeferCleanup(func() { entity.UnregisterDynamicEntityFamily(profileFamilyName) })
		handler, err = service.Handler("/api/v1", newFamilyMux())
		Expect(err).ToNot(HaveOccurred())
	})

	It("reads the filter values only after the hook has prepared them", func() {
		response := get(handler, lookupPath, "application/json+clicky")
		Expect(response.Code).To(Equal(http.StatusOK), response.Body.String())
		Expect(events).To(Equal([]string{"hook:stream-results", "lookup", "release"}))
	})

	DescribeTable("answers with the status an execution answers the same failure with, and reads nothing",
		func(cause error, status int, code string) {
			hookErr = fmt.Errorf("stream run-9: %w", cause)
			response := get(handler, lookupPath, "application/json+clicky")
			Expect(response.Code).To(Equal(status), response.Body.String())
			var body entity.ErrorResponse
			Expect(json.Unmarshal(response.Body.Bytes(), &body)).To(Succeed(), response.Body.String())
			Expect(body.Code).To(Equal(code))
			Expect(body.Message).To(ContainSubstring("run-9"))
			Expect(events).To(Equal([]string{"hook:stream-results", "release"}))
		},
		Entry("404 when the data the request names is missing", ErrProfileDataNotFound, http.StatusNotFound, "profile_data_not_found"),
		Entry("410 when the data expired", dbcontext.ErrConnectionExpired, http.StatusGone, "profile_data_expired"),
		Entry("400 when the request itself is invalid", ErrProfileRequestInvalid, http.StatusBadRequest, "invalid_params"),
		Entry("500 for anything else, which is the server's", errors.New("index unavailable"), http.StatusInternalServerError, "prepare_failed"),
	)
})
