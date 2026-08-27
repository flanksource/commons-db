package query

import (
	"fmt"
	"sort"
	"strings"

	"github.com/flanksource/clicky"
	"github.com/flanksource/clicky/api"
	"github.com/flanksource/clicky/api/icons"
	inspection "github.com/flanksource/commons-db/inspect"
)

type InspectionCardinality struct {
	Value    int64  `json:"value"`
	Relation string `json:"relation"`
	Cached   bool   `json:"cached"`
}

type InspectionFilterResolution struct {
	Label  string `json:"label"`
	Kind   string `json:"kind"`
	Origin string `json:"origin"`
	Field  string `json:"field,omitempty"`
	Lookup bool   `json:"lookup,omitempty"`
	Multi  bool   `json:"multi,omitempty"`
	Reason string `json:"reason"`
}

type InspectionField struct {
	ID           string                     `json:"id"`
	Name         string                     `json:"name"`
	Source       string                     `json:"source,omitempty"`
	DatabaseType string                     `json:"databaseType"`
	SemanticType string                     `json:"semanticType"`
	Cardinality  *InspectionCardinality     `json:"cardinality,omitempty"`
	Filter       InspectionFilterResolution `json:"filter"`
}

type InspectionLimit struct {
	Label  string `json:"label"`
	Value  int    `json:"value"`
	Origin string `json:"origin"`
}

type InspectionPaging struct {
	Selected    string            `json:"selected"`
	Supported   []string          `json:"supported"`
	Execution   string            `json:"execution"`
	Order       string            `json:"order"`
	Consistency string            `json:"consistency"`
	Note        string            `json:"note"`
	Limits      []InspectionLimit `json:"limits"`
}

type InspectionCacheSummary struct {
	Policy string `json:"policy"`
	State  string `json:"state"`
	Age    string `json:"age"`
	Cached bool   `json:"cached"`
}

type InspectionResult struct {
	Name       string                 `json:"name"`
	Provider   string                 `json:"provider"`
	Connection string                 `json:"connection"`
	ScopeLabel string                 `json:"scopeLabel"`
	Scope      string                 `json:"scope"`
	Query      string                 `json:"query"`
	Status     string                 `json:"status"`
	StatusNote string                 `json:"statusNote"`
	DurationMS float64                `json:"durationMs,omitempty"`
	Cache      InspectionCacheSummary `json:"cache"`
	Fields     []InspectionField      `json:"fields"`
	Paging     InspectionPaging       `json:"paging"`
}

func NewProfileInspectionResult(profile Profile, sample *SampleResult) *InspectionResult {
	if sample == nil {
		panic("profile inspection sample is required")
	}
	resultColumns := make(map[string]ResultColumn, len(sample.ResultColumns))
	for _, column := range sample.ResultColumns {
		resultColumns[column.Name] = column
	}
	declared := make(map[string]ColumnDef, len(profile.Columns))
	for _, column := range profile.Columns {
		declared[column.Name] = column
	}
	cached := inspectionCacheHit(sample.Inspection)
	fields := make([]InspectionField, 0, len(sample.Columns))
	for _, column := range sample.Columns {
		if column.Hidden {
			continue
		}
		field := InspectionField{
			ID: column.Name, Name: column.Name,
			DatabaseType: inspectionDatabaseType(column, resultColumns[column.Name]),
			SemanticType: inspectionSemanticType(column, resultColumns[column.Name]),
			Filter:       resolvedInspectionFilter(column, declared[column.Name], resultColumns[column.Name]),
		}
		field.Source = column.InspectedField()
		if field.Source == field.Name {
			field.Source = ""
		}
		if sample.Inspection != nil {
			if count, ok := sample.Inspection.Counts[column.InspectedField()]; ok {
				relation := "Exact"
				if profile.Provider.Type == "opensearch" && count > DefaultFilterLookupLimit {
					relation = "At least"
				}
				field.Cardinality = &InspectionCardinality{Value: count, Relation: relation, Cached: cached}
			}
		}
		fields = append(fields, field)
	}
	status, note := profileInspectionStatus(sample.Inspection)
	return &InspectionResult{
		Name: profile.Name, Provider: profile.Provider.Type,
		Connection: inspectionConnection(profile.Provider.Connection), ScopeLabel: "Profile", Scope: profile.Name,
		Query: sample.RenderedQuery, Status: status, StatusNote: note, DurationMS: sample.DurationMS,
		Cache: SummarizeInspectionCache(inspectionCaches(sample.Inspection)), Fields: fields,
		Paging: profileInspectionPaging(profile, sample),
	}
}

