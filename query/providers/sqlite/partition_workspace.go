package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"golang.org/x/sync/errgroup"
)

const workspaceBatchRows = 500

// partitionWorkspace is a scratch SQLite file every partition's rows are
// gathered into, so a query that needs all of them at once runs once.
type partitionWorkspace struct {
	Client *sql.DB
	dir    string

	// mu serializes writes: the file has one writer, and the table is created
	// by whichever partition answers first.
	mu      sync.Mutex
	table   string
	columns []string
}

// gatherPartitions runs each member's planned read and loads its rows into
// one table of a new workspace. Close removes the file.
func gatherPartitions(ctx context.Context, members []SQLMember, plans []partitionPlan, concurrency int) (*partitionWorkspace, error) {
	if err := os.MkdirAll(".tmp", 0o700); err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp(".tmp", "partition-sql-")
	if err != nil {
		return nil, err
	}
	client, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "workspace.sqlite"))
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	client.SetMaxOpenConns(1)
	workspace := &partitionWorkspace{Client: client, dir: dir, table: plans[0].Table}

	group, groupCtx := errgroup.WithContext(ctx)
	if concurrency <= 0 {
		concurrency = defaultMergeConcurrency
	}
	group.SetLimit(concurrency)
	for index, member := range members {
		group.Go(func() error {
			if err := workspace.gather(groupCtx, member, plans[index]); err != nil {
				return fmt.Errorf("partition %q: %w", member.Name, err)
			}
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		workspace.Close()
		return nil, err
	}
	return workspace, nil
}

func (w *partitionWorkspace) Close() {
	_ = w.Client.Close()
	_ = os.RemoveAll(w.dir)
}

func (w *partitionWorkspace) gather(ctx context.Context, member SQLMember, plan partitionPlan) error {
	statement, err := newSQLStatement(dialectSQLite)
	if err != nil {
		return err
	}
	if statement.tree, err = statement.parseBase(plan.Member); err != nil {
		return err
	}
	text, args, err := statement.render()
	if err != nil {
		return err
	}
	rows, err := member.Client.QueryContext(ctx, text, driverArgs(dialectSQLite, args)...)
	if err != nil {
		return fmt.Errorf("failed to execute sql query: %w", err)
	}
	defer func() { _ = rows.Close() }()
	columns, err := rows.Columns()
	if err != nil {
		return err
	}
	if err := w.ensureTable(ctx, columns); err != nil {
		return err
	}
	batch := make([][]any, 0, workspaceBatchRows)
	for rows.Next() {
		values := make([]any, len(columns))
		pointers := make([]any, len(columns))
		for index := range values {
			pointers[index] = &values[index]
		}
		if err := rows.Scan(pointers...); err != nil {
			return fmt.Errorf("failed to scan row: %w", err)
		}
		if batch = append(batch, values); len(batch) == workspaceBatchRows {
			if err := w.insert(ctx, batch); err != nil {
				return err
			}
			batch = batch[:0]
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("row iteration error: %w", err)
	}
	return w.insert(ctx, batch)
}

// ensureTable creates the table from the first partition's columns and holds
// every later partition to the same ones. Columns are untyped so each value
// keeps the storage class it was read with.
func (w *partitionWorkspace) ensureTable(ctx context.Context, columns []string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.columns != nil {
		if !slices.Equal(w.columns, columns) {
			return fmt.Errorf("returns columns %v, another partition returned %v; partitions must share a schema", columns, w.columns)
		}
		return nil
	}
	if duplicate := slices.Compact(slices.Sorted(slices.Values(columns))); len(duplicate) != len(columns) {
		return fmt.Errorf("returns columns %v, which name one column twice; a label may repeat a stored column", columns)
	}
	quoted := make([]string, 0, len(columns))
	for _, column := range columns {
		quoted = append(quoted, quote(column))
	}
	if _, err := w.Client.ExecContext(ctx, fmt.Sprintf("CREATE TABLE %s (%s)", quote(w.table), strings.Join(quoted, ", "))); err != nil {
		return fmt.Errorf("create workspace table %q: %w", w.table, err)
	}
	w.columns = columns
	return nil
}

func (w *partitionWorkspace) insert(ctx context.Context, batch [][]any) error {
	if len(batch) == 0 {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	tx, err := w.Client.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?, ", len(w.columns)), ", ")
	insert, err := tx.PrepareContext(ctx, fmt.Sprintf("INSERT INTO %s VALUES (%s)", quote(w.table), placeholders))
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	for _, values := range batch {
		if _, err := insert.ExecContext(ctx, values...); err != nil {
			_ = insert.Close()
			_ = tx.Rollback()
			return fmt.Errorf("load workspace table %q: %w", w.table, err)
		}
	}
	if err := insert.Close(); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}
