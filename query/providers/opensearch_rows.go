package providers

import (
	"github.com/flanksource/commons-db/logs/opensearch"
	"github.com/flanksource/commons-db/query"
)

func openSearchRows(result opensearch.Response) []query.Row {
	rows := make([]query.Row, 0, len(result.Hits.Hits))
	for _, hit := range result.Hits.Hits {
		row := make(query.Row, len(hit.Fields)+len(hit.Source)+3)
		for name, value := range hit.Fields {
			if values, ok := value.([]any); ok && len(values) == 1 {
				value = values[0]
			}
			row[name] = value
		}
		for name, value := range hit.Source {
			row[name] = value
		}
		row["source_index"], row["source_id"], row["_id"] = hit.Index, hit.ID, hit.ID
		rows = append(rows, row)
	}
	return rows
}
