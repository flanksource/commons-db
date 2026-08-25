package opensearchinspect

import (
	"regexp"
	"sort"
	"strings"
)

// A rotated index carries a date, and optionally a rollover sequence, in its
// suffix — `jaeger-span-2026-07-12`, `security-auditlog-2026.04.14`,
// `top_queries-2026.08.04-74270`, `logstash-2024.01.01-000001`. The prefix is
// the logical target an author means; the suffix is bookkeeping.
var (
	datedSuffix    = regexp.MustCompile(`[-_.](\d{4}[-._]?\d{2}([-._]?\d{2})?|\d{8})([-_.]\d+)?$`)
	rolloverSuffix = regexp.MustCompile(`[-_.]\d{6,}$`)
)

// TimestampedPattern reports the wildcard a rotated index rolls up into, and
// whether the name looked rotated at all.
func TimestampedPattern(name string) (string, bool) {
	for _, suffix := range []*regexp.Regexp{datedSuffix, rolloverSuffix} {
		match := suffix.FindStringIndex(name)
		if match == nil || match[0] == 0 {
			continue
		}
		return name[:match[0]] + string(name[match[0]]) + "*", true
	}
	return "", false
}

// RollupTargets folds rotations of the same index into one wildcard target, so
// an author picks `jaeger-span-*` once instead of scrolling fifty-three daily
// indexes. A pattern is only synthesised for two or more rotations — a lone
// dated index is already its own best name — and data-stream backing indices
// are left alone, because the data stream already names them.
func RollupTargets(targets []Target) []Target {
	members := map[string][]int{}
	for i, target := range targets {
		if target.Kind != "index" || target.DataStream != "" {
			continue
		}
		if pattern, ok := TimestampedPattern(target.Name); ok {
			members[pattern] = append(members[pattern], i)
		}
	}

	rolled := append(make([]Target, 0, len(targets)+len(members)), targets...)
	var patterns []Target
	for pattern, indexes := range members {
		if len(indexes) < 2 {
			continue
		}
		hidden, system := true, true
		exactMembers := make([]string, 0, len(indexes))
		for _, i := range indexes {
			rolled[i].Pattern = pattern
			hidden = hidden && rolled[i].Hidden
			system = system && rolled[i].System
			exactMembers = append(exactMembers, rolled[i].Name)
		}
		sort.Strings(exactMembers)
		patterns = append(patterns, Target{
			Name: strings.Join(exactMembers, ","), Pattern: pattern, Kind: "group",
			Count: len(indexes), Members: exactMembers, Hidden: hidden, System: system,
		})
	}
	sort.Slice(patterns, func(a, b int) bool { return patterns[a].Pattern < patterns[b].Pattern })
	return append(patterns, rolled...)
}
