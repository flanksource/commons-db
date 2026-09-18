package recordresults

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/db/sqlitetable"
	"github.com/flanksource/commons-db/query"
)

// ResultView is a named SQL profile over one result type's streams. Its query
// reads the stream from the CTE stream_rows: the kind's table narrowed to
// {{.params.stream}} and the (afterSeq, toSeq] window. The registry, not the
// view author, owns that predicate, because the index holds every
// environment's streams and a view that forgot it would read them all.
type ResultView struct {
	// Name is a SQL identifier: the last segment of the view's profile,
	// <prefix>/<kind>/<name>, and the CTE a view using it reads.
	Name  string `yaml:"name"`
	Title string `yaml:"title"`

	// Uses names views of the same type compiled in first, AS MATERIALIZED.
	Uses []string `yaml:"uses,omitempty"`

	// Query is a SELECT, optionally WITH …, over stream_rows and the views it
	// uses. A view aggregates with GROUP BY: a correlated join or anti-join over
	// a stream of a hundred thousand rows runs for minutes.
	Query string `yaml:"query"`

	// Params are the view's own, in addition to stream, afterSeq and toSeq.
	Params []query.ParamDef `yaml:"params,omitempty"`

	Columns []query.ColumnDef `yaml:"columns"`

	// Order must end in a unique column, which is what pages it.
	Order query.Order `yaml:"order"`
}

// RegisteredResultView is one view of a registered result type.
type RegisteredResultView struct {
	Name    string `json:"name"`
	Title   string `json:"title"`
	Profile string `json:"profile"`
}

const (
	// streamRowsCTE is the stream window every view reads, and pagingCTE the
	// name the sqlite provider wraps a statement in to filter and page it.
	streamRowsCTE = "stream_rows"
	pagingCTE     = "__cdb_base"

	// probeStream is a stream id no stream can have — recordstore.ValidateStream
	// refuses "/" — so a registration probe reads an empty window.
	probeStream = "view-probe/none"
)

var viewNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// compiledView is one view as the registry serves it.
type compiledView struct {
	registered RegisteredResultView
	profile    query.Profile
}

// viewProfiles compiles views into profiles beside base, the type's own
// profile over table, and reads each once against the index, so a view that
// names a column the kind lacks fails when the index opens rather than on its
// first request.
func (r *Registry) viewProfiles(table sqlitetable.Table, base query.Profile, views []ResultView) ([]compiledView, error) {
	byName, err := indexViews(views, baseOnlyParams(base))
	if err != nil {
		return nil, err
	}
	if err := r.checkBareNames(views); err != nil {
		return nil, err
	}
	window, err := streamWindow(table)
	if err != nil {
		return nil, err
	}
	compiled := make([]compiledView, 0, len(views))
	for _, view := range views {
		uses, err := usedViews(view, byName)
		if err != nil {
			return nil, err
		}
		profile := compileView(base, window, view, uses)
		if err := validateProfile(profile); err != nil {
			return nil, fmt.Errorf("view %q: %w", view.Name, err)
		}
		if err := r.probeView(profile, view); err != nil {
			return nil, fmt.Errorf("view %q: %w", view.Name, err)
		}
		compiled = append(compiled, compiledView{
			registered: RegisteredResultView{Name: view.Name, Title: view.Title, Profile: profile.Name},
			profile:    profile,
		})
	}
	return compiled, nil
}

// indexViews refuses a view the registry could not name, and indexes the rest
// by name.
func indexViews(views []ResultView, baseOnly []string) (map[string]ResultView, error) {
	byName := make(map[string]ResultView, len(views))
	folded := make(map[string]bool, len(views))
	for _, view := range views {
		switch {
		case !viewNamePattern.MatchString(view.Name):
			return nil, fmt.Errorf("view name %q is not a SQL identifier of letters, digits and _", view.Name)
		case strings.EqualFold(view.Name, streamRowsCTE) || strings.EqualFold(view.Name, pagingCTE):
			return nil, fmt.Errorf("view name %q is reserved: every view statement declares it", view.Name)
		case folded[strings.ToLower(view.Name)]:
			return nil, fmt.Errorf("view name %q is declared twice; SQLite names ignore case", view.Name)
		case view.Title == "":
			return nil, fmt.Errorf("view %q needs a title", view.Name)
		case strings.TrimSpace(view.Query) == "":
			return nil, fmt.Errorf("view %q needs a query", view.Name)
		}
		// SQLite reads a quoted name that resolves to no column as a string, so
		// the paging wrapper's ORDER BY an undeclared column would order by a
		// constant rather than fail.
		for _, by := range view.Order {
			if !slices.ContainsFunc(view.Columns, func(column query.ColumnDef) bool { return column.Name == by.Column }) {
				return nil, fmt.Errorf("view %q orders by %q, which is not one of its declared columns", view.Name, by.Column)
			}
		}
		for _, param := range view.Params {
			if param.Type == query.ParamTypeIdentifier {
				return nil, fmt.Errorf("view %q param %q is an identifier, which names a column no view statement can vary", view.Name, param.Name)
			}
			if slices.Contains(baseOnly, param.Name) {
				return nil, fmt.Errorf("view %q param %q belongs to the base profile, whose rows it bounds", view.Name, param.Name)
			}
		}
		folded[strings.ToLower(view.Name)] = true
		byName[view.Name] = view
	}
	return byName, nil
}

