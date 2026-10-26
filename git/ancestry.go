package git

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
)

// AncestorsOptions names a head in one cached remote Repository or one
// existing local Directory (working or bare, read in place, never fetched).
// Max bounds how many commits are listed and is required.
type AncestorsOptions struct {
	Repository *Repository
	Directory  string
	Head       string
	Max        int
}

// Ancestors lists Head and its ancestors nearest first (rev-list order), at
// most Max of them, so a caller ranks stored commits by distance in one spawn
// (a Head that is not a full commit ID is resolved first). A cached
// repository fetches Head with its history.
func (c *Cache) Ancestors(ctx context.Context, options AncestorsOptions) ([]string, error) {
	if options.Max <= 0 || options.Head == "" {
		return nil, fmt.Errorf("%w: ancestry requires a head and a positive max", ErrInvalidSource)
	}
	if (options.Repository == nil) == (options.Directory == "") {
		return nil, fmt.Errorf("%w: choose one repository or local directory", ErrInvalidSource)
	}
	if options.Directory != "" {
		head := options.Head
		if !fullSHAPattern.MatchString(head) {
			source := &PreparedSource{info: SourceInfo{RepositoryRoot: options.Directory}}
			resolved, err := source.resolveCommit(ctx, head)
			if err != nil {
				return nil, err
			}
			head = resolved
		}
		return revList(ctx, command{dir: options.Directory, env: append(baseEnv(), "GIT_NO_REPLACE_OBJECTS=1")}, head, options.Max)
	}
	var ancestors []string
	repo := options.Repository
	err := c.readRefs(ctx, repo, refReadOptions{Refs: []refOn{{repo, options.Head}}, History: true}, func(mirror string, shas []string) (err error) {
		ancestors, err = revList(ctx, command{dir: mirror}, shas[0], options.Max)
		return err
	})
	return ancestors, err
}

func revList(ctx context.Context, c command, head string, limit int) ([]string, error) {
	c.args = []string{"rev-list", "--max-count=" + strconv.Itoa(limit), "--end-of-options", head, "--"}
	listed, err := run(ctx, c)
	if err != nil {
		return nil, err
	}
	return strings.Fields(listed), nil
}

// LocalRevisionOptions names a revision of the existing local repository
// holding Directory.
type LocalRevisionOptions struct {
	Directory string
	Revision  string
}

// LocalRevision is where Directory sits in its repository: Root is the work
// tree (a bare repository's git directory), Prefix the slash-separated path of
// Directory below it ("" at the root or in a bare repository), Commit the
// revision's full commit ID. Paths are physical: git reports them with
// symlinks resolved.
type LocalRevision struct {
	Root   string
	Bare   bool
	Prefix string
	Commit string
}

// ResolveLocalRevision locates Directory in its repository and resolves
// Revision there, outside any cache, in two spawns.
func ResolveLocalRevision(ctx context.Context, options LocalRevisionOptions) (LocalRevision, error) {
	directory, err := filepath.Abs(options.Directory)
	if err == nil {
		directory, err = filepath.EvalSymlinks(directory)
	}
	if err != nil {
		return LocalRevision{}, err
	}
	output, err := runBytes(ctx, command{dir: directory, args: []string{"rev-parse", "--is-bare-repository", "--absolute-git-dir", "--show-prefix"}})
	if err != nil {
		return LocalRevision{}, fmt.Errorf("%w: %s: %w", ErrNotRepository, directory, err)
	}
	lines := strings.Split(strings.TrimSuffix(string(output), "\n"), "\n")
	if len(lines) != 3 || (lines[0] != "true" && lines[0] != "false") {
		return LocalRevision{}, fmt.Errorf("unexpected rev-parse output %q for %s", output, directory)
	}
	revision := LocalRevision{Bare: lines[0] == "true", Prefix: strings.TrimSuffix(lines[2], "/"), Root: lines[1]}
	if !revision.Bare {
		root, found := strings.CutSuffix(filepath.ToSlash(directory), "/"+revision.Prefix)
		if revision.Prefix == "" {
			root, found = directory, true
		}
		if !found {
			return LocalRevision{}, errors.New("git prefix " + revision.Prefix + " does not end " + directory)
		}
		revision.Root = filepath.FromSlash(root)
	}
	source := &PreparedSource{info: SourceInfo{RepositoryRoot: directory}}
	if revision.Commit, err = source.resolveCommit(ctx, options.Revision); err != nil {
		return LocalRevision{}, err
	}
	return revision, nil
}

// LocalFilesOptions names root-relative Files (matched literally) to read at
// Commit of the local repository at Root.
type LocalFilesOptions struct {
	Root   string
	Commit string
	Files  []string
}

// LocalFile is one committed tree entry; Content is read for a blob only.
type LocalFile struct {
	Mode    string
	Type    string
	Content []byte
}

// ReadLocalFiles lists the requested entries Commit has in one spawn and reads
// each blob among them in one more.
func ReadLocalFiles(ctx context.Context, options LocalFilesOptions) (map[string]LocalFile, error) {
	if options.Root == "" || options.Commit == "" || len(options.Files) == 0 {
		return nil, fmt.Errorf("%w: reading local files requires a root, a commit and files", ErrInvalidSource)
	}
	env := append(baseEnv(), "GIT_NO_REPLACE_OBJECTS=1")
	listed, err := runBytes(ctx, command{dir: options.Root, env: env,
		args: append([]string{"--literal-pathspecs", "ls-tree", "-z", "--full-tree", "--end-of-options", options.Commit, "--"}, options.Files...)})
	if err != nil {
		return nil, err
	}
	found := map[string]LocalFile{}
	for _, entry := range strings.Split(string(listed), "\x00") {
		if entry == "" {
			continue
		}
		header, name, ok := strings.Cut(entry, "\t")
		fields := strings.Fields(header)
		if !ok || len(fields) != 3 {
			return nil, fmt.Errorf("invalid tree entry %q at %s", entry, options.Commit)
		}
		file := LocalFile{Mode: fields[0], Type: fields[1]}
		if file.Type == "blob" {
			if file.Content, err = runBytes(ctx, command{dir: options.Root, env: env, args: []string{"cat-file", "blob", fields[2]}}); err != nil {
				return nil, err
			}
		}
		found[name] = file
	}
	return found, nil
}
