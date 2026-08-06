package profiles

import (
	"testing"

	"github.com/flanksource/commons-db/query"
)

func namedProfiles(names ...string) []query.Profile {
	profiles := make([]query.Profile, len(names))
	for i, name := range names {
		profiles[i] = query.Profile{Name: name}
	}
	return profiles
}

func TestProfileOptions(t *testing.T) {
	all := namedProfiles("jms", "jms.incoming", "logs.api", "remote-debugger.jdbc")

	// The stored value of `imports` / `reconcile.dest` is the profile name, so
	// the option key must be the bare name — decorating it (as the connection
	// lookup does with "(type)") would also break the picker's hierarchy split.
	options, total := profileOptions(all, "", 0)
	if total != len(all) {
		t.Errorf("total = %d, want %d", total, len(all))
	}
	for _, profile := range all {
		option, ok := options[profile.Name]
		if !ok {
			t.Fatalf("no option keyed %q in %v", profile.Name, options)
		}
		if got := option.String(); got != profile.Name {
			t.Errorf("option %q label = %q, want the bare name", profile.Name, got)
		}
	}
}

func TestProfileOptionsFiltersByNameSubstring(t *testing.T) {
	all := namedProfiles("jms", "jms.incoming", "logs.api")

	options, total := profileOptions(all, "jms", 0)
	if total != 2 {
		t.Errorf("total = %d, want 2", total)
	}
	if _, ok := options["logs.api"]; ok {
		t.Error("logs.api matched the query \"jms\"")
	}

	// Search reaches past the first segment, so a user who knows only the leaf
	// still finds it.
	if _, total := profileOptions(all, "INCOMING", 0); total != 1 {
		t.Errorf("case-insensitive leaf search total = %d, want 1", total)
	}
}

func TestProfileOptionsCapsAtLimitButReportsTheFullCount(t *testing.T) {
	all := namedProfiles("a", "b", "c", "d")

	options, total := profileOptions(all, "", 2)
	if len(options) != 2 {
		t.Errorf("returned %d options, want 2", len(options))
	}
	// total is the pre-cap count so the picker can say "… and N more".
	if total != 4 {
		t.Errorf("total = %d, want 4", total)
	}
}

func TestProfileFilterKeyMatchesTheSchemaLookup(t *testing.T) {
	// query/schema/profile.go declares `"filter": "profile"` on the imports and
	// reconcile.dest lookups; a mismatch here returns an empty option set with
	// no error, so the picker would just look broken.
	if got := (profileFilter{}).Key(); got != "profile" {
		t.Errorf("profileFilter.Key() = %q, want profile", got)
	}
}
