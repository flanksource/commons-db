package profiles

import (
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/query/schema"
)

// profileIcon is the glyph a profile wears in the sidebar and pickers. An
// explicit `icon:` on the profile wins; otherwise the provider type supplies its
// own vendor mark.
func profileIcon(profile query.Profile) string {
	if profile.Icon != "" {
		return profile.Icon
	}
	return providerIcon(profile.Provider.Type)
}

// providerIcon maps a provider type to an opaque UI icon name, reusing the same
// schema.ProviderTypeIcon map the profile form renders — so postgres, sqlserver,
// grafana and opensearch each keep their own mark rather than collapsing into
// one generic glyph.
//
// The names resolve in two places: clicky-ui's surfaceIconMap knows the generic
// ones (database, globe, activity, table), and the consumer's runtime icon
// provider resolves the vendor ones. Unknown providers fall back to a table.
func providerIcon(providerType string) string {
	if icon := schema.ProviderTypeIcon(providerType); icon != "" {
		return icon
	}
	return "table"
}
