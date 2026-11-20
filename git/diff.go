// The unified patch between two revisions of a cached remote repository, as git diff prints it,
// whole or under some paths.
package git

import (
	"context"
	"errors"
	"fmt"
	"slices"
)

// DiffOptions selects the patch of Head against Base in one cached remote Repository. Paths
// (matched literally, never as globs) keeps only changes under them; Renames detects renames (-M).
type DiffOptions struct {
	Repository *Repository
	Base       string
	Head       string
	Paths      []string
	Renames    bool
}

// Diff is the unified patch from Base to Head, without color, fetching either revision the mirror
// lacks.
func (c *Cache) Diff(ctx context.Context, options DiffOptions) (string, error) {
	if options.Base == "" || options.Head == "" {
		return "", errors.New("a diff requires base and head revisions")
	}
	if options.Repository == nil {
		return "", fmt.Errorf("%w: a diff requires a repository", ErrInvalidSource)
	}
	repo := options.Repository
	var output []byte
	err := c.readRefs(ctx, repo, refReadOptions{Refs: []refOn{{repo, options.Base}, {repo, options.Head}}}, func(mirror string, shas []string) (err error) {
		output, err = runBytes(ctx, command{dir: mirror, args: diffArgs(shas, options)})
		return err
	})
	if err != nil {
		return "", err
	}
	return string(output), nil
}

func diffArgs(commits []string, options DiffOptions) []string {
	renames := "--no-renames"
	if options.Renames {
		renames = "-M"
	}
	return slices.Concat([]string{"--literal-pathspecs", "diff", "--no-color", "--no-ext-diff", renames,
		"--end-of-options", commits[0], commits[1], "--"}, options.Paths)
}
