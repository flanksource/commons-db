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
})
