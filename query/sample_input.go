package query

import "fmt"

func sampleFilterProfile(profile Profile, columns []ColumnDef) Profile {
	if len(columns) == 0 {
		return profile
	}
	profile.Columns = columns
	return profile
}

func sampleInput(params map[string]any, filters map[string]string) (map[string]any, error) {
	input := make(map[string]any, len(params)+len(filters))
	for key, value := range params {
		input[key] = value
	}
	for key, value := range filters {
		if _, exists := input[key]; exists {
			return nil, fmt.Errorf("sample input %q is present as both a parameter and a filter", key)
		}
		input[key] = value
	}
	return input, nil
}
