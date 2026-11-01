package sqlite

import (
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/flanksource/sqlglot-go/sqlglot"
)

// partitionShape is how a query over partitions is executed.
type partitionShape int

const (
	// partitionRows returns rows: each partition's rows are merged in order.
	partitionRows partitionShape = iota
	// partitionDecomposable aggregates with count, sum, total, min, max and
	// avg only: each partition computes partial aggregates and the final
	// query combines them.
	partitionDecomposable
	// partitionHolistic aggregates with anything else (median, DISTINCT, a
	// window): each partition's filtered rows are gathered and the query runs
	// once over all of them.
	partitionHolistic
)

func (s partitionShape) String() string {
	return [...]string{"rows", "decomposable", "holistic"}[s]
}

const (
	partialsTable      = "__cdb_partials"
	partialGroupPrefix = "__cdb_g"
	partialAggPrefix   = "__cdb_a"
)

// partitionPlan is one partition's read and the query that combines them.
type partitionPlan struct {
	Shape partitionShape
	// Member is what this partition runs; its rows load into Table.
	Member sqlQuery
	// Table is the workspace table Member rows load into.
	Table string
	// Final runs once in the workspace, through the ordinary paged read.
	Final sqlQuery
}

// planPartitioned decides how source runs across partitions and, for an
// aggregate, rewrites it into the read one partition with labels runs and the
// query combining every partition's rows. aggregates names the functions SQLite
// computes over groups, which sqlglot cannot tell for a call it does not know.
func planPartitioned(source sqlQuery, labels map[string]string, aggregates map[string]bool) (partitionPlan, error) {
	root, err := parsePartitioned(source.Text)
	if err != nil {
		return partitionPlan{}, err
	}
	shape, err := classifyPartitioned(root, aggregates)
	if err != nil || shape == partitionRows {
		return partitionPlan{Shape: shape}, err
	}
	if shape == partitionHolistic {
		return planHolistic(source, root, labels)
	}
	return planDecomposable(source, labels, aggregates)
}

func parsePartitioned(statement string) (sqlNode, error) {
	root, err := sqlglot.ParseOne(statement, "sqlite")
	if err != nil {
		return nil, fmt.Errorf("the query cannot be read as sqlite SQL, which partitioned reads need to plan it: %w", err)
	}
	return root, nil
}

func classifyPartitioned(root sqlNode, aggregates map[string]bool) (partitionShape, error) {
	if root.Class != "Select" {
		for _, branch := range root.FindAll("Select") {
			if groups(branch, aggregates) {
				return 0, fmt.Errorf("an aggregate inside a %s cannot be combined across partitions; aggregate over the union instead", strings.ToUpper(root.Class))
			}
		}
		return partitionRows, nil
	}
	if !groups(root, aggregates) {
		return partitionRows, nil
	}
	if root.Args["with_"] != nil || len(root.FindAll("Select", "Subquery")) > 1 {
		return 0, fmt.Errorf("an aggregate over partitions cannot contain a CTE or subquery; each would run inside one partition and see only its rows")
	}
	if root.Args["distinct"] != nil {
		return partitionHolistic, nil
	}
	for _, aggregate := range aggregateNodes(root, aggregates) {
		if !decomposable(aggregate) {
			return partitionHolistic, nil
		}
	}
	return partitionDecomposable, nil
}

// groups reports whether a select's rows are groups rather than rows.
func groups(selection sqlNode, aggregates map[string]bool) bool {
	for _, clause := range []string{"group", "having", "distinct"} {
		if selection.Args[clause] != nil {
			return true
		}
	}
	return len(aggregateNodes(selection, aggregates)) > 0
}

// aggregateNodes are the outermost aggregate and window calls of a select's
// projections, HAVING and ORDER BY, in tree order, without descending into a
// subquery, whose rows are its own.
func aggregateNodes(selection sqlNode, aggregates map[string]bool) []sqlNode {
	var found []sqlNode
	for _, clause := range []string{"expressions", "having", "order"} {
		for _, top := range clauseNodes(selection, clause) {
			top.Walk(func(node sqlNode) bool {
				if node.Class == "Select" || node.Class == "Subquery" {
					return false
				}
				if isAggregateCall(node, aggregates) {
					found = append(found, node)
					return false
				}
				return true
			})
		}
	}
	return found
}