// checkBareNames refuses a view name SQLite reads only when quoted, such as a
// keyword: a view using it names it bare.
func (r *Registry) checkBareNames(views []ResultView) error {
	if len(views) == 0 {
		return nil
	}
	client, err := sql.Open("sqlite", r.index.ReadDSN())
	if err != nil {
		return fmt.Errorf("open the index to check view names: %w", err)
	}
	var errs []error
	for _, view := range views {
		statement, err := client.PrepareContext(context.Background(),
			fmt.Sprintf("WITH %s AS (SELECT 1) SELECT 1 FROM %s", view.Name, view.Name))
		if err != nil {
			errs = append(errs, fmt.Errorf("view name %q must be a bare SQLite identifier: %w", view.Name, err))
			break
		}
		errs = append(errs, statement.Close())
	}
	return errors.Join(append(errs, client.Close())...)
}

// usedViews is every view view uses, directly or through another, each once
// and after the views it uses.
func usedViews(view ResultView, byName map[string]ResultView) ([]ResultView, error) {
	var ordered []ResultView
	placed := map[string]bool{}
	var visit func(name string, path []string) error
	visit = func(name string, path []string) error {
		if slices.Contains(path, name) {
			return fmt.Errorf("views use each other: %s", strings.Join(append(path, name), " -> "))
		}
		if placed[name] {
			return nil
		}
		used, ok := byName[name]
		if !ok {
			return fmt.Errorf("view %q uses %q, which is not a view of this result type", path[len(path)-1], name)
		}
		for _, next := range used.Uses {
			if err := visit(next, append(slices.Clone(path), name)); err != nil {
				return err
			}
		}
		placed[name] = true
		ordered = append(ordered, used)
		return nil
	}
	for _, name := range view.Uses {
		if err := visit(name, []string{view.Name}); err != nil {
			return nil, err
		}
	}
	return ordered, nil
}

// streamWindow reads one stream's (afterSeq, toSeq] rows from table under
// their declared names. Every edge binds as a placeholder, addressing the
// table's own columns.
func streamWindow(table sqlitetable.Table) (string, error) {
	stream, err := table.Physical(streamIDKey)
	if err != nil {
		return "", err
	}
	seq, err := table.Physical(seqColumn)
	if err != nil {
		return "", err
	}
	return table.Select() + fmt.Sprintf(` WHERE %s = {{.params.%s}} AND %s > {{.params.%s}} AND %s <= {{.params.%s}}`,
		stream, streamParam, seq, afterSeqParam, seq, toSeqParam), nil
}

// compileView is view's profile: the stream window, each used view
// materialized in order, then the view's own statement, its WITH list merged
// into the outer one.
func compileView(base query.Profile, window string, view ResultView, uses []ResultView) query.Profile {
	ctes := []string{streamRowsCTE + " AS (\n" + window + "\n)"}
	for _, used := range uses {
		ctes = append(ctes, used.Name+" AS MATERIALIZED (\n"+strings.TrimSpace(used.Query)+"\n)")
	}
	own, recursive, merges := leadingWith(view.Query)
	head := "WITH "
	if recursive {
		head = "WITH RECURSIVE "
	}
	statement := head + strings.Join(ctes, ",\n") + "\n" + strings.TrimSpace(view.Query)
	if merges {
		statement = head + strings.Join(ctes, ",\n") + ",\n" + own
	}
	return query.Profile{
		Name: base.Name + "/" + view.Name, Virtual: true, ReadOnly: true,
		Provider: query.ProviderConfig{Type: indexProviderType, Connection: base.Provider.Connection},
		Query:    statement,
		Params:   append(resultParams("", ""), view.Params...),
		Columns:  slices.Clone(view.Columns),
		Order:    slices.Clone(view.Order),
		Limits:   resultLimits(),
		Output:   resultOutputs(),
	}
}

