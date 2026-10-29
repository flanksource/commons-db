package metrics

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/flanksource/sqlglot-go/sqlglot"
	_ "modernc.org/sqlite"
)

type Source struct {
	Name, Transform, Window string
	Filters                 map[string]string
	BoundFilters            map[string]int
}
type Sample struct {
	Time                               time.Time
	Value                              float64
	Labels                             map[string]string
	Metric, Namespace, Statistic, Unit string
}
type Request struct {
	SQL    string
	Args   []any
	Labels map[string]string
}
type Fetch func(Source, func(Sample) error) error

func Execute(ctx context.Context, req Request, fetch Fetch) ([]map[string]any, error) {
	plan, err := Plan(req.SQL)
	if err != nil {
		return nil, err
	}
	labels, err := ValidateLabels(req.Labels)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(".tmp", 0700); err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp(".tmp", "metric-sql-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	db, err := sql.Open("sqlite", filepath.Join(dir, "samples.sqlite"))
	if err != nil {
		return nil, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err := InitializeWorkspace(ctx, db); err != nil {
		return nil, err
	}
	for i, source := range plan.Sources {
		for label, index := range source.BoundFilters {
			if index < 0 || index >= len(req.Args) {
				return nil, fmt.Errorf("missing metric SQL parameter %d", index)
			}
			if value, ok := req.Args[index].(string); ok {
				source.Filters[label] = value
			}
		}
		if err := load(ctx, db, source, fmt.Sprintf("__metric_%d", i), labels, req.Labels, fetch); err != nil {
			return nil, fmt.Errorf("metric %s: %w", source.Name, err)
		}
	}
	bound := make([]any, len(req.Args))
	for i, v := range req.Args {
		bound[i] = sql.Named(fmt.Sprintf("metric_param_%d", i), v)
	}
	rows, err := db.QueryContext(ctx, plan.SQL, bound...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	result := []map[string]any{}
	for rows.Next() {
		values := make([]any, len(columns))
		ptrs := make([]any, len(columns))
		for i := range values {
			ptrs[i] = &values[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		row := map[string]any{}
		for i, c := range columns {
			row[c] = values[i]
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func ValidateLabels(labels map[string]string) ([]string, error) {
	seen := map[string]bool{}
	for _, n := range []string{"time", "value", "series_id", "metric_name", "metric_namespace", "statistic", "unit"} {
		seen[n] = true
	}
	names := []string{}
	for name, source := range labels {
		if name == "" || source == "" || seen[strings.ToLower(name)] {
			return nil, fmt.Errorf("invalid or reserved metric label alias %q", name)
		}
		seen[strings.ToLower(name)] = true
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}
func quote(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }

func load(ctx context.Context, db *sql.DB, source Source, table string, labels []string, mapping map[string]string, fetch Fetch) error {
	defs := []string{`time TEXT`, `value REAL`, `series_id TEXT`, `metric_name TEXT`, `metric_namespace TEXT`, `statistic TEXT`, `unit TEXT`}
	for _, label := range labels {
		defs = append(defs, quote(label)+" TEXT")
	}
	if _, err := db.ExecContext(ctx, "CREATE TABLE "+quote(table)+" ("+strings.Join(defs, ",")+", PRIMARY KEY(series_id,time))"); err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, "INSERT INTO "+quote(table)+" VALUES ("+strings.TrimSuffix(strings.Repeat("?,", len(defs)), ",")+") ON CONFLICT(series_id,time) DO NOTHING")
	if err != nil {
		return err
	}
	defer stmt.Close()
	err = fetch(source, func(s Sample) error {
		if math.IsNaN(s.Value) || math.IsInf(s.Value, 0) {
			return fmt.Errorf("non-finite metric sample at %s", s.Time)
		}
		identity, err := json.Marshal(s.Labels)
		if err != nil {
			return err
		}
		id := fmt.Sprintf("%x", sha256.Sum256(append([]byte(source.Name+"\x00"+s.Namespace+"\x00"+s.Statistic+"\x00"+s.Unit+"\x00"), identity...)))
		values := []any{formatTime(s.Time), s.Value, id, source.Name, s.Namespace, s.Statistic, s.Unit}
		for _, label := range labels {
			var value any
			if v, ok := s.Labels[mapping[label]]; ok {
				value = v
			}
			values = append(values, value)
		}
		result, err := stmt.ExecContext(ctx, values...)
		if err != nil {
			return err
		}
		inserted, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if inserted == 0 {
			var previous float64
			if err := tx.QueryRowContext(ctx, "SELECT value FROM "+quote(table)+" WHERE series_id=? AND time=?", id, values[0]).Scan(&previous); err != nil {
				return err
			}
			if previous != s.Value {
				return fmt.Errorf("conflicting samples for series %s at %s", id, s.Time)
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	return tx.Commit()
}

type QueryPlan struct {
	SQL     string
	Sources []Source
}
type node = *sqlglot.Expression

func arg(k string, v any) sqlglot.Arg { return sqlglot.Arg{Key: k, Value: v} }
func ident(s string) node             { return sqlglot.New("Identifier", arg("this", s), arg("quoted", true)) }

func Plan(statement string) (QueryPlan, error) {
	tree, err := sqlglot.ParseOne(statement, "sqlite")
	if err != nil {
		return QueryPlan{}, err
	}
	if tree.Class != "Select" && tree.Class != "Union" {
		return QueryPlan{}, fmt.Errorf("metric SQL requires SELECT")
	}
	for _, n := range tree.FindAll("Identifier") {
		if n.Name() == "__counter_value" || strings.HasPrefix(n.Name(), "__metric_") {
			return QueryPlan{}, fmt.Errorf("reserved metric SQL identifier %q", n.Name())
		}
	}
	for _, c := range tree.FindAll("Column") {
		if strings.HasPrefix(c.Name(), "__cdb_query_param_") {
			index, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(c.Name(), "__cdb_query_param_"), "__"))
			if err != nil {
				return QueryPlan{}, err
			}
			if err := replace(c, sqlglot.New("Placeholder", arg("this", fmt.Sprintf("metric_param_%d", index)))); err != nil {
				return QueryPlan{}, err
			}
		}
	}
	for _, n := range tree.FindAll("Identifier") {
		if strings.HasPrefix(n.Name(), "__cdb_query_identifier_") {
			return QueryPlan{}, fmt.Errorf("metric SQL identifier parameters are unsupported; declare metric names in the profile")
		}
	}
	for _, fn := range tree.FindAll("Median") {
		value, _ := fn.Args["this"].(node)
		if value == nil {
			return QueryPlan{}, fmt.Errorf("median requires value")
		}
		if err := replace(fn, sqlglot.New("Anonymous", arg("this", "median"), arg("expressions", []node{value.Copy()}))); err != nil {
			return QueryPlan{}, err
		}
	}
	ctes := map[string]bool{}
	for _, cte := range tree.FindAll("CTE") {
		alias, _ := cte.Args["alias"].(node)
		if alias == nil {
			return QueryPlan{}, fmt.Errorf("CTE requires alias")
		}
		ctes[strings.ToLower(alias.Name())] = true
	}
	plan := QueryPlan{}
	for _, table := range tree.FindAll("Table") {
		if value, ok := table.Args["this"].(node); ok && value.Class == "Anonymous" {
			if strings.EqualFold(value.Name(), "time_series") {
				continue
			}
			return plan, fmt.Errorf("unsupported metric table function %q", value.Name())
		}
		if ctes[strings.ToLower(table.Name())] {
			continue
		}
		if table.Args["db"] != nil || table.Args["catalog"] != nil {
			return plan, fmt.Errorf("metric names cannot be database qualified")
		}
		source := Source{Name: table.Name()}
		scope := selectOf(table)
		alias := table.Name()
		if a, ok := table.Args["alias"].(node); ok {
			alias = a.Name()
		}
		source.Filters, source.BoundFilters = sourceFilters(scope, alias)
		for _, fn := range scope.FindAll("Anonymous") {
			name := strings.ToLower(fn.Name())
			if (name != "rate" && name != "increase") || selectOf(fn) != scope {
				continue
			}
			args, _ := fn.Args["expressions"].([]node)
			if len(args) != 2 || args[0].Class != "Column" || args[0].Name() != "value" || args[1].Class != "Literal" || args[1].Args["is_string"] != true {
				return plan, fmt.Errorf("%s requires a source value and literal counter window", name)
			}
			qualifier, _ := args[0].Args["table"].(node)
			if qualifier != nil && qualifier.Name() != alias {
				continue
			}
			if qualifier == nil && len(scope.FindAll("Table")) != 1 {
				return plan, fmt.Errorf("qualify rate value when multiple sources are present")
			}
			if source.Transform != "" && (source.Transform != name || source.Window != args[1].Name()) {
				return plan, fmt.Errorf("one counter transform/window is allowed per table alias")
			}
			source.Transform = name
			source.Window = args[1].Name()
			args[0].Set("this", ident("__counter_value"))
			if err := replace(fn, args[0].Copy()); err != nil {
				return plan, err
			}
		}
		if source.Transform != "" {
			for _, star := range scope.FindAll("Star") {
				if selectOf(star) == scope && star.Parent != nil && star.Parent.Class != "Count" {
					return plan, fmt.Errorf("cannot mix wildcard raw values with counter transforms")
				}
			}
			for _, c := range scope.FindAll("Column") {
				if selectOf(c) != scope {
					continue
				}
				q, _ := c.Args["table"].(node)
				if q != nil && q.Name() != alias {
					continue
				}
				if c.Name() == "value" || c.Class == "Star" {
					return plan, fmt.Errorf("cannot mix raw values and counter transforms on one table alias")
				}
				if c.Name() == "__counter_value" {
					c.Set("this", ident("value"))
				}
			}
		}
		physical := fmt.Sprintf("__metric_%d", len(plan.Sources))
		plan.Sources = append(plan.Sources, source)
		table.Set("this", ident(physical))
		if table.Args["alias"] == nil {
			table.Set("alias", sqlglot.New("TableAlias", arg("this", ident(alias))))
		}
	}
	if len(plan.Sources) == 0 {
		return plan, fmt.Errorf("metric SQL requires a metric table")
	}
	for _, fn := range tree.FindAll("Anonymous") {
		if strings.EqualFold(fn.Name(), "rate") || strings.EqualFold(fn.Name(), "increase") {
			return plan, fmt.Errorf("counter expression must reference a metric source directly")
		}
	}
	plan.SQL, err = sqlglot.Generate(tree, "sqlite")
	return plan, err
}
func selectOf(n node) node {
	for n != nil && n.Class != "Select" {
		n = n.Parent
	}
	return n
}
func replace(old, new node) error {
	p := old.Parent
	if p == nil {
		return fmt.Errorf("cannot replace root")
	}
	for _, k := range p.Keys {
		switch v := p.Args[k].(type) {
		case node:
			if v == old {
				p.Set(k, new)
				return nil
			}
		case []node:
			for i, c := range v {
				if c == old {
					v[i] = new
					p.Set(k, v)
					return nil
				}
			}
		}
	}
	return fmt.Errorf("missing expression parent")
}