func clauseNodes(selection sqlNode, clause string) []sqlNode {
	switch value := selection.Args[clause].(type) {
	case []sqlNode:
		return value
	case sqlNode:
		return sqlNodes(value)
	}
	return nil
}

// isAggregateCall reads sqlglot's module for a call it knows and SQLite's own
// function list for one it parses as anonymous.
func isAggregateCall(node sqlNode, aggregates map[string]bool) bool {
	switch node.Class {
	case "Window", "AnonymousAggFunc":
		return true
	case "Anonymous":
		return aggregates[strings.ToLower(node.Name())]
	case "Min", "Max":
		// SQLite's min and max of two or more arguments are scalar.
		extra, _ := node.Args["expressions"].([]sqlNode)
		return len(extra) == 0
	}
	class, _ := node.Dump()[0]["c"].(string)
	return strings.HasPrefix(class, "sqlglot.expressions.aggregate.") || strings.HasSuffix(node.Class, "Agg")
}

// decomposable reports whether partial results of aggregate combine into the
// whole: a count or sum of sums, a min of mins, an avg of sums over counts.
func decomposable(aggregate sqlNode) bool {
	switch aggregate.Class {
	case "Count":
		this, _ := aggregate.Args["this"].(sqlNode)
		return this == nil || this.Class != "Distinct"
	case "Sum", "Avg", "Min", "Max":
		return true
	case "Anonymous":
		return strings.EqualFold(aggregate.Name(), "total")
	}
	return false
}

// planHolistic gathers each partition's filtered rows, labels included, into a
// table named as the query's own, so the query then runs over it unchanged.
func planHolistic(source sqlQuery, root sqlNode, labels map[string]string) (partitionPlan, error) {
	from, _ := root.Args["from_"].(sqlNode)
	var table sqlNode
	if from != nil {
		table, _ = from.Args["this"].(sqlNode)
	}
	if table == nil || table.Class != "Table" || root.Args["joins"] != nil || table.Args["db"] != nil {
		return partitionPlan{}, fmt.Errorf("an aggregate over partitions that is not count, sum, total, min, max or avg must read one unqualified table, so its rows can be gathered")
	}
	projections := sqlNodes(sqlglot.New("Star"))
	for _, column := range labelColumns(labels) {
		projections = append(projections, aliased(stringLiteral(labels[column]), identifier(column)))
	}
	root.Set("expressions", projections)
	for _, clause := range []string{"group", "having", "order", "limit", "offset", "distinct"} {
		root.Set(clause, nil)
	}
	if err := substituteLabels(root, labels); err != nil {
		return partitionPlan{}, err
	}
	member, err := renumberMarkers(root, source)
	if err != nil {
		return partitionPlan{}, err
	}
	return partitionPlan{Shape: partitionHolistic, Member: member, Table: table.Name(), Final: source}, nil
}

// planDecomposable splits the query: every partition groups by the query's own
// keys and computes each aggregate's partial columns, and the final query
// groups the gathered partials again and combines them.
func planDecomposable(source sqlQuery, labels map[string]string, aggregates map[string]bool) (partitionPlan, error) {
	partial, err := parsePartitioned(source.Text)
	if err != nil {
		return partitionPlan{}, err
	}
	final, err := parsePartitioned(source.Text)
	if err != nil {
		return partitionPlan{}, err
	}
	if err := requireProjectionNames(final, aggregates); err != nil {
		return partitionPlan{}, err
	}
	member, err := partialQuery(partial, source, labels, aggregates)
	if err != nil {
		return partitionPlan{}, err
	}
	combined, err := finalQuery(final, source, aggregates)
	if err != nil {
		return partitionPlan{}, err
	}
	return partitionPlan{Shape: partitionDecomposable, Member: member, Table: partialsTable, Final: combined}, nil
}

