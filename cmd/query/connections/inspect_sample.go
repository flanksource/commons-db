package connections

import (
	"fmt"
	"strings"

	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/query"
)

type connectionInspectionSampleRequest struct {
	Connection *models.Connection
	Descriptor browserDescriptor
	Options    InspectFlags
	Target     ConnectionInspectionTarget
	Inspected  browserInspection
}

func connectionInspectionSampleProfile(request connectionInspectionSampleRequest) (query.Profile, error) {
	if request.Connection == nil {
		return query.Profile{}, fmt.Errorf("connection inspection sample requires a connection")
	}
	fields, err := connectionInspectionFields(request.Target, request.Inspected)
	if err != nil {
		return query.Profile{}, err
	}
	columns := make([]query.ColumnDef, 0, len(fields))
	for _, field := range fields {
		columns = append(columns, query.ColumnDef{Name: field.Name, Type: connectionInspectionColumnType(field)})
	}
	providerOptions := map[string]any{}
	statement := request.Descriptor.DefaultQuery
	switch request.Inspected.Kind {
	case "sql":
		statement = "SELECT * FROM " + sqlIdentifier(request.Connection.Type, request.Target.Schema, request.Target.Target)
		if request.Options.Database != "" {
			providerOptions["database"] = request.Options.Database
		}
	case "opensearch":
		providerOptions["index"] = request.Target.Target
	default:
		return query.Profile{}, fmt.Errorf("inspection kind %q cannot be sampled", request.Inspected.Kind)
	}
	profile := browserProfile(request.Descriptor, request.Connection, statement, providerOptions, columns)
	profile.Name = request.Connection.Name + " · " + request.Target.Name
	return profile, nil
}

func connectionInspectionColumnType(field query.InspectionField) query.ColumnType {
	if strings.Contains(strings.ToLower(field.DatabaseType), "uuid") {
		return query.ColumnTypeUUID
	}
	switch field.SemanticType {
	case "number":
		return query.ColumnTypeNumber
	case "boolean":
		return query.ColumnTypeBoolean
	case "datetime":
		return query.ColumnTypeDateTime
	case "json":
		return query.ColumnTypeJSON
	default:
		return query.ColumnTypeString
	}
}

func mergeConnectionInspectionResult(base, sampled *query.InspectionResult) *query.InspectionResult {
	if base == nil || sampled == nil {
		panic("connection inspection merge requires catalog and sampled results")
	}
	result := *base
	result.Query = sampled.Query
	result.Status = sampled.Status
	result.StatusNote = sampled.StatusNote
	result.DurationMS = sampled.DurationMS
	result.Cache = sampled.Cache
	result.Paging = sampled.Paging
	fields := make(map[string]query.InspectionField, len(sampled.Fields))
	for _, field := range sampled.Fields {
		fields[field.ID] = field
	}
	result.Fields = append([]query.InspectionField(nil), base.Fields...)
	for index := range result.Fields {
		if field, ok := fields[result.Fields[index].ID]; ok {
			result.Fields[index].Cardinality = field.Cardinality
			result.Fields[index].Filter = field.Filter
		}
	}
	return &result
}
