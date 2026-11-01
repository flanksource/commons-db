package sqlite

import (
	"database/sql"
	"fmt"
	"iter"

	"github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/query"
)

// partitionPages serves a profile whose options name partitions: one read-only
// client per file. Rows merge in order; an aggregate gathers each partition's
// partial or filtered rows into a workspace and runs its final query there.
func (p sqlProvider) partitionPages(
	ctx context.Context,
	req query.ProviderRequest,
	page query.PageRequest,
	opts sqlOptions,
) iter.Seq2[query.Page, error] {
	if err := p.partitionedRead(req, opts); err != nil {
		return query.ErrorPage(err)
	}
	partitions, err := opts.Partitions.Resolve(req.Filters)
	if err != nil {
		return query.ErrorPage(err)
	}
	plans, err := planPartitions(req, partitions)
	if err != nil {
		return query.ErrorPage(err)
	}
	return func(yield func(query.Page, error) bool) {
		members, err := openPartitions(partitions)
		defer closePartitions(members)
		if err != nil {
			yield(query.Page{}, err)
			return
		}
		if err := requireSQLArrayFilters(ctx, members[0].Client, dialectSQLite, filtersUseArrays(req.Filters)); err != nil {
			yield(query.Page{}, err)
			return
		}
		if plans[0].Shape != partitionRows {
			aggregatePages(ctx, members, plans, req, page, opts.Partitions.Concurrency, yield)
			return
		}
		merged := MergeSQLPages(ctx, SQLMergeRequest{
			Driver: models.ConnectionTypeSQLite, Members: members,
			Read: sqlPageRequest(req, page), Concurrency: opts.Partitions.Concurrency,
		})
		for result, err := range merged {
			if !yield(result, err) {
				return
			}
		}
	}
}

// planPartitions plans the query for each partition's labels before any file
// is opened, so a query the planner refuses fails without reading one.
func planPartitions(req query.ProviderRequest, partitions []Partition) ([]partitionPlan, error) {
	aggregates, err := sqliteAggregates()
	if err != nil {
		return nil, err
	}
	source := sqlQuery{Text: req.Query, Args: req.QueryArgs, Identifiers: req.QueryIdentifiers}
	plans := make([]partitionPlan, 0, len(partitions))
	for _, partition := range partitions {
		plan, err := planPartitioned(source, partition.Labels, aggregates)
		if err != nil {
			return nil, err
		}
		plans = append(plans, plan)
	}
	return plans, nil
}

// aggregatePages gathers the partitions into a workspace and pages the final
// query over it, which, being one file, pages by offset or cursor alike.
func aggregatePages(
	ctx context.Context,
	members []SQLMember,
	plans []partitionPlan,
	req query.ProviderRequest,
	page query.PageRequest,
	concurrency int,
	yield func(query.Page, error) bool,
) {
	workspace, err := gatherPartitions(ctx, members, plans, concurrency)
	if err != nil {
		yield(query.Page{}, err)
		return
	}
	defer workspace.Close()
	final := req
	final.Query, final.QueryArgs, final.QueryIdentifiers = plans[0].Final.Text, plans[0].Final.Args, plans[0].Final.Identifiers
	readSQLPages(ctx, workspace.Client, dialectSQLite, final, page, yield)
}

func (p sqlProvider) partitionedRead(req query.ProviderRequest, opts sqlOptions) error {
	driver := p.connType
	if driver == "" {
		driver = opts.Type
	}
	switch {
	case sqlDialect(driver) != dialectSQLite:
		return fmt.Errorf("partitions are SQLite files; provider %q reads %q", p.key, driver)
	case req.Connection != "" || opts.URL != "":
		return fmt.Errorf("partitions name the files to read, so a connection or url cannot be set as well")
	case req.Query == "":
		return fmt.Errorf("sql query is required")
	}
	return nil
}

func openPartitions(partitions []Partition) ([]SQLMember, error) {
	members := make([]SQLMember, 0, len(partitions))
	for _, partition := range partitions {
		client, err := sql.Open("sqlite", partition.DSN)
		if err != nil {
			return members, fmt.Errorf("partition %q: %w", partition.Name, err)
		}
		members = append(members, SQLMember{Name: partition.Name, Client: client, Labels: partition.Labels})
	}
	return members, nil
}

func closePartitions(members []SQLMember) {
	for _, member := range members {
		_ = member.Client.Close()
	}
}
