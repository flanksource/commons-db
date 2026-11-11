package query

import (
	"fmt"
	"math/big"
	"slices"
	"strings"
)

// SortRows merges independently ordered sample windows before paging them.
// Missing measurements stay last in either direction; exact integers retain precision.
func SortRows(rows []Row, order Order) []Row {
	result := slices.Clone(rows)
	slices.SortStableFunc(result, func(a, b Row) int {
		for _, by := range order {
			av, bv := a[by.Column], b[by.Column]
			if av == nil && bv == nil {
				continue
			}
			if av == nil {
				return 1
			}
			if bv == nil {
				return -1
			}
			comparison := strings.Compare(fmt.Sprint(av), fmt.Sprint(bv))
			left, lok := new(big.Rat).SetString(fmt.Sprint(av))
			right, rok := new(big.Rat).SetString(fmt.Sprint(bv))
			_, leftString := av.(string)
			_, rightString := bv.(string)
			if lok && rok && !leftString && !rightString {
				comparison = left.Cmp(right)
			}
			if comparison != 0 {
				if by.Desc {
					return -comparison
				}
				return comparison
			}
		}
		return 0
	})
	return result
}
