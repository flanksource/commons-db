package sqlite

import (
	"fmt"
	"maps"
	"regexp"
	"slices"

	"github.com/bmatcuk/doublestar/v4"

	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/sqlite"
)

// PartitionSet names SQLite files sharing one schema that a sqlite profile
// reads as one result, such as one file per day or per tenant.
type PartitionSet struct {
	// Files lists the partitions explicitly. Exclusive with Glob.
	Files []string `json:"files,omitempty"`

	// Glob matches the partitions, with ** for any depth.
	Glob string `json:"glob,omitempty"`

	// Labels is a regular expression over each partition's path whose named
	// groups become constant columns of that partition's rows, such as
	// `dt=(?P<dt>[0-9-]+)`. Every partition must match it. An exact-value filter
	// on a label column skips the partitions it excludes without opening them.
	Labels string `json:"labels,omitempty"`

	// Immutable opens every partition with immutable=1: SQLite then skips all
	// locking, which is only correct for sealed files nothing writes again.
	Immutable bool `json:"immutable,omitempty"`

	// Concurrency bounds the partitions read at once; zero means 8.
	Concurrency int `json:"concurrency,omitempty"`
}

// Partition is one resolved file of a PartitionSet.
type Partition struct {
	// Name is the file's path, which keeps it stable across requests and
	// unique within the set.
	Name   string
	DSN    string
	Labels map[string]string
}

// Resolve lists the partitions, extracts their labels and drops those an
// exact-value filter on a label excludes. An empty result is an error: a read
// over no files names a layout that does not exist, which an empty table
// would hide.
func (s PartitionSet) Resolve(filters []query.ColumnFilterValue) ([]Partition, error) {
	paths, err := s.paths()
	if err != nil {
		return nil, err
	}
	labels, err := s.labelPattern()
	if err != nil {
		return nil, err
	}
	partitions := make([]Partition, 0, len(paths))
	for _, path := range paths {
		values, err := pathLabels(labels, path)
		if err != nil {
			return nil, err
		}
		if !labelsSelected(values, filters) {
			continue
		}
		dsn := sqlite.ReadOnlyDSN(path)
		if s.Immutable {
			dsn += "&immutable=1"
		}
		partitions = append(partitions, Partition{Name: path, DSN: dsn, Labels: values})
	}
	if len(partitions) == 0 {
		return nil, fmt.Errorf("partitions: no files remain of %d matched after filtering", len(paths))
	}
	return partitions, nil
}

func (s PartitionSet) paths() ([]string, error) {
	switch {
	case len(s.Files) > 0 && s.Glob != "":
		return nil, fmt.Errorf("partitions: files and glob are exclusive")
	case len(s.Files) > 0:
		paths := slices.Clone(s.Files)
		slices.Sort(paths)
		if duplicate := slices.Compact(slices.Clone(paths)); len(duplicate) != len(paths) {
			return nil, fmt.Errorf("partitions: a file is listed twice")
		}
		return paths, nil
	case s.Glob != "":
		paths, err := doublestar.FilepathGlob(s.Glob, doublestar.WithFilesOnly())
		if err != nil {
			return nil, fmt.Errorf("partitions: glob %q: %w", s.Glob, err)
		}
		if len(paths) == 0 {
			return nil, fmt.Errorf("partitions: glob %q matches no files", s.Glob)
		}
		slices.Sort(paths)
		return paths, nil
	}
	return nil, fmt.Errorf("partitions: files or glob is required")
}

func (s PartitionSet) labelPattern() (*regexp.Regexp, error) {
	if s.Labels == "" {
		return nil, nil
	}
	pattern, err := regexp.Compile(s.Labels)
	if err != nil {
		return nil, fmt.Errorf("partitions: labels %q: %w", s.Labels, err)
	}
	if !slices.ContainsFunc(pattern.SubexpNames(), func(name string) bool { return name != "" }) {
		return nil, fmt.Errorf("partitions: labels %q has no named group, so it extracts nothing", s.Labels)
	}
	return pattern, nil
}

func pathLabels(pattern *regexp.Regexp, path string) (map[string]string, error) {
	if pattern == nil {
		return nil, nil
	}
	match := pattern.FindStringSubmatch(path)
	if match == nil {
		return nil, fmt.Errorf("partitions: %q does not match labels %q", path, pattern)
	}
	labels := map[string]string{}
	for index, name := range pattern.SubexpNames() {
		if name != "" {
			labels[name] = match[index]
		}
	}
	return labels, nil
}

// labelsSelected applies the exact-value filters on label columns. Any other
// filter, or a filter on a stored column, is left to SQL in every partition,
// which is always correct and only costs opening the file.
func labelsSelected(labels map[string]string, filters []query.ColumnFilterValue) bool {
	for _, filter := range filters {
		value, isLabel := labels[filter.Field]
		if !isLabel || filter.Kind.CompilesAs() != query.ColumnFilterKindTerms || filter.Array || filter.Nested != "" {
			continue
		}
		if len(filter.Include) > 0 && !slices.Contains(filter.Include, value) {
			return false
		}
		if slices.Contains(filter.Exclude, value) {
			return false
		}
	}
	return true
}

// labelColumns are a partition's labels in a stable order.
func labelColumns(labels map[string]string) []string {
	return slices.Sorted(maps.Keys(labels))
}
