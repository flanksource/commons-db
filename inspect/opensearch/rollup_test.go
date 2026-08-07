package opensearchinspect

import "testing"

func TestTimestampedPattern(t *testing.T) {
	cases := []struct {
		name    string
		pattern string
	}{
		{"jaeger-jaeger-span-2026-07-12", "jaeger-jaeger-span-*"},
		{"security-auditlog-2026.04.14", "security-auditlog-*"},
		{"top_queries-2026.08.04-74270", "top_queries-*"},
		{"logstash-2024.01.01-000001", "logstash-*"},
		{"logs-20240101", "logs-*"},
		{"metrics-2024.01", "metrics-*"},
		{"events.2024.01.01", "events.*"},
		{"my-index-000001", "my-index-*"},
		{"logs-2", ""},
		{"orders-1234", ""},
		{".opendistro_security", ""},
		{"2026-07-12", ""},
	}
	for _, tc := range cases {
		pattern, ok := TimestampedPattern(tc.name)
		if tc.pattern == "" {
			if ok {
				t.Errorf("%s: expected no pattern, got %q", tc.name, pattern)
			}
			continue
		}
		if !ok || pattern != tc.pattern {
			t.Errorf("%s: pattern = %q (%v), want %q", tc.name, pattern, ok, tc.pattern)
		}
	}
}

func TestRollupTargetsFoldsRotations(t *testing.T) {
	targets := []Target{
		{Name: "logs", Kind: "alias"},
		{Name: "jaeger-span-2026-07-11", Kind: "index"},
		{Name: "jaeger-span-2026-07-12", Kind: "index"},
		{Name: "orders", Kind: "index"},
		{Name: "audit-2026.04.14", Kind: "index"},
		{Name: ".ds-traces-2026.07.12-000001", Kind: "index", DataStream: "traces", Hidden: true},
	}
	rolled := RollupTargets(targets)

	patterns := map[string]Target{}
	for _, target := range rolled {
		if target.Kind == "pattern" {
			patterns[target.Name] = target
		}
	}
	if len(patterns) != 1 {
		t.Fatalf("patterns = %#v", patterns)
	}
	span := patterns["jaeger-span-*"]
	if span.Count != 2 {
		t.Errorf("jaeger-span-* count = %d, want 2", span.Count)
	}

	members := map[string]string{}
	for _, target := range rolled {
		if target.Kind == "index" {
			members[target.Name] = target.Pattern
		}
	}
	if members["jaeger-span-2026-07-11"] != "jaeger-span-*" || members["jaeger-span-2026-07-12"] != "jaeger-span-*" {
		t.Errorf("rotation members = %#v", members)
	}
	// A lone dated index is not worth a wildcard, and a data-stream backing index
	// is already named by its data stream.
	if members["audit-2026.04.14"] != "" || members[".ds-traces-2026.07.12-000001"] != "" {
		t.Errorf("unexpected rollup: %#v", members)
	}
	if len(rolled) != len(targets)+1 {
		t.Errorf("rolled %d targets, want %d", len(rolled), len(targets)+1)
	}
}
