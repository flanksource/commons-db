package query

import (
	"maps"
	"slices"

	"github.com/flanksource/clicky"
	"github.com/flanksource/clicky/api"
)

func (r Row) Pretty() api.Text {
	t := clicky.Text(r.Profile, "text-blue-500 font-bold").Space().Append(r.Provider, "text-muted")
	for _, key := range slices.Sorted(maps.Keys(r.Row)) {
		t = t.Space().Append(key+"=", "text-muted").Add(clicky.Human(r.Row[key]))
	}
	return t
}

func (r Row) PrettyFull() api.Textable { return r.Pretty().NewLine().Add(clicky.Map(r.Row)) }