// requireProjectionNames refuses an unaliased computed projection. SQLite
// names it after its own text, which the final query spells differently, so
// the column would change name between one file and many.
func requireProjectionNames(selection sqlNode, aggregates map[string]bool) error {
	for index, projection := range clauseNodes(selection, "expressions") {
		if projection.Class == "Alias" || projection.Class == "Column" {
			continue
		}
		written, _ := sqlglot.Generate(projection, "sqlite")
		return fmt.Errorf("projection %d (%s) needs an alias: across partitions it is computed by a different expression", index+1, written)
	}
	return nil
}

func partialQuery(partial sqlNode, source sqlQuery, labels map[string]string, aggregates map[string]bool) (sqlQuery, error) {
	keys, err := groupKeys(partial)
	if err != nil {
		return sqlQuery{}, err
	}
	var projections []sqlNode
	for index, key := range keys {
		projections = append(projections, aliased(key.Copy(), identifier(partialGroupPrefix+strconv.Itoa(index))))
	}
	for index, aggregate := range aggregateNodes(partial, aggregates) {
		projections = append(projections, partialColumns(aggregate, partialAggPrefix+strconv.Itoa(index))...)
	}
	partial.Set("expressions", projections)
	partial.Set("group", nil)
	if len(keys) > 0 {
		partial.Set("group", sqlglot.New("Group", kv("expressions", copies(keys))))
	}
	for _, clause := range []string{"having", "order", "limit", "offset"} {
		partial.Set(clause, nil)
	}
	if err := substituteLabels(partial, labels); err != nil {
		return sqlQuery{}, err
	}
	return renumberMarkers(partial, source)
}

func partialColumns(aggregate sqlNode, name string) []sqlNode {
	if aggregate.Class != "Avg" {
		return sqlNodes(aliased(aggregate.Copy(), identifier(name)))
	}
	this := aggregate.Args["this"].(sqlNode)
	return sqlNodes(
		aliased(aggregateCall("sum", this.Copy()), identifier(name+"_sum")),
		aliased(aggregateCall("count", this.Copy()), identifier(name+"_count")),
	)
}

const aggregateArgument = "__cdb_argument"

// aggregateCall is function(argument), parsed from a template rather than
// assembled: an aggregate node carries args the generator reads (count's
// big_int, min's expressions) that only the parser fills in.
func aggregateCall(function string, argument sqlNode) sqlNode {
	template, err := sqlglot.ParseOne(fmt.Sprintf("SELECT %s(%s)", function, aggregateArgument), "sqlite")
	if err != nil {
		panic(fmt.Sprintf("the %s template does not parse: %v", function, err))
	}
	call := clauseNodes(template, "expressions")[0]
	placeholder := call.FindAll("Column")[0]
	if err := replaceNode(placeholder, argument); err != nil {
		panic(fmt.Sprintf("the %s template has no argument: %v", function, err))
	}
	call.Parent = nil
	return call
}

func finalQuery(final sqlNode, source sqlQuery, aggregates map[string]bool) (sqlQuery, error) {
	keys, err := groupKeys(final)
	if err != nil {
		return sqlQuery{}, err
	}
	for index, aggregate := range aggregateNodes(final, aggregates) {
		if err := replaceNode(aggregate, combined(aggregate, partialAggPrefix+strconv.Itoa(index))); err != nil {
			return sqlQuery{}, err
		}
	}
	if err := replaceGroupKeys(final, keys); err != nil {
		return sqlQuery{}, err
	}
	columns := make([]sqlNode, 0, len(keys))
	for index := range keys {
		columns = append(columns, columnNode(partialGroupPrefix+strconv.Itoa(index)))
	}
	final.Set("group", nil)
	if len(columns) > 0 {
		final.Set("group", sqlglot.New("Group", kv("expressions", columns)))
	}
	final.Set("from_", fromItem(sqlglot.New("Table", kv("this", identifier(partialsTable)))))
	final.Set("joins", nil)
	final.Set("where", nil)
	if err := requireCombinedColumns(final); err != nil {
		return sqlQuery{}, err
	}
	return renumberMarkers(final, source)
}

