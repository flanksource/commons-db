package git

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// ChangesOptions selects the two endpoint trees to compare, from one cached
// remote Repository or one existing local Directory (working or bare, read
// through the same private scratch as OpenTree, never fetched). Paths
// (matched literally, never as globs) keeps only changes under them; Renames
// detects renames (-M), reporting each once as FileRenamed with its OldPath.
type ChangesOptions struct {
	Repository *Repository
	Directory  string
	Base       string
	Head       string
	Paths      []string
	Renames    bool
}

// Change describes a literal path's object and mode changes. Missing objects
// have empty IDs; mode-only changes keep equal OldObject and NewObject IDs.
// OldPath is set for a FileRenamed change only.
type Change struct {
	Path      string
	OldPath   string
	Status    FileStatus
	OldObject string
	NewObject string
	OldMode   string
	NewMode   string
}

// Changes compares endpoint trees without reading file bodies. Without
// Renames, a rename is a deletion and an addition.
func (c *Cache) Changes(ctx context.Context, options ChangesOptions) ([]Change, error) {
	if options.Base == "" || options.Head == "" {
		return nil, errors.New("tree changes require base and head revisions")
	}
	if (options.Repository == nil) == (options.Directory == "") {
		return nil, fmt.Errorf("%w: choose one repository or local directory", ErrInvalidSource)
	}
	if options.Directory != "" {
		return c.localChanges(ctx, options)
	}
	var output []byte
	repo := options.Repository
	err := c.readRefs(ctx, repo, refReadOptions{Refs: []refOn{{repo, options.Base}, {repo, options.Head}}}, func(mirror string, shas []string) (err error) {
		output, err = runBytes(ctx, command{dir: mirror, args: diffTreeArgs(shas, options)})
		return err
	})
	if err != nil {
		return nil, err
	}
	return parseChanges(string(output))
}

func (c *Cache) localChanges(ctx context.Context, options ChangesOptions) (changes []Change, err error) {
	if c.Root == "" {
		return nil, fmt.Errorf("%w: cache root is required", ErrInvalidSource)
	}
	local, err := openLocalRepository(ctx, localRepositoryOptions{CacheRoot: c.Root, Directory: options.Directory, Revisions: []string{options.Head, options.Base}})
	if local != nil {
		defer func() { err = errors.Join(err, local.close()) }()
	}
	if err != nil {
		return nil, err
	}
	output, err := runBytes(ctx, command{dir: local.source.gitDir, env: local.source.env, args: diffTreeArgs([]string{local.commits[1], local.commits[0]}, options)})
	if err != nil {
		return nil, err
	}
	return parseChanges(string(output))
}

func diffTreeArgs(commits []string, options ChangesOptions) []string {
	renames := "--no-renames"
	if options.Renames {
		renames = "-M"
	}
	return slices.Concat([]string{"--literal-pathspecs", "diff-tree", "--no-commit-id", "--raw", "-r", "-z", "--no-abbrev", renames,
		"--no-ext-diff", "--end-of-options", commits[0], commits[1], "--"}, options.Paths)
}

func parseChanges(output string) ([]Change, error) {
	changes := []Change{}
	for output != "" {
		header, rest, ok := strings.Cut(output, "\x00")
		if !ok {
			return nil, errors.New("unterminated Git tree change header")
		}
		fields := strings.Fields(strings.TrimPrefix(header, ":"))
		if !strings.HasPrefix(header, ":") || len(fields) != 5 {
			return nil, fmt.Errorf("invalid Git tree change %q", header)
		}
		change := Change{OldMode: fields[0], NewMode: fields[1], OldObject: fields[2], NewObject: fields[3],
			Status: FileStatus(strings.TrimRight(fields[4], "0123456789"))}
		if change.Status == FileRenamed || change.Status == FileCopied {
			if change.OldPath, rest, ok = strings.Cut(rest, "\x00"); !ok || change.OldPath == "" {
				return nil, fmt.Errorf("invalid Git tree change source %q", header)
			}
		}
		if change.Path, rest, ok = strings.Cut(rest, "\x00"); !ok || change.Path == "" {
			return nil, fmt.Errorf("invalid Git tree change %q", header)
		}
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
