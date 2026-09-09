package report

import (
	"time"

	"github.com/flanksource/commons-db/query"
)

// EntryFile is the TSX component the embedded template renders with.
const EntryFile = "QueryReport.tsx"

// Column is one facet DynamicTable column. Type drives the cell renderer, and
// must be one of facet's own names.
type Column struct {
	Name string `json:"name"`
	Type string `json:"type"`
	Unit string `json:"unit,omitempty"`
}

// Payload is what the TSX template is called with. It mirrors ReportData in
// QueryReport.tsx.
type Payload struct {
	// Name is what facet writes the file as when the output path is a
	// directory, so it doubles as the report's slug.
	Name        string      `json:"name"`
	Title       string      `json:"title"`
	Subtitle    string      `json:"subtitle,omitempty"`
	GeneratedAt string      `json:"generatedAt"`
	RowCount    int         `json:"rowCount"`
	Truncated   bool        `json:"truncated,omitempty"`
	Columns     []Column    `json:"columns"`
	Rows        []query.Row `json:"rows"`
}

// facetColumnTypes maps the engine's column types onto facet's. Anything
// unrecognised renders as a string, which is always legible even when it is not
// the richest possible cell — the types are two independent vocabularies and
// only overlap in part.
var facetColumnTypes = map[query.ColumnType]string{
	query.ColumnTypeString:    "string",
	query.ColumnTypeNumber:    "number",
	query.ColumnTypeBoolean:   "boolean",
	query.ColumnTypeDateTime:  "datetime",
	query.ColumnTypeDuration:  "duration",
	query.ColumnTypeBytes:     "bytes",
	query.ColumnTypeStatus:    "status",
	query.ColumnTypeHealth:    "health",
	query.ColumnTypeKeyValue:  "labels",
	query.ColumnTypeKeyValues: "labels",
}

// NewPayload projects a result and its columns onto the template's props.
func NewPayload(title string, result *query.Result, columns []query.ColumnDef) Payload {
	payload := Payload{
		Name:        "report",
		Title:       title,
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Columns:     make([]Column, 0, len(columns)),
	}
	if result != nil {
		payload.Rows = result.Rows
		payload.RowCount = len(result.Rows)
		payload.Truncated = result.Truncated
		if payload.Title == "" {
			payload.Title = result.Profile
		}
	}
	for _, column := range columns {
		if column.Hidden {
			continue
		}
		payload.Columns = append(payload.Columns, Column{
			Name: column.Name,
			Type: facetColumnType(column.Type),
		})
	}
	return payload
}

func facetColumnType(columnType query.ColumnType) string {
	if mapped, ok := facetColumnTypes[columnType]; ok {
		return mapped
	}
	return "string"
}