// combined is the expression folding one aggregate's partial columns.
func combined(aggregate sqlNode, name string) sqlNode {
	column := columnNode(name)
	switch aggregate.Class {
	case "Count", "Sum":
		return aggregateCall("sum", column)
	case "Min", "Max":
		return aggregateCall(strings.ToLower(aggregate.Class), column)
	case "Avg":
		// Parsed rather than assembled: the division and cast nodes carry
		// dialect flags the generator reads, which only the parser sets right.
		// The names are the planner's own, so the text is fixed.
		ratio, err := sqlglot.ParseOne(fmt.Sprintf(`SELECT (CAST(sum("%s_sum") AS REAL) / sum("%s_count"))`, name, name), "sqlite")
		if err != nil {
			panic(fmt.Sprintf("the avg combination does not parse: %v", err))
		}
		return clauseNodes(ratio, "expressions")[0]
	default: // total
		return aggregateCall("total", column)
	}
}

// groupKeys are the GROUP BY expressions with ordinals and projection aliases
// resolved to the expressions they name, since the partial query renames
// every projection.
func groupKeys(selection sqlNode) ([]sqlNode, error) {
	group, _ := selection.Args["group"].(sqlNode)
	if group == nil {
		return nil, nil
	}
	projections := clauseNodes(selection, "expressions")
	keys := clauseNodes(group, "expressions")
	resolved := make([]sqlNode, 0, len(keys))
	for _, key := range keys {
		if key.Class == "Literal" && key.Args["is_string"] == false {
			ordinal, err := strconv.Atoi(key.Name())
			if err != nil || ordinal < 1 || ordinal > len(projections) {
				return nil, fmt.Errorf("GROUP BY %s names no projection", key.Name())
			}
			resolved = append(resolved, unaliased(projections[ordinal-1]))
			continue
		}
		resolved = append(resolved, aliasTarget(key, projections))
	}
	return resolved, nil
}

func aliasTarget(key sqlNode, projections []sqlNode) sqlNode {
	if key.Class != "Column" || key.Args["table"] != nil {
		return key
	}
	for _, projection := range projections {
		if projection.Class == "Alias" && projection.Args["alias"].(sqlNode).Name() == key.Name() {
			return unaliased(projection)
		}
	}
	return key
}

func unaliased(projection sqlNode) sqlNode {
	if projection.Class == "Alias" {
		return projection.Args["this"].(sqlNode)
	}
	return projection
}

// replaceGroupKeys points every use of a group key in the projections, HAVING
// and ORDER BY at the partial column holding it. A bare column projection
// keeps its name.
func replaceGroupKeys(selection sqlNode, keys []sqlNode) error {
	type replacement struct{ old, new sqlNode }
	var pending []replacement
	for _, clause := range []string{"expressions", "having", "order"} {
		for _, top := range clauseNodes(selection, clause) {
			top.Walk(func(node sqlNode) bool {
				for index, key := range keys {
					if node.Equal(key) {
						column := columnNode(partialGroupPrefix + strconv.Itoa(index))
						if node == top && clause == "expressions" && node.Class == "Column" {
							column = aliased(column, identifier(node.Name()))
						}
						pending = append(pending, replacement{node, column})
						return false
					}
				}
				return true
			})
		}
	}
	for _, r := range pending {
		if err := replaceNode(r.old, r.new); err != nil {
			return err
		}
	}
	return nil
}

