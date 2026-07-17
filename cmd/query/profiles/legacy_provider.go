package profiles

import (
	"fmt"

	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
)

type legacyTraceProfileProvider struct{}

func (legacyTraceProfileProvider) Type() string { return legacyTraceProvider }

func (legacyTraceProfileProvider) Execute(_ dbcontext.Context, request query.ProviderRequest) ([]query.Row, error) {
	return nil, fmt.Errorf(
		"legacy trace profile kind %q is catalog-compatible but is not executable by the query engine",
		request.Options["kind"],
	)
}

func init() {
	query.RegisterProvider(legacyTraceProfileProvider{})
}