func InferInspectionSemanticType(databaseType string) string {
	value := strings.ToLower(databaseType)
	switch {
	case strings.Contains(value, "date"), strings.Contains(value, "time"):
		return "datetime"
	case strings.Contains(value, "int"), strings.Contains(value, "long"), strings.Contains(value, "float"),
		strings.Contains(value, "double"), strings.Contains(value, "decimal"), strings.Contains(value, "number"),
		strings.Contains(value, "numeric"):
		return "number"
	case strings.Contains(value, "bool"):
		return "boolean"
	case strings.Contains(value, "json"), strings.Contains(value, "object"), strings.Contains(value, "map"), strings.Contains(value, "nested"):
		return "json"
	default:
		return "string"
	}
}

func (InspectionField) Columns() []api.ColumnDef {
	return []api.ColumnDef{
		api.Column("field").Label("Field").MaxWidth(32).Build(),
		api.Column("datatype").Label("Datatype").MaxWidth(28).Build(),
		api.Column("cardinality").Label("Cardinality").Build(),
		api.Column("filter").Label("Resolved auto-filter").MaxWidth(52).Build(),
	}
}

func (field InspectionField) Row() map[string]any {
	name := api.Text{}.Append(field.Name, "font-mono font-medium")
	if field.Source != "" {
		name = name.NewLine().Append("← "+field.Source, "font-mono text-muted")
	}
	icon, color := inspectionTypeVisual(field.SemanticType)
	datatype := api.Text{}.Add(icon.WithStyle(color)).Space().Append(field.DatabaseType, "font-mono")
	if field.SemanticType != field.DatabaseType {
		datatype = datatype.NewLine().Append(field.SemanticType, "text-muted")
	}
	cardinality := api.Text{}.Append("Not probed", "text-muted")
	if field.Cardinality != nil {
		value := fmt.Sprintf("%d", field.Cardinality.Value)
		if field.Cardinality.Relation == "At least" {
			value = "≥" + value
		}
		source := "this run"
		if field.Cardinality.Cached {
			source = "cached"
		}
		cardinality = api.Text{}.Append(value, "font-mono font-medium").NewLine().
			Append(field.Cardinality.Relation+" · "+source, "text-muted")
	}
	filterIcon, filterColor := inspectionFilterVisual(field.Filter.Kind)
	filter := api.Text{}.Add(filterIcon.WithStyle(filterColor)).Space().Append(field.Filter.Label, filterColor).
		Space().Append("· "+field.Filter.Origin, inspectionOriginColor(field.Filter.Origin)).
		NewLine().Append(field.Filter.Reason, "text-muted")
	return map[string]any{"field": name, "datatype": datatype, "cardinality": cardinality, "filter": filter}
}

func (InspectionLimit) Columns() []api.ColumnDef {
	return []api.ColumnDef{
		api.Column("limit").Label("Limit").Build(),
		api.Column("value").Label("Value").Build(),
		api.Column("origin").Label("Origin").Build(),
	}
}

func (limit InspectionLimit) Row() map[string]any {
	return map[string]any{
		"limit":  api.Text{}.Add(icons.Config.WithStyle("text-amber-600")).Space().Append(limit.Label),
		"value":  api.Text{}.Append(fmt.Sprintf("%d", limit.Value), "font-mono font-medium"),
		"origin": api.Text{}.Append(limit.Origin, "text-muted"),
	}
}