// leadingWith reads past the whitespace and comments a statement opens with.
// When a WITH follows, it returns the CTE list and statement after the keyword
// and whether the list is RECURSIVE.
func leadingWith(statement string) (rest string, recursive, found bool) {
	rest, found = cutKeyword(statement, "with")
	if !found {
		return "", false, false
	}
	if after, isRecursive := cutKeyword(rest, "recursive"); isRecursive {
		return strings.TrimSpace(after), true, true
	}
	return strings.TrimSpace(rest), false, true
}

// cutKeyword skips leading whitespace and comments, and cuts keyword, ignoring
// case, when it is the next whole word.
func cutKeyword(statement, keyword string) (string, bool) {
	for {
		trimmed := strings.TrimLeft(statement, " \t\r\n")
		switch {
		case strings.HasPrefix(trimmed, "--"):
			end := strings.IndexByte(trimmed, '\n')
			if end < 0 {
				return statement, false
			}
			statement = trimmed[end+1:]
		case strings.HasPrefix(trimmed, "/*"):
			end := strings.Index(trimmed, "*/")
			if end < 0 {
				return statement, false
			}
			statement = trimmed[end+2:]
		case len(trimmed) > len(keyword) && strings.EqualFold(trimmed[:len(keyword)], keyword) &&
			!isIdentifierByte(trimmed[len(keyword)]):
			return trimmed[len(keyword):], true
		default:
			return statement, false
		}
	}
}

func isIdentifierByte(char byte) bool {
	return char == '_' || char == '$' || (char >= '0' && char <= '9') || (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z')
}

// validateProfile is every check a result profile passes at registration.
func validateProfile(profile query.Profile) error {
	if err := profile.Validate(); err != nil {
		return err
	}
	if _, err := profile.FilterBindings(); err != nil {
		return err
	}
	return profile.Pageable()
}

// probeView reads one page of profile for a stream that cannot exist, through
// the engine and provider a request reads through: its params bound as the
// sqlite provider binds them, wrapped for paging, and ordered by its order.
// SQLite prepares the whole statement before it returns a row, so a column
// the kind or a used view lacks fails here, while the empty window keeps the
// read itself free.
func (r *Registry) probeView(profile query.Profile, view ResultView) error {
	params := map[string]any{streamParam: probeStream}
	for _, param := range view.Params {
		if !param.Required || param.Default != nil {
			continue
		}
		value, err := probeValue(param)
		if err != nil {
			return err
		}
		params[param.Name] = value
	}
	ctx := dbcontext.New().WithConnectionResolver(r.ResolveConnection)
	for _, err := range query.ExecutePages(ctx, profile, query.PageRequest{Limit: 1, SkipTotal: true}, params) {
		if err != nil {
			return fmt.Errorf("read against the index: %w", err)
		}
		return nil
	}
	return errors.New("read against the index returned no page")
}

// probeValue is a value of param's type for a probe read to bind, where the
// view declares none.
func probeValue(param query.ParamDef) (any, error) {
	if len(param.Options) > 0 {
		return param.Options[0], nil
	}
	switch param.Type {
	case "", query.ParamTypeString, query.ParamTypeEnum, query.ParamTypeList, query.ParamTypeLabels:
		return "probe", nil
	case query.ParamTypeNumber:
		return int64(0), nil
	case query.ParamTypeBoolean:
		return false, nil
	case query.ParamTypeDate, query.ParamTypeDateTime:
		return "now", nil
	case query.ParamTypeDuration:
		return "0s", nil
	default:
		return nil, fmt.Errorf("param %q has type %q, which a view cannot take", param.Name, param.Type)
	}
}

// baseOnlyParams are the params base declares beyond the stream window, which
// a view shares: the time window, search and roots-only reads of the type's
// own rows.
func baseOnlyParams(base query.Profile) []string {
	var names []string
	for _, param := range base.Params {
		switch param.Name {
		case streamParam, afterSeqParam, toSeqParam:
		default:
			names = append(names, param.Name)
		}
	}
	return names
}
