package providers

import (
	"fmt"
	"strconv"

	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/query"
)

type OpenSearchPagingOptions struct {
	PagingMode      string `json:"pagingMode,omitempty"`
	PagingThreshold *int   `json:"pagingThreshold,omitempty"`
}

type openSearchPagingPolicy struct {
	mode      openSearchPagingMode
	threshold int
}

func resolveOpenSearchPagingPolicy(conn *models.Connection, options OpenSearchPagingOptions) (openSearchPagingPolicy, error) {
	mode, err := resolveOpenSearchPagingMode(conn)
	if err != nil {
		return openSearchPagingPolicy{}, err
	}
	threshold := 10000
	if conn != nil && conn.Properties[models.OpenSearchPropertyPagingThreshold] != "" {
		threshold, err = strconv.Atoi(conn.Properties[models.OpenSearchPropertyPagingThreshold])
		if err != nil {
			return openSearchPagingPolicy{}, fmt.Errorf("invalid paging_threshold: %w", err)
		}
	}
	if options.PagingMode != "" {
		mode, err = resolveOpenSearchPagingMode(&models.Connection{Properties: map[string]string{models.OpenSearchPropertyPagingMode: options.PagingMode}})
		if err != nil {
			return openSearchPagingPolicy{}, err
		}
	}
	if options.PagingThreshold != nil {
		threshold = *options.PagingThreshold
	}
	if threshold < 0 || threshold > openSearchResultWindow {
		return openSearchPagingPolicy{}, fmt.Errorf("paging threshold must be between 0 and %d, got %d", openSearchResultWindow, threshold)
	}
	return openSearchPagingPolicy{mode: mode, threshold: threshold}, nil
}

func (p openSearchPagingPolicy) usesCursor(page query.PageRequest) bool {
	if page.ReadAll {
		return page.Ceiling == 0 || page.Ceiling > p.threshold
	}
	return page.Limit > p.threshold
}

func boundedOpenSearchYield(remaining int, yield func(query.Page, error) bool) func(query.Page, error) bool {
	return func(page query.Page, err error) bool {
		if err != nil {
			return yield(page, err)
		}
		if len(page.Rows) > remaining {
			page.Rows = page.Rows[:remaining]
			page.NextKeys = nil
			page.NextOffset = 0
			page.Truncated = true
		}
		remaining -= len(page.Rows)
		return yield(page, nil) && remaining > 0
	}
}