func (result *InspectionResult) PrettyFull() api.Textable {
	if result == nil {
		panic("inspection result is required")
	}
	statusIcon := icons.Success.WithStyle("text-green-600")
	statusColor := "text-green-600"
	if result.Status != "Complete" {
		statusIcon = icons.Warning.WithStyle("text-yellow-600")
		statusColor = "text-yellow-600"
	}
	context := api.DescriptionList{Items: []api.KeyValuePair{
		api.KeyValue("Provider", result.Provider), api.KeyValue("Connection", result.Connection),
		api.KeyValue(result.ScopeLabel, result.Scope), api.KeyValue("Status", result.Status, statusColor),
	}}
	cache := api.DescriptionList{Items: []api.KeyValuePair{
		api.KeyValue("Policy", result.Cache.Policy), api.KeyValue("State", result.Cache.State),
		api.KeyValue("Age", result.Cache.Age), api.KeyValue("Cached", result.Cache.Cached),
	}}
	paging := api.DescriptionList{Items: []api.KeyValuePair{
		api.KeyValue("Selected mode", result.Paging.Selected),
		api.KeyValue("Provider supports", strings.Join(result.Paging.Supported, " · ")),
		api.KeyValue("Execution", result.Paging.Execution), api.KeyValue("Consistency", result.Paging.Consistency),
		api.KeyValue("Effective order", result.Paging.Order), api.KeyValue("Resolution", result.Paging.Note),
	}}
	document := api.TextList{
		clicky.Heading(2, api.Text{}.Add(statusIcon).Space().Append(result.Name, "font-bold")),
		context,
		api.Text{}.Append(result.StatusNote, "text-muted"),
		clicky.Heading(3, api.Text{}.Add(icons.Table.WithStyle("text-sky-600")).Space().Append("Fields and resolved filters")),
		api.NewTableFrom(result.Fields),
		clicky.Heading(3, api.Text{}.Add(icons.Config.WithStyle("text-violet-600")).Space().Append("Paging resolution")),
		paging,
		clicky.Heading(3, api.Text{}.Add(icons.Config.WithStyle("text-amber-600")).Space().Append("Resolved limits")),
		api.NewTableFrom(result.Paging.Limits),
		clicky.Heading(3, api.Text{}.Add(icons.Search.WithStyle("text-cyan-600")).Space().Append("Inspection metadata")),
		cache,
	}
	if result.Query != "" {
		document = append(document, clicky.Collapsed("Resolved query", api.CodeBlock("text", result.Query)))
	}
	return document
}

func (result *InspectionResult) Pretty() api.Text {
	return api.Text{}.Add(result.PrettyFull())
}

func inspectionDatabaseType(column ColumnDef, result ResultColumn) string {
	if result.DatabaseType != "" {
		return result.DatabaseType
	}
	if column.Type != "" {
		return string(column.Type)
	}
	return "unknown"
}

func inspectionSemanticType(column ColumnDef, result ResultColumn) string {
	if column.Type != "" {
		return string(column.Type)
	}
	return InferInspectionSemanticType(result.DatabaseType)
}

func resolvedInspectionFilter(column, declared ColumnDef, result ResultColumn) InspectionFilterResolution {
	if declared.Filter != nil && declared.Filter.Disabled {
		return InspectionFilterResolution{Label: "Disabled", Kind: "none", Origin: "Disabled", Reason: "Disabled by the profile"}
	}
	if result.Filter == nil || result.FilterKey == "" {
		return InspectionFilterResolution{Label: "Not filterable", Kind: "none", Origin: "Disabled", Reason: "No backend filter binding resolved"}
	}
	origin := "Inferred"
	if declared.Filter != nil {
		origin = "Profile override"
	}
	return InspectionFilterResolution{
		Label: inspectionTitle(result.Filter.Kind), Kind: result.Filter.Kind, Origin: origin,
		Field: column.InspectedField(), Lookup: result.Filter.Lookup, Multi: result.Filter.Multi,
		Reason: fmt.Sprintf("Resolved as %s on %s", result.Filter.Kind, result.FilterKey),
	}
}

func profileInspectionPaging(profile Profile, sample *SampleResult) InspectionPaging {
	order := make([]string, 0, len(sample.Resolution.Order))
	for _, by := range sample.Resolution.Order {
		part := by.Column
		if by.Desc {
			part += " desc"
		}
		if by.Unique {
			part += " unique"
		}
		order = append(order, part)
	}
	effectiveOrder := strings.Join(order, ", ")
	if effectiveOrder == "" {
		effectiveOrder = "No stable order"
	}
	execution := "Buffered"
	if sample.Resolution.NativePaging {
		execution = "Native"
	}
	note := sample.Resolution.PageableReason
	if sample.Resolution.Pageable {
		note = "This profile has a stable order and can serve positions beyond the first page."
	} else if note == "" {
		note = "This profile only serves its first page."
	}
	supported := strings.Split(sample.Resolution.PagingModes, ",")
	for index := range supported {
		supported[index] = inspectionTitle(supported[index])
	}
	return InspectionPaging{
		Selected: inspectionTitle(sample.Pagination.Mode), Supported: supported, Execution: execution,
		Order: effectiveOrder, Consistency: inspectionTitle(sample.Pagination.Consistency), Note: note,
		Limits: resolvedInspectionLimits(sample.Resolution.Limits, profile.Limits),
	}
}

