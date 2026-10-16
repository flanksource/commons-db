package providerkit

import (
	"fmt"

	"github.com/flanksource/commons-db/query"
)

// OrderKeys reads a row's position in the declared order, which is what the
// next page resumes after.
func OrderKeys(row query.Row, order query.Order) ([]any, error) {
	keys := make([]any, 0, len(order))
	for _, by := range order {
		value, ok := row[by.Column]
		if !ok {
			return nil, fmt.Errorf(
				"cannot cursor on column %q: the query does not return it, so there is no position to resume from", by.Column)
		}
		keys = append(keys, value)
	}
	return keys, nil
}
