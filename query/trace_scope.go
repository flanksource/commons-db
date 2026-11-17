package query

// TraceScopeKey narrows a traces profile to the spans of one trace. It is a
// request scope rather than a filter control: a trace id is what a listing row
// opens, never a value someone picks from the filter bar, so it stays out of
// FilterBindings while every other filter.* key still applies beside it.
const TraceScopeKey = ColumnFilterPrefix + "trace_id"

// traceScopeBinding resolves the trace id the way any column filter on the
// canonical trace_id column would be, so a canonical search and a raw span
// mapping each reach the field their own layout names.
func (p Profile) traceScopeBinding() (ColumnFilterBinding, bool, error) {
	if p.RenderMode() != RenderTraces {
		return ColumnFilterBinding{}, false, nil
	}
	column := ColumnDef{
		Name: "trace_id", Label: "Trace ID", Type: ColumnTypeString,
		Filter: &ColumnFilterDef{Kind: ColumnFilterKindExact},
	}
	return resolveColumnFilterBinding(p, column)
}
