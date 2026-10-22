package query

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/flanksource/commons-db/api"
	"github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/pkg/kube/labels"
	"github.com/flanksource/commons-db/query/grammar"
	"github.com/flanksource/commons-db/types"
	"github.com/flanksource/commons/collections"
	"github.com/flanksource/commons/duration"
	"github.com/patrickmn/go-cache"
	"github.com/samber/lo"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"k8s.io/apimachinery/pkg/selection"
)

var (
	// resourceSelectorCache caches the results of resource selector queries
	resourceSelectorCache = cache.New(time.Second*90, time.Minute*5)

	// immutableCache caches immutable resource selector results (no expiration)
	immutableCache = cache.New(cache.NoExpiration, time.Hour*12)
)

// FlushResourceSelectorCache flushes all resource selector caches
func FlushResourceSelectorCache() {
	resourceSelectorCache.Flush()
	immutableCache.Flush()
}

// SetResourceSelectorClause applies a ResourceSelector to a GORM query.
// The caller must provide a QueryModel that defines the table structure and capabilities.
//
// Returns the modified query and any error encountered.
func SetResourceSelectorClause(
	ctx context.Context,
	resourceSelector types.ResourceSelector,
	query *gorm.DB,
	queryModel QueryModel,
) (*gorm.DB, error) {
	if peg := resourceSelector.ToPeg(false); peg != "" {
		qf, err := grammar.ParsePEG(peg)
		if err != nil {
			return nil, fmt.Errorf("error parsing grammar[%s]: %w", peg, err)
		}

		var clauses []clause.Expression
		query, clauses, err = queryModel.Apply(ctx, *qf, query)
		if err != nil {
			return nil, fmt.Errorf("error applying query model: %w", err)
		}

		query = query.Clauses(clauses...)
	}

	if !resourceSelector.IncludeDeleted {
		query = query.Where("deleted_at IS NULL")
	}

	if len(resourceSelector.TagSelector) > 0 {
		if !queryModel.HasTags {
			return nil, api.Errorf(api.EINVALID, "tagSelector is not supported for table=%s", queryModel.Table)
		}

		parsedTagSelector, err := labels.Parse(resourceSelector.TagSelector)
		if err != nil {
			return nil, api.Errorf(api.EINVALID, "failed to parse tag selector: %v", err)
		}
		requirements, _ := parsedTagSelector.Requirements()
		for _, r := range requirements {
			query = jsonColumnRequirementsToSQLClause(query, "tags", r)
		}
	}

	if len(resourceSelector.LabelSelector) > 0 {
		if !queryModel.HasLabels {
			return nil, api.Errorf(api.EINVALID, "labelSelector is not supported for table=%s", queryModel.Table)
		}

		parsedLabelSelector, err := labels.Parse(resourceSelector.LabelSelector)
		if err != nil {
			return nil, api.Errorf(api.EINVALID, "failed to parse label selector: %v", err)
		}
		requirements, _ := parsedLabelSelector.Requirements()
		for _, r := range requirements {
			query = jsonColumnRequirementsToSQLClause(query, "labels", r)
		}
	}

	if len(resourceSelector.FieldSelector) > 0 {
		parsedFieldSelector, err := labels.Parse(resourceSelector.FieldSelector)
		if err != nil {
			return nil, api.Errorf(api.EINVALID, "failed to parse field selector: %v", err)
		}

		requirements, _ := parsedFieldSelector.Requirements()
		for _, r := range requirements {
			query = jsonColumnRequirementsToSQLClause(query, "properties", r)
		}
	}

	return query, nil
}

