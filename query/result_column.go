package query

// ResultColumnOptions describes the profile output columns for an interactive
// result surface. DatabaseTypes is optional display-only metadata reported by
// SQL drivers.
type ResultColumnOptions struct {
	Profile       Profile
	DatabaseTypes map[string]string
}

// ResultColumn is the transport contract consumed by clicky-ui's QueryBrowser.
// It is intentionally separate from ColumnDef: ColumnDef is authoring metadata,
// while this type carries the resolved filter key and control the server can
// actually honour.
type ResultColumn struct {
	Name         string              `json:"name"`
	Label        string              `json:"label,omitempty"`
	DatabaseType string              `json:"databaseType,omitempty"`
	Kind         ColumnKind          `json:"kind,omitempty"`
	FilterKey    string              `json:"filterKey,omitempty"`
	Filter       *ResultColumnFilter `json:"filter,omitempty"`
}

type ResultColumnFilter struct {
	Kind    string                     `json:"kind"`
	Options []ResultColumnFilterOption `json:"options,omitempty"`
	Lookup  bool                       `json:"lookup,omitempty"`
	Multi   bool                       `json:"multi,omitempty"`
	Unit    string                     `json:"unit,omitempty"`
}

type ResultColumnFilterOption struct {
	Value string `json:"value"`
}

func DescribeResultColumns(options ResultColumnOptions) ([]ResultColumn, error) {
	bindings, err := options.Profile.ColumnFilterBindings()
	if err != nil {
		return nil, err
	}
	byColumn := make(map[string]ColumnFilterBinding, len(bindings))
	for _, binding := range bindings {
		byColumn[binding.Column] = binding
	}

	columns := make([]ResultColumn, 0, len(options.Profile.Columns))
	for _, column := range options.Profile.Columns {
		if column.Hidden {
			continue
		}
		result := ResultColumn{
			Name: column.Name, DatabaseType: options.DatabaseTypes[column.Name], Kind: column.Kind,
		}
		if column.Label != "" && column.Label != column.Name {
			result.Label = column.Label
		}
		if binding, ok := byColumn[column.Name]; ok {
			result.FilterKey = binding.Key
			result.Filter = &ResultColumnFilter{
				Kind: string(binding.Kind.Normalized()), Lookup: binding.Lookup,
				Multi: binding.Multi, Unit: binding.Unit,
			}
			for _, value := range binding.Options {
				result.Filter.Options = append(result.Filter.Options, ResultColumnFilterOption{Value: value})
			}
		}
		columns = append(columns, result)
	}
	return columns, nil
}
