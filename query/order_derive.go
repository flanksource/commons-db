package query

// OrderingProvider is implemented by a provider that can name a total order for
// a profile which declares none.
//
// Paging of either kind needs a total order, and a profile without one dead-ends
// at its first page — the surface describing it cannot offer a position, so the
// caller is left with a row count and nowhere to step. For most providers that
// is the only honest answer: nothing reachable from a SQL profile says which of
// its columns is unique. A document store is different. OpenSearch indexes every
// hit with a position that is unique by construction, so the order a profile
// declined to declare is one the provider already knows, and requiring it to be
// typed out is a toll rather than a question.
//
// The order returned is a paging device, not a claim about what the author
// meant. It is used to cut and resume pages; it is deliberately not used where
// an order carries meaning the author must own — the reconcile merge join reads
// the declared order and only that.
type OrderingProvider interface {
	// NaturalOrder returns the order this provider would page by, given the
	// options the profile configured it with.
	NaturalOrder(config ProviderConfig) (Order, error)
}

// NaturalOrder returns the order the configured provider can page by, or nil
// when it has none to offer.
//
// A nil order is an answer rather than a failure: the profile stays un-pageable
// and Pageable() keeps reporting why, which is the same outcome every provider
// had before any of them could answer this. An error means the provider knows
// an order but could not read the options to build it, which is a broken
// profile and is reported as one.
func NaturalOrder(config ProviderConfig) (Order, error) {
	provider, err := GetProvider(config.Type)
	if err != nil {
		return nil, nil
	}
	ordering, ok := provider.(OrderingProvider)
	if !ok {
		return nil, nil
	}
	return ordering.NaturalOrder(config)
}
