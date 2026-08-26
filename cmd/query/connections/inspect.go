package connections

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/flanksource/clicky"
	"github.com/flanksource/clicky/api"
	"github.com/flanksource/clicky/api/icons"
	inspection "github.com/flanksource/commons-db/inspect"
	sqlinspect "github.com/flanksource/commons-db/inspect/sql"
	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/query"
)

type InspectFlags struct {
	Database   string `flag:"database" help:"Database to inspect"`
	Schema     string `flag:"schema" hidden:"true" help:"SQL schema containing the target"`
	Target     string `flag:"target" help:"Table, view, index, alias, data stream, or pattern to inspect"`
	TargetKind string `flag:"target-kind" hidden:"true" help:"OpenSearch target kind"`
	Sample     bool   `flag:"sample" help:"Sample the selected target to resolve cardinality and auto-filters"`
	Refresh    bool   `flag:"refresh" help:"Refresh cached inspection metadata"`

	service      *Service
	connectionID string
	context      context.Context
	inspected    *browserInspection
}

func (InspectFlags) ClickyActionFlags() {}

type ConnectionInspection struct {
	Targets []ConnectionInspectionTarget `json:"targets,omitempty"`
	Result  *query.InspectionResult      `json:"result,omitempty"`
}

type ConnectionInspectionTarget struct {
	Name       string          `json:"name"`
	Target     string          `json:"target"`
	Kind       string          `json:"kind"`
	Schema     string          `json:"schema,omitempty"`
	FieldCount int             `json:"fieldCount,omitempty"`
	Inspect    api.LinkCommand `json:"inspect"`
}

func connectionInspectionTargets(connectionID string, options InspectFlags, inspected browserInspection) []ConnectionInspectionTarget {
	var targets []ConnectionInspectionTarget
	switch inspected.Kind {
	case "sql":
		for _, schema := range inspected.Schemas {
			for _, relation := range schema.Relations {
				flags := map[string]string{"schema": schema.Name, "target": relation.Name}
				if options.Database != "" {
					flags["database"] = options.Database
				}
				if options.Refresh {
					flags["refresh"] = strconv.FormatBool(options.Refresh)
				}
				if options.Sample {
					flags["sample"] = strconv.FormatBool(options.Sample)
				}
				name := schema.Name + "." + relation.Name
				targets = append(targets, ConnectionInspectionTarget{
					Name: name, Target: relation.Name, Kind: relation.Type, Schema: schema.Name,
					FieldCount: len(relation.Columns), Inspect: inspectionTargetCommand(connectionID, name, flags),
				})
			}
		}
	case "opensearch":
		for _, target := range inspected.Targets {
			flags := map[string]string{"target": target.Name, "target-kind": target.Kind}
			if options.Refresh {
				flags["refresh"] = strconv.FormatBool(options.Refresh)
			}
			if options.Sample {
				flags["sample"] = strconv.FormatBool(options.Sample)
			}
			label := target.Name
			if target.Pattern != "" && target.Pattern != target.Name {
				label += " · " + target.Pattern
			}
			targets = append(targets, ConnectionInspectionTarget{
				Name: label, Target: target.Name, Kind: target.Kind,
				Inspect: inspectionTargetCommand(connectionID, label, flags),
			})
		}
	}
	sort.SliceStable(targets, func(i, j int) bool { return targets[i].Name < targets[j].Name })
	return targets
}

func connectionInspectionResult(
	connection *models.Connection,
	descriptor browserDescriptor,
	target ConnectionInspectionTarget,
	inspected browserInspection,
) (*query.InspectionResult, error) {
	if connection == nil {
		return nil, fmt.Errorf("connection inspection requires a connection")
	}
	if descriptor.RowLimits == nil {
		return nil, fmt.Errorf("connection type %q has no browser row limits", connection.Type)
	}
	fields, err := connectionInspectionFields(target, inspected)
	if err != nil {
		return nil, err
	}
	status, note := connectionInspectionStatus(inspected)
	queryText := descriptor.DefaultQuery
	if queryText == "" {
		queryText = "Catalog metadata only"
	}
	scopeLabel := "Target"
	if inspected.Kind == "sql" {
		scopeLabel = inspectionTitle(target.Kind)
	}
	cache := []inspection.CacheMetadata{}
	if inspected.Cache != nil {
		cache = append(cache, *inspected.Cache)
	}
	return &query.InspectionResult{
		Name: connection.Name + " · " + target.Name, Provider: connection.Type, Connection: connection.Name,
		ScopeLabel: scopeLabel, Scope: target.Name, Query: queryText, Status: status, StatusNote: note,
		Cache: query.SummarizeInspectionCache(cache), Fields: fields,
		Paging: query.InspectionPaging{
			Selected: "Offset", Supported: []string{"Offset"}, Execution: "Browser", Order: "Query-defined", Consistency: "Live",
			Note: "The connection browser exposes bounded offset pages. Stable profile paging resolves from a profile order.",
			Limits: []query.InspectionLimit{
				{Label: "Page size", Value: descriptor.RowLimits.PageSize, Origin: "Provider default"},
				{Label: "Max page", Value: descriptor.RowLimits.MaxPageSize, Origin: "Provider default"},
				{Label: "Export cap", Value: descriptor.RowLimits.MaxExportRows, Origin: "Provider default"},
			},
		},
	}, nil
}

