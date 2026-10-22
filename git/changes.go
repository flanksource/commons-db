package git

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// ChangesOptions selects the two endpoint trees to compare.
type ChangesOptions struct {
	Base string
	Head string
}

// Change describes a literal path's object and mode changes. Missing objects
// have empty IDs; mode-only changes keep equal OldObject and NewObject IDs.
type Change struct {
	Path      string
	Status    FileStatus
	OldObject string
	NewObject string
	OldMode   string
	NewMode   string
}

// Changes compares endpoint trees without reading file bodies. Renames are
// represented as a deletion and an addition.
func (c *Cache) Changes(ctx context.Context, repo *Repository, options ChangesOptions) ([]Change, error) {
	if options.Base == "" || options.Head == "" {
		return nil, errors.New("tree changes require base and head revisions")
	}
	var output []byte
	err := c.withRefs(ctx, repo, refReadOptions{Refs: []refOn{{repo, options.Base}, {repo, options.Head}}}, func(mirror string, shas []string) error {
		var readErr error
		output, readErr = runBytes(ctx, command{dir: mirror, args: []string{"diff-tree", "--no-commit-id", "--raw", "-r", "-z", "--no-abbrev", "--no-renames", "--no-ext-diff", "--end-of-options", shas[0], shas[1], "--"}})
		return readErr
	})

	if err != nil {
		return nil, err
	}
	return parseChanges(string(output))
}

func parseChanges(output string) ([]Change, error) {
	changes := []Change{}
	for output != "" {
		header, rest, ok := strings.Cut(output, "\x00")
		if !ok {
			return nil, errors.New("unterminated Git tree change header")
		}
		path, rest, ok := strings.Cut(rest, "\x00")
		fields := strings.Fields(strings.TrimPrefix(header, ":"))
		if !ok || !strings.HasPrefix(header, ":") || len(fields) != 5 || path == "" {
			return nil, fmt.Errorf("invalid Git tree change %q", header)
		}
		change := Change{Path: path, OldMode: fields[0], NewMode: fields[1], OldObject: fields[2], NewObject: fields[3], Status: FileStatus(fields[4])}
		if strings.Trim(change.OldObject, "0") == "" {
			change.OldObject = ""
		}
		if strings.Trim(change.NewObject, "0") == "" {
			change.NewObject = ""
		}
		changes = append(changes, change)
		output = rest
	}
	return changes, nil
}
