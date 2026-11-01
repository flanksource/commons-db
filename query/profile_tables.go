package query

import (
	stdctx "context"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query/metrics"
)

// ProfileResolver resolves the profiles metric SQL references as
// `profiles.<name>`. The profile store lives above this package, so whoever
// owns it binds a resolver to the execution context.
//
// Prepare, when set, runs before the referenced profile is read, under the
// caller's context — it is the profile service's BeforeExecute hook, so a
// reference cannot read data its caller could not read directly. Its release
// is held until the read finishes.
type ProfileResolver struct {
	Resolve func(ctx stdctx.Context, name string) (Profile, error)
	Prepare func(ctx stdctx.Context, p Profile, params map[string]any) (release func(), err error)
}

type profileResolverKey struct{}
type profileStackKey struct{}

// WithProfileResolver binds resolver for every `profiles.<name>` reference
// executed under ctx.
func WithProfileResolver(ctx context.Context, resolver ProfileResolver) context.Context {
	return ctx.WithValue(profileResolverKey{}, resolver)
}

func withProfileFrame(ctx context.Context, name string) context.Context {
	stack, _ := ctx.Value(profileStackKey{}).([]string)
	return ctx.WithValue(profileStackKey{}, append(slices.Clip(stack), name))
}

// ProfileTables executes `profiles.<name>` references for a metric SQL
// statement. Each child receives the parent's resolved params it declares
// (the rest take the child's defaults); a reference back onto the current
// call stack fails with the cycle's path.
func ProfileTables(ctx context.Context, parentParams map[string]any) metrics.ProfileRows {
	return func(name string) (metrics.ProfileTable, error) {
		stack, _ := ctx.Value(profileStackKey{}).([]string)
		if index := slices.Index(stack, name); index >= 0 {
			return metrics.ProfileTable{}, fmt.Errorf("profile reference cycle: %s", strings.Join(append(slices.Clone(stack[index:]), name), " -> "))
		}
		resolver, ok := ctx.Value(profileResolverKey{}).(ProfileResolver)
		if !ok || resolver.Resolve == nil {
			return metrics.ProfileTable{}, fmt.Errorf("no profile resolver is bound to this execution")
		}
		child, err := resolver.Resolve(ctx, name)
		if err != nil {
			return metrics.ProfileTable{}, err
		}
		params := map[string]any{}
		for _, def := range child.Params {
			if value, ok := parentParams[def.Name]; ok {
				params[def.Name] = value
			}
		}
		if resolver.Prepare != nil {
			release, err := resolver.Prepare(ctx, child, params)
			if err != nil {
				return metrics.ProfileTable{}, err
			}
			defer release()
		}
		// Execute pushes the child onto the call stack itself.
		result, err := Execute(ctx, child, params)
		if err != nil {
			return metrics.ProfileTable{}, err
		}
		return metrics.ProfileTable{Columns: profileTableColumns(child, result.Rows), Rows: result.Rows}, nil
	}
}

// profileTableColumns are the child's declared columns, or every key its rows
// carry when it declares none.
func profileTableColumns(p Profile, rows []Row) []string {
	if len(p.Columns) > 0 {
		columns := make([]string, len(p.Columns))
		for i, c := range p.Columns {
			columns[i] = c.Name
		}
		return columns
	}
	seen := map[string]bool{}
	for _, row := range rows {
		for key := range row {
			seen[key] = true
		}
	}
	columns := make([]string, 0, len(seen))
	for key := range seen {
		columns = append(columns, key)
	}
	sort.Strings(columns)
	return columns
}