func (ConnectionInspectionTarget) Columns() []api.ColumnDef {
	return []api.ColumnDef{
		api.Column("target").Label("Target").MaxWidth(48).Build(),
		api.Column("kind").Label("Kind").Build(),
		api.Column("fields").Label("Fields").Build(),
	}
}

func (target ConnectionInspectionTarget) Row() map[string]any {
	fields := any(api.Text{}.Append("Resolve on inspect", "text-muted"))
	if target.FieldCount > 0 {
		fields = api.Text{}.Append(strconv.Itoa(target.FieldCount), "font-mono font-medium")
	}
	return map[string]any{
		"target": target.Inspect,
		"kind":   api.Text{}.Add(icons.Target.WithStyle("text-violet-600")).Space().Append(target.Kind),
		"fields": fields,
	}
}

func (result *ConnectionInspection) PrettyFull() api.Textable {
	if result == nil {
		panic("connection inspection is required")
	}
	if result.Result != nil {
		return result.Result.PrettyFull()
	}
	return api.TextList{
		clicky.Heading(2, api.Text{}.Add(icons.Database.WithStyle("text-sky-600")).Space().Append("Inspection targets")),
		api.Text{}.Append("Choose a table, view, index, alias, data stream, or pattern to inspect its fields.", "text-muted"),
		api.NewTableFrom(result.Targets),
	}
}

func (result *ConnectionInspection) Pretty() api.Text {
	return api.Text{}.Add(result.PrettyFull())
}

func (s *Service) Inspect(ctx context.Context, id string, options InspectFlags) (*ConnectionInspection, error) {
	database, err := s.database()
	if err != nil {
		return nil, err
	}
	connection, err := s.findConnection(database, id)
	if err != nil {
		return nil, err
	}
	requestContext, cancel := inspectionContext(ctx, connection.Type, 15*time.Second)
	defer cancel()
	var inspected browserInspection
	if options.inspected != nil {
		inspected = *options.inspected
	} else {
		inspected, err = s.browser.inspectConnection(
			requestContext, connection, options.Database, options.Target, options.TargetKind, options.Refresh,
		)
		if err != nil {
			return nil, fmt.Errorf("inspect connection %q: %s", connection.Name, sanitizeConnectionError(err, connection))
		}
	}
	if options.Target != "" && inspected.Kind == "opensearch" && inspected.Selected == nil {
		inspected, err = s.browser.inspectConnection(
			requestContext, connection, options.Database, options.Target, options.TargetKind, options.Refresh,
		)
		if err != nil {
			return nil, fmt.Errorf("inspect connection %q: %s", connection.Name, sanitizeConnectionError(err, connection))
		}
	}
	targets := connectionInspectionTargets(id, options, inspected)
	if options.Target == "" {
		return &ConnectionInspection{Targets: targets}, nil
	}
	target, err := selectedConnectionInspectionTarget(options, inspected, targets)
	if err != nil {
		return nil, err
	}
	descriptor, ok := descriptorForConnection(connection.Type)
	if !ok {
		return nil, fmt.Errorf("connection type %q has no browser descriptor", connection.Type)
	}
	result, err := connectionInspectionResult(connection, descriptor, target, inspected)
	if err != nil {
		return nil, err
	}
	if options.Sample {
		profile, err := connectionInspectionSampleProfile(connectionInspectionSampleRequest{
			Connection: connection, Descriptor: descriptor, Options: options, Target: target, Inspected: inspected,
		})
		if err != nil {
			return nil, err
		}
		sample, err := query.Sample(s.context().Wrap(requestContext), profile, query.SampleOptions{
			InspectionColumns: profile.Columns,
			Inspection:        query.InspectionOptions{Refresh: options.Refresh},
		})
		if err != nil {
			return nil, fmt.Errorf("sample connection %q target %q: %s", connection.Name, target.Name, sanitizeConnectionError(err, connection))
		}
		result = mergeConnectionInspectionResult(result, query.NewProfileInspectionResult(profile, sample))
	}
	return &ConnectionInspection{Result: result}, nil
}

