package profiles

import (
	"slices"

	"github.com/flanksource/clicky/rpc"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
)

// followableMock tails nothing; it exists so a query profile over it is one
// whose provider streams.
type followableMock struct{}

const followableMockType = "openapi-followable-mock"

func (followableMock) Type() string { return followableMockType }

func (followableMock) Execute(dbcontext.Context, query.ProviderRequest) ([]query.Row, error) {
	return nil, nil
}

func (followableMock) Stream(ctx dbcontext.Context, _ query.ProviderRequest, _ func(query.Row)) error {
	<-ctx.Done()
	return nil
}

var _ = Describe("the session start a profile's OpenAPI document offers", func() {
	BeforeEach(func() { query.RegisterProvider(followableMock{}) })

	sessionStart := func(profile query.Profile) (rpc.OpenAPIOperation, bool) {
		spec := &rpc.OpenAPISpec{Paths: map[string]rpc.OpenAPIPath{}, Clicky: &rpc.ClickySpecMeta{}}
		Expect(addProfileToSpec(spec, profile)).To(Succeed())
		operation, ok := spec.Paths["/api/v1/profile/"+profileSurfaceKey(profile.Name)+"/sessions"]["post"]
		return operation, ok
	}

	parameterNames := func(operation rpc.OpenAPIOperation) []string {
		names := make([]string, 0, len(operation.Parameters))
		for _, parameter := range operation.Parameters {
			names = append(names, parameter.Name)
		}
		return names
	}

	It("offers a query profile whose provider streams a session it can follow", func() {
		profile := sampleProfile("followable")
		profile.Provider = query.ProviderConfig{Type: followableMockType}

		operation, ok := sessionStart(profile)
		Expect(ok).To(BeTrue(), "no session start for a followable query profile")
		Expect(operation.OperationID).To(Equal("start-profile-followable-session"))
		Expect(operation.Description).To(ContainSubstring("follow=true"))
		Expect(parameterNames(operation)).To(ContainElements("region", "follow"))
		follow := operation.Parameters[slices.IndexFunc(operation.Parameters, func(p rpc.OpenAPIParameter) bool { return p.Name == "follow" })]
		Expect(follow.Required).To(BeTrue())
		Expect(follow.Schema.Enum).To(Equal([]any{true}))
	})

	It("offers no session start for a query profile whose provider answers once", func() {
		_, ok := sessionStart(sampleProfile("answers-once"))
		Expect(ok).To(BeFalse())
	})

	It("offers a declared trace profile a session start without a follow parameter", func() {
		profile := sampleProfile("declared-trace")
		profile.Provider = query.ProviderConfig{Type: followableMockType}
		profile.Trace = &query.TraceSpec{}

		operation, ok := sessionStart(profile)
		Expect(ok).To(BeTrue())
		Expect(parameterNames(operation)).ToNot(ContainElement("follow"))
	})

	It("no longer points a session's stop at DELETE", func() {
		profile := sampleProfile("stop-by-post")
		profile.Provider = query.ProviderConfig{Type: followableMockType}
		profile.Trace = &query.TraceSpec{}
		operation, _ := sessionStart(profile)
		Expect(operation.Description).To(ContainSubstring("POST /api/v1/sessions/{id}/stop"))
		Expect(operation.Description).ToNot(ContainSubstring("DELETE"))
	})
})

var _ = Describe("the sessions API's OpenAPI operations", func() {
	var spec *rpc.OpenAPISpec

	BeforeEach(func() {
		spec = &rpc.OpenAPISpec{Paths: map[string]rpc.OpenAPIPath{}}
		AddSessionsOpenAPI(spec)
	})

	parameterNames := func(operation rpc.OpenAPIOperation) []string {
		names := make([]string, 0, len(operation.Parameters))
		for _, parameter := range operation.Parameters {
			names = append(names, parameter.Name)
		}
		return names
	}

	parameter := func(operation rpc.OpenAPIOperation, name string) rpc.OpenAPIParameter {
		index := slices.IndexFunc(operation.Parameters, func(p rpc.OpenAPIParameter) bool { return p.Name == name })
		Expect(index).To(BeNumerically(">=", 0), "parameter %s", name)
		return operation.Parameters[index]
	}

	It("emits the list as the sessions surface's collection, with lookups on every filter", func() {
		list := spec.Paths["/api/v1/sessions"]["get"]
		Expect(list.OperationID).To(Equal("list-sessions"))
		Expect(*list.Clicky).To(Equal(rpc.ClickyOperationMeta{Command: "sessions", Surface: "sessions", Verb: "list", Scope: "collection"}))
		Expect(spec.Clicky.Surfaces).To(ContainElement(rpc.ClickySurface{Key: "sessions", Entity: "sessions", Title: "Sessions"}))

		Expect(parameterNames(list)).To(Equal([]string{
			"profile", "kind", "state", "principal", "restartOf", "label.target", "label.origin", "label.via",
			"label.runId", "label.plan", "label.step", "label.environment", "from", "to", "sort", "order", "limit", "offset",
		}))
		Expect(*parameter(list, "label.runId").Lookup).To(Equal(rpc.ClickyLookupMeta{
			Ref: "#/components/x-clicky-filters/sessions-label-runId", URL: "/api/v1/sessions",
			Filter: "label.runId", SearchParam: "__lookup_q", Multi: true,
		}))
		Expect(parameter(list, "restartOf").Lookup.Multi).To(BeFalse())
		Expect(parameter(list, "state").Schema.Enum).To(Equal([]any{"starting", "running", "stopping", "completed", "failed", "stopped", "interrupted"}))
		Expect(parameter(list, "from").Clicky.Role).To(Equal("time-from"))
		Expect(parameter(list, "sort").Schema.Enum).To(Equal([]any{"startedAt", "stoppedAt", "updatedAt", "state", "profile", "principal", "eventCount"}))
		Expect(parameter(list, "limit").Schema.Default).To(Equal(50))

		Expect(spec.Components.ClickyFilters["sessions-restartOf"]).To(And(
			HaveField("Label", "Restart of"), HaveField("Type", "value"), HaveField("Multi", false)))
		Expect(spec.Components.ClickyFilters["sessions-label-target"]).To(And(
			HaveField("Label", "target"), HaveField("Type", "multi-filter"), HaveField("Multi", true)))
	})

	It("emits the detail and the stop, extend and restart actions", func() {
		detail := spec.Paths["/api/v1/sessions/{id}"]["get"]
		Expect(detail.OperationID).To(Equal("get-session"))
		Expect(*detail.Clicky).To(Equal(rpc.ClickyOperationMeta{Command: "sessions", Surface: "sessions", Verb: "get", Scope: "entity", IDParam: "id"}))

		Expect(spec.Paths["/api/v1/sessions/{id}/stop"]["post"].OperationID).To(Equal("stop-session"))
		extend := spec.Paths["/api/v1/sessions/{id}/extend"]["post"]
		Expect(extend.OperationID).To(Equal("extend-session"))
		Expect(parameter(extend, "duration").Required).To(BeTrue())
		restart := spec.Paths["/api/v1/sessions/{id}/restart"]["post"]
		Expect(restart.OperationID).To(Equal("restart-session"))
		Expect(parameter(restart, "duration").Required).To(BeFalse())
		Expect(parameter(restart, "duration").Description).To(And(
			ContainSubstring("replays the ended session's params.durationMs"), ContainSubstring("not a run an extension lengthened")))
		Expect(restart.Responses).To(HaveKey("201"))
		Expect(restart.Responses).ToNot(HaveKey("200"))
	})
})