func resolvedInspectionLimits(limits RowLimits, declared *RowLimits) []InspectionLimit {
	origin := func(value int) string {
		if value != 0 {
			return "Profile override"
		}
		return "Provider default"
	}
	if declared == nil {
		declared = &RowLimits{}
	}
	return []InspectionLimit{
		{Label: "Page size", Value: limits.PageSize, Origin: origin(declared.PageSize)},
		{Label: "Max page", Value: limits.MaxPageSize, Origin: origin(declared.MaxPageSize)},
		{Label: "Export cap", Value: limits.MaxExportRows, Origin: origin(declared.MaxExportRows)},
	}
}

func profileInspectionStatus(status *InspectionStatus) (string, string) {
	if status == nil {
		return "Complete", "The provider returned fields without optional cardinality inspection."
	}
	if status.Status == "failed" || status.Status == "partial" {
		return "Partial", status.Message
	}
	return "Complete", "Field types, cardinality probes, and backend filter bindings resolved successfully."
}

func inspectionConnection(connection string) string {
	if connection == "" {
		return "inline"
	}
	return connection
}

func inspectionCaches(status *InspectionStatus) []inspection.CacheMetadata {
	if status == nil {
		return nil
	}
	return status.Cache
}

func inspectionCacheHit(status *InspectionStatus) bool {
	if status == nil || len(status.Cache) == 0 {
		return false
	}
	for _, cache := range status.Cache {
		if !cache.Cached {
			return false
		}
	}
	return true
}

func SummarizeInspectionCache(cache []inspection.CacheMetadata) InspectionCacheSummary {
	if len(cache) == 0 {
		return InspectionCacheSummary{Policy: "Not used", State: "Unavailable", Age: "—"}
	}
	policies := make(map[string]struct{}, len(cache))
	state := "Fresh"
	cached, age := true, int64(0)
	for _, entry := range cache {
		policies[entry.Policy] = struct{}{}
		if entry.State == inspection.CacheStateStale {
			state = "Stale"
		}
		cached = cached && entry.Cached
		age = max(age, entry.AgeMS)
	}
	names := make([]string, 0, len(policies))
	for policy := range policies {
		names = append(names, policy)
	}
	sort.Strings(names)
	return InspectionCacheSummary{Policy: strings.Join(names, " · "), State: state, Age: inspectionAge(age), Cached: cached}
}

func inspectionAge(milliseconds int64) string {
	switch {
	case milliseconds < 1_000:
		return fmt.Sprintf("%dms", milliseconds)
	case milliseconds < 60_000:
		return fmt.Sprintf("%ds", (milliseconds+500)/1_000)
	case milliseconds < 3_600_000:
		return fmt.Sprintf("%dm", (milliseconds+30_000)/60_000)
	default:
		return fmt.Sprintf("%dh", (milliseconds+1_800_000)/3_600_000)
	}
}

func inspectionTitle(value string) string {
	if value == "" {
		return ""
	}
	return strings.ToUpper(value[:1]) + value[1:]
}

func inspectionTypeVisual(semanticType string) (icons.Icon, string) {
	switch semanticType {
	case "number":
		return icons.Number, "text-violet-600"
	case "boolean":
		return icons.Boolean, "text-emerald-600"
	case "datetime":
		return icons.Type, "text-sky-600"
	case "duration", "bytes":
		return icons.Performance, "text-amber-600"
	case "status", "health":
		return icons.Success, "text-teal-600"
	case "json", "key_value", "key_values":
		return icons.JSON, "text-indigo-600"
	default:
		return icons.Type, "text-slate-600"
	}
}

func inspectionFilterVisual(kind string) (icons.Icon, string) {
	switch kind {
	case "terms":
		return icons.Search, "text-green-600"
	case "range", "duration", "time", "date":
		return icons.Config, "text-amber-600"
	case "boolean":
		return icons.Boolean, "text-green-600"
	case "none":
		return icons.Cross, "text-slate-500"
	default:
		return icons.Type, "text-sky-600"
	}
}

func inspectionOriginColor(origin string) string {
	switch origin {
	case "Inferred":
		return "text-green-600"
	case "Profile override":
		return "text-sky-600"
	case "Unresolved":
		return "text-yellow-600"
	default:
		return "text-slate-500"
	}
}