// queryResourceSelector runs the given resourceSelector and returns the resource results.
// It includes caching support based on the resource selector's cache directive.
func queryResourceSelector[T any](ctx context.Context, opts ResourceSelectorQuery, resourceSelector types.ResourceSelector) ([]T, error) {
	queryModel, selectColumns, limit, clauses := opts.Model, opts.Columns, opts.Limit, opts.Clauses
	if resourceSelector.IsEmpty() {
		return nil, nil
	}

	// must create a deep copy to avoid mutating the original order of the select columns
	var selectColumnsCopy = make([]string, len(selectColumns))
	copy(selectColumnsCopy, selectColumns)
	sort.Strings(selectColumnsCopy)

	var dummy T
	cacheKey := fmt.Sprintf("%s-%s-%s-%d-%T", strings.Join(selectColumnsCopy, ","), queryModel.Table, resourceSelector.Hash(), limit, dummy)

	cacheToUse := resourceSelectorCache
	if resourceSelector.Immutable() {
		cacheToUse = immutableCache
	}

	if resourceSelector.Cache != "no-cache" {
		if val, ok := cacheToUse.Get(cacheKey); ok {
			return val.([]T), nil
		}
	}

	query := ctx.DB().Select(selectColumns).Table(queryModel.Table)
	if len(clauses) > 0 {
		query = query.Clauses(clauses...)
	}

	// Resource selector's limit gets higher priority
	if resourceSelector.Limit > 0 {
		query = query.Limit(resourceSelector.Limit)
	} else if limit > 0 {
		query = query.Limit(limit)
	}

	query, err := SetResourceSelectorClause(ctx, resourceSelector, query, queryModel)
	if err != nil {
		return nil, err
	}

	var output []T
	if err := query.Find(&output).Error; err != nil {
		return nil, err
	}

	if resourceSelector.Cache != "no-store" {
		cacheDuration := cache.DefaultExpiration
		if len(output) == 0 {
			cacheDuration = time.Minute // if results weren't found, cache it shortly even on the immutable cache
		}

		if strings.HasPrefix(resourceSelector.Cache, "max-age=") {
			d, err := duration.ParseDuration(strings.TrimPrefix(resourceSelector.Cache, "max-age="))
			if err != nil {
				return nil, err
			}

			cacheDuration = time.Duration(d)
		}

		cacheToUse.Set(cacheKey, output, cacheDuration)
	}

	return output, nil
}

// jsonColumnRequirementsToSQLClause converts each selector requirement into a gorm SQL clause for a JSONB column
func jsonColumnRequirementsToSQLClause(q *gorm.DB, column string, r labels.Requirement) *gorm.DB {
	switch r.Operator() {
	case selection.Equals, selection.DoubleEquals:
		for val := range r.Values() {
			q = q.Where(fmt.Sprintf("%s @> ?", column), types.JSONStringMap{r.Key(): val})
		}
	case selection.NotEquals:
		for val := range r.Values() {
			q = q.Where(fmt.Sprintf("%s->>'%s' != ?", column, r.Key()), lo.Ternary[any](val == "nil", nil, val))
		}
	case selection.In:
		q = q.Where(fmt.Sprintf("%s->>'%s' IN ?", column, r.Key()), collections.MapKeys(r.Values()))
	case selection.NotIn:
		q = q.Where(fmt.Sprintf("%s->>'%s' NOT IN ?", column, r.Key()), collections.MapKeys(r.Values()))
	case selection.DoesNotExist:
		for val := range r.Values() {
			q = q.Where(fmt.Sprintf("%s->>'%s' IS NULL", column, val))
		}
	case selection.Exists:
		q = q.Where(fmt.Sprintf("%s ? ?", column), gorm.Expr("?"), r.Key())
	case selection.GreaterThan:
		for val := range r.Values() {
			q = q.Where(fmt.Sprintf("%s->>'%s' > ?", column, r.Key()), val)
		}
	case selection.LessThan:
		for val := range r.Values() {
			q = q.Where(fmt.Sprintf("%s->>'%s' < ?", column, r.Key()), val)
		}
	}

	return q
}

// ResourceSelectorQuery is the table, projection, row cap and extra clauses a
// resource selector query runs with. A selector's own limit wins over Limit.
type ResourceSelectorQuery struct {
	Model   QueryModel
	Columns []string
	Limit   int
	Clauses []clause.Expression
}

// QueryResourceSelectors queries a table using multiple resource selectors.
// It returns the combined results from all selectors, respecting the limit.
//
// Example usage:
//
//	model := QueryModel{
//	    Table: "my_resources",
//	    Columns: []string{"id", "name", "type"},
//	    HasTags: true,
//	}
//	results, err := QueryResourceSelectors[MyResource](ctx, ResourceSelectorQuery{
//	    Model: model, Columns: []string{"id", "name"}, Limit: 100,
//	}, selectors...)
func QueryResourceSelectors[T any](ctx context.Context, opts ResourceSelectorQuery, resourceSelectors ...types.ResourceSelector) ([]T, error) {
	var output []T

	for _, resourceSelector := range resourceSelectors {
		items, err := queryResourceSelector[T](ctx, opts, resourceSelector)
		if err != nil {
			return nil, err
		}

		output = append(output, items...)
		if opts.Limit > 0 && len(output) >= opts.Limit {
			return output[:opts.Limit], nil
		}
	}

	return output, nil
}