func inspectionTargetCommand(connectionID, label string, flags map[string]string) api.LinkCommand {
	return api.NewLinkCommand("connection/inspect").WithArgs(connectionID).WithFlags(flags).
		WithTarget(api.LinkTargetDialog).WithAutoRun(true).Text(label, "font-mono font-medium text-sky-600")
}

func selectedConnectionInspectionTarget(
	options InspectFlags,
	inspected browserInspection,
	targets []ConnectionInspectionTarget,
) (ConnectionInspectionTarget, error) {
	for _, target := range targets {
		if inspected.Kind == "sql" && target.Schema == options.Schema && target.Target == options.Target {
			return target, nil
		}
		if inspected.Kind == "opensearch" && target.Target == options.Target && target.Kind == options.TargetKind {
			return target, nil
		}
	}
	if inspected.Kind == "opensearch" && strings.Contains(options.Target, "*") {
		return ConnectionInspectionTarget{Name: options.Target, Target: options.Target, Kind: "pattern"}, nil
	}
	return ConnectionInspectionTarget{}, fmt.Errorf("inspection target %q was not discovered", options.Target)
}

func connectionInspectionFields(target ConnectionInspectionTarget, inspected browserInspection) ([]query.InspectionField, error) {
	var fields []query.InspectionField
	switch inspected.Kind {
	case "sql":
		columns, err := selectedSQLInspectionColumns(target, inspected.Schemas)
		if err != nil {
			return nil, err
		}
		for _, column := range columns {
			fields = append(fields, unresolvedConnectionField(column.Name, column.DataType, "A profile query is required to resolve a filter"))
		}
	case "opensearch":
		if inspected.Selected == nil {
			return nil, fmt.Errorf("OpenSearch target %q has no field catalog", target.Target)
		}
		for _, field := range inspected.Selected.Fields {
			dataType := strings.Join(field.Types, " · ")
			if dataType == "" {
				dataType = "unknown"
			}
			capabilities := []string{}
			if field.Searchable {
				capabilities = append(capabilities, "searchable")
			}
			if field.Aggregatable {
				capabilities = append(capabilities, "aggregatable")
			}
			reason := "A query context is required to resolve a filter"
			if len(capabilities) > 0 {
				reason = "Catalog marks this field " + strings.Join(capabilities, " and ") + "; a profile query is required to resolve a filter"
			}
			fields = append(fields, unresolvedConnectionField(field.Name, dataType, reason))
		}
	default:
		return nil, fmt.Errorf("inspection kind %q has no field projection", inspected.Kind)
	}
	return fields, nil
}

func selectedSQLInspectionColumns(target ConnectionInspectionTarget, schemas []sqlinspect.Schema) ([]sqlinspect.Column, error) {
	for _, schema := range schemas {
		if schema.Name != target.Schema {
			continue
		}
		for _, relation := range schema.Relations {
			if relation.Name == target.Target {
				return relation.Columns, nil
			}
		}
	}
	return nil, fmt.Errorf("SQL target %q was not discovered", target.Name)
}

func unresolvedConnectionField(name, dataType, reason string) query.InspectionField {
	if dataType == "" {
		dataType = "unknown"
	}
	return query.InspectionField{
		ID: name, Name: name, DatabaseType: dataType, SemanticType: query.InferInspectionSemanticType(dataType),
		Filter: query.InspectionFilterResolution{
			Label: "Profile required", Kind: "none", Origin: "Unresolved", Reason: reason,
		},
	}
}

func connectionInspectionStatus(inspected browserInspection) (string, string) {
	reason := inspected.TruncateReason
	partial := inspected.Truncated
	if inspected.Selected != nil && inspected.Selected.Truncated {
		partial = true
		if inspected.Selected.TruncateReason != "" {
			reason = inspected.Selected.TruncateReason
		}
	}
	if inspected.Cache != nil && inspected.Cache.LastRefreshError != "" {
		partial = true
		reason = inspected.Cache.LastRefreshError
	}
	if partial {
		return "Partial", reason
	}
	return "Complete", "Catalog metadata is complete. Cardinality and auto-filters remain unresolved until this connection is used by a profile query."
}

func inspectionTitle(value string) string {
	if value == "" {
		return "Target"
	}
	return strings.ToUpper(value[:1]) + value[1:]
}
