package providerkit

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"

	"github.com/flanksource/commons-db/query"
)

// FieldBatches splits fields into consecutive batches of at most size.
func FieldBatches(fields []string, size int) [][]string {
	if size <= 0 {
		panic("inspection field batch size must be positive")
	}
	count := len(fields) / size
	if len(fields)%size != 0 {
		count++
	}
	batches := make([][]string, 0, count)
	for start := 0; start < len(fields); start += size {
		batches = append(batches, fields[start:min(start+size, len(fields))])
	}
	return batches
}

// InspectionRequestKey is the column-inspection cache key for req over fields.
func InspectionRequestKey(prefix string, req query.ProviderRequest, fields []string) (string, error) {
	payload, err := json.Marshal(struct {
		Provider         string
		Connection       string
		Query            string
		QueryArgs        []any
		QueryIdentifiers []string
		Options          map[string]any
		Params           map[string]any
		Fields           []string
	}{
		Provider: req.Provider, Connection: req.Connection, Query: req.Query,
		QueryArgs: req.QueryArgs, QueryIdentifiers: req.QueryIdentifiers,
		Options: req.Options, Params: req.Params, Fields: fields,
	})
	if err != nil {
		return "", fmt.Errorf("encode inspection cache key: %w", err)
	}
	return fmt.Sprintf("%s:%x", prefix, sha256.Sum256(payload)), nil
}
