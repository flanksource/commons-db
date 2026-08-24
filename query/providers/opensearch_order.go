package providers

import (
	"strings"

	"github.com/flanksource/commons-db/query"
)

// openSearchTiebreaker is the sort field that makes a derived order total.
//
// _id is unique per document and, being a document value, means the same thing
// in every search — so a position cut from one page resumes correctly in the
// next request without pinning anything. _shard_doc is OpenSearch's own
// tiebreaker and was the obvious candidate, but it is numbered inside a
// point-in-time: using it would oblige every page of every profile to open one,
// and a point-in-time over a dated wildcard pins a search context per shard —
// 55 of them for a fifty-day span, against a cluster default of 300. Paging
// would exhaust the cluster after a handful of views.
//
// It is only ever appended, never made the leading key. Sorting by _id alone
// forces a global fielddata sort that does not complete on a wide wildcard;
// behind a selective leading column it costs a fraction of a second.
const openSearchTiebreaker = "_id"

// NaturalOrder gives an unordered profile the order OpenSearch can page it by.
//
// The sort it is built on is the author's wherever there is one: the compiled
// search drops search.sort as soon as a profile order exists, so an order that
// ignored it would silently reorder a profile whose sort was deliberate. With
// no sort, the time field is the meaningful default for an index that declares
// one — newest first is what a span or log profile is read in. Either way the
// tiebreaker is appended, because a prefix of equal values is exactly where an
// untotalled order repeats or skips rows across a page boundary.
//
// A raw-DSL profile gets nothing. Its sort lives in hand-written JSON this
// package does not parse, and guessing an order for it would fight whatever the
// query already asked for.
func (opensearchProvider) NaturalOrder(config query.ProviderConfig) (query.Order, error) {
	opts, err := query.DecodeOptions[opensearchOptions](config.Options)
	if err != nil {
		return nil, err
	}
	if opts.Search == nil {
		return nil, nil
	}

	var order query.Order
	switch {
	case len(opts.Search.Sort) > 0:
		for _, by := range opts.Search.Sort {
			field := strings.TrimSpace(by.Field)
			// A sort the tiebreaker already covers, or one with no field to
			// name, would make the derived order invalid rather than total.
			if field == "" || field == openSearchTiebreaker {
				continue
			}
			order = append(order, query.OrderBy{
				Column: field,
				Desc:   strings.EqualFold(strings.TrimSpace(by.Order), "desc"),
			})
		}
	case strings.TrimSpace(opts.Search.TimeField) != "":
		order = append(order, query.OrderBy{Column: strings.TrimSpace(opts.Search.TimeField), Desc: true})
	default:
		// Neither a sort nor a time field means nothing here knows what order
		// this profile is meant to be read in. Paging by document position
		// alone would page correctly and present rows in an arbitrary sequence,
		// which is a worse answer than saying there is no order.
		return nil, nil
	}

	if len(order) == 0 {
		return nil, nil
	}
	return append(order, query.OrderBy{Column: openSearchTiebreaker, Unique: true}), nil
}