// requireCombinedColumns refuses a column the final query would read that is
// neither a group key nor an aggregate: each partition would have picked its
// own row's value, and the gathered partials hold no such column.
func requireCombinedColumns(selection sqlNode) error {
	aliases := map[string]bool{}
	for _, projection := range clauseNodes(selection, "expressions") {
		if projection.Class == "Alias" {
			aliases[projection.Args["alias"].(sqlNode).Name()] = true
		}
	}
	for _, clause := range []string{"expressions", "having", "order"} {
		for _, top := range clauseNodes(selection, clause) {
			for _, column := range top.FindAll("Column") {
				name := column.Name()
				if strings.HasPrefix(name, partialGroupPrefix) || strings.HasPrefix(name, partialAggPrefix) ||
					strings.HasPrefix(name, sqlParamMarkerPrefix) || (clause != "expressions" && aliases[name]) {
					continue
				}
				return fmt.Errorf("column %q is neither grouped nor aggregated, so it has no single value across partitions", name)
			}
		}
	}
	return nil
}

// substituteLabels writes each partition's label values into its read where
// the query names a label column, since the file itself has no such column.
func substituteLabels(tree sqlNode, labels map[string]string) error {
	for _, column := range tree.FindAll("Column") {
		value, isLabel := labels[column.Name()]
		if !isLabel || column.Args["table"] != nil {
			continue
		}
		if err := replaceNode(column, stringLiteral(value)); err != nil {
			return err
		}
	}
	return nil
}

func copies(nodes []sqlNode) []sqlNode {
	copied := make([]sqlNode, 0, len(nodes))
	for _, node := range nodes {
		copied = append(copied, node.Copy())
	}
	return copied
}

// renumberMarkers writes tree back as text whose param and identifier markers
// are numbered from zero in the order they now appear, with the values they
// stand for. A rewrite drops some markers (a HAVING a partition does not run)
// and copies others (avg's argument into a sum and a count), and parseBase
// requires each marker exactly once.
func renumberMarkers(tree sqlNode, source sqlQuery) (sqlQuery, error) {
	var out sqlQuery
	identifiers := map[int]int{}
	var err error
	tree.Walk(func(node sqlNode) bool {
		if err != nil {
			return false
		}
		switch {
		case node.Class == "Column" && node.Args["table"] == nil && strings.HasPrefix(node.Name(), sqlParamMarkerPrefix):
			var index int
			if index, err = markerIndex(node.Name(), sqlParamMarkerPrefix, len(source.Args)); err == nil {
				out.Args = append(out.Args, source.Args[index])
				node.Args["this"].(sqlNode).Set("this", sqlParamMarker(len(out.Args)-1))
			}
		case node.Class == "Identifier" && strings.HasPrefix(node.Name(), sqlIdentifierMarkerPrefix):
			var index int
			if index, err = markerIndex(node.Name(), sqlIdentifierMarkerPrefix, len(source.Identifiers)); err == nil {
				renumbered, seen := identifiers[index]
				if !seen {
					renumbered = len(out.Identifiers)
					identifiers[index] = renumbered
					out.Identifiers = append(out.Identifiers, source.Identifiers[index])
				}
				node.Set("this", sqlIdentifierMarker(renumbered))
			}
		}
		return true
	})
	if err != nil {
		return sqlQuery{}, err
	}
	if out.Text, err = sqlglot.Generate(tree, "sqlite"); err != nil {
		return sqlQuery{}, fmt.Errorf("write the partitioned statement: %w", err)
	}
	return out, nil
}

func markerIndex(name, prefix string, count int) (int, error) {
	index, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(name, prefix), "__"))
	if err != nil || index < 0 || index >= count {
		return 0, fmt.Errorf("SQL query marker %q names no supplied value", name)
	}
	return index, nil
}

// sqliteAggregates are the functions SQLite computes over groups, built-in or
// registered by this process, from SQLite's own function list.
var sqliteAggregates = sync.OnceValues(func() (map[string]bool, error) {
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return nil, err
	}
	defer func() { _ = database.Close() }()
	rows, err := database.Query(`SELECT DISTINCT name FROM pragma_function_list WHERE type IN ('a', 'w')`)
	if err != nil {
		return nil, fmt.Errorf("list sqlite aggregate functions: %w", err)
	}
	defer func() { _ = rows.Close() }()
	names := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names[strings.ToLower(name)] = true
	}
	return names, rows.Err()
})
