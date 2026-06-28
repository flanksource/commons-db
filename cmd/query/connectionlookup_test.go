package main

import (
	"sort"
	"testing"

	"github.com/flanksource/clicky/api"
	"github.com/flanksource/commons-db/models"
)

func conns(names ...string) []*models.Connection {
	out := make([]*models.Connection, len(names))
	for i, n := range names {
		out[i] = &models.Connection{Name: n, Type: "postgres"}
	}
	return out
}

// TestConnectionOptionsValuesAreReferences guards that an option key is the
// connection:// reference a Profile stores, and the label carries the type.
func TestConnectionOptionsValuesAreReferences(t *testing.T) {
	opts, total := connectionOptions(
		[]*models.Connection{{Name: "prod", Type: "postgres"}}, "", 0)
	if total != 1 {
		t.Fatalf("total = %d, want 1", total)
	}
	label, ok := opts["connection://prod"]
	if !ok {
		t.Fatalf("option key should be connection://prod, got keys %v", keys(opts))
	}
	if got := label.String(); got != "prod (postgres)" {
		t.Errorf("label = %q, want %q", got, "prod (postgres)")
	}
}

func TestConnectionOptionsFiltersByNameQuery(t *testing.T) {
	opts, total := connectionOptions(conns("alpha", "beta", "alpha-2"), "alph", 0)
	if total != 2 {
		t.Fatalf("total = %d, want 2 (alpha, alpha-2)", total)
	}
	got := keys(opts)
	want := []string{"connection://alpha", "connection://alpha-2"}
	if !equalUnordered(got, want) {
		t.Errorf("matched keys = %v, want %v", got, want)
	}
}

// TestConnectionOptionsCapsAtLimit verifies the head set is capped while total
// still reports the full match count (so the UI can show "… and N more").
func TestConnectionOptionsCapsAtLimit(t *testing.T) {
	opts, total := connectionOptions(conns("a", "b", "c", "d"), "", 2)
	if total != 4 {
		t.Errorf("total = %d, want 4 (full match count)", total)
	}
	if len(opts) != 2 {
		t.Errorf("returned %d options, want 2 (capped at limit)", len(opts))
	}
}

func TestConnectionTypeFilter(t *testing.T) {
	cases := []struct {
		name string
		o    connListOpts
		want []string
	}{
		{"single type flag", connListOpts{Type: "postgres"}, []string{"postgres"}},
		{"csv types scope", connListOpts{Types: "postgres,mysql,sql_server"}, []string{"postgres", "mysql", "sql_server"}},
		{"trims blanks", connListOpts{Types: "postgres, ,mysql,"}, []string{"postgres", "mysql"}},
		{"combines flag and scope", connListOpts{Type: "loki", Types: "jaeger"}, []string{"loki", "jaeger"}},
		{"empty", connListOpts{}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := connectionTypeFilter(tc.o)
			if !equalUnordered(got, tc.want) {
				t.Errorf("connectionTypeFilter(%+v) = %v, want %v", tc.o, got, tc.want)
			}
		})
	}
}

func keys(m map[string]api.Textable) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func equalUnordered(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	a, b = append([]string(nil), a...), append([]string(nil), b...)
	sort.Strings(a)
	sort.Strings(b)
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
