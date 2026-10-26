package git

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// applyInIndex commits request.Files onto base without a worktree, in the
// mirror under the shared lock: a scratch index read from base takes the files
// through update-index from a scratch directory holding only them (so the
// base's .gitattributes still apply and an existing file keeps its mode), then
// write-tree, commit-tree and a push of that commit.
func (c *Cache) applyInIndex(ctx context.Context, repo *Repository, base Ref, request ChangeRequest) (ChangeResult, error) {
	var result ChangeResult
	err := c.withRefs(ctx, repo, refReadOptions{Refs: []refOn{{repo, base.SHA}}}, func(mirror string, _ []string) error {
		sha, err := commitInIndex(ctx, mirror, base.SHA, request)
		if err != nil {
			return err
		}
		result, err = publishCommit(ctx, mirror, repo, sha, request)
		return err
	})
	return result, err
}

// indexScratch is a temporary index over the mirror with a work tree holding
// only the changed files.
type indexScratch struct {
	mirror, index, tree string
}

func (s indexScratch) run(ctx context.Context, stdin []byte, args ...string) (string, error) {
	env := append(baseEnv(configPair{"core.fileMode", "false"}), "GIT_INDEX_FILE="+s.index)
	prefix := []string{"--git-dir=" + s.mirror, "--work-tree=" + s.tree, "--literal-pathspecs"}
	return run(ctx, command{dir: s.tree, env: env, stdin: stdin, args: append(prefix, args...)})
}

// commitInIndex writes the commit of request.Files on base and returns its
// SHA, refusing a change that leaves base's tree as it was.
func commitInIndex(ctx context.Context, mirror, base string, request ChangeRequest) (sha string, err error) {
	if strings.TrimSpace(request.Message) == "" {
		return "", errors.New("commit message is required")
	}
	identity, err := request.Author.env()
	if err != nil {
		return "", err
	}
	scratch, err := os.MkdirTemp(filepath.Dir(mirror), "index-")
	if err != nil {
		return "", fmt.Errorf("create scratch index: %w", err)
	}
	defer func() { err = errors.Join(err, os.RemoveAll(scratch)) }()
	index := indexScratch{mirror: mirror, index: filepath.Join(scratch, "index"), tree: filepath.Join(scratch, "tree")}
	if err := os.Mkdir(index.tree, 0o755); err != nil {
		return "", fmt.Errorf("create scratch work tree: %w", err)
	}
	if _, err := index.run(ctx, nil, "read-tree", "--end-of-options", base); err != nil {
		return "", err
	}
	if err := index.stage(ctx, request.Files); err != nil {
		return "", err
	}
	tree, err := index.run(ctx, nil, "write-tree")
	if err != nil {
		return "", err
	}
	baseTree, err := run(ctx, command{dir: mirror, args: []string{"rev-parse", "--verify", "--end-of-options", base + "^{tree}"}})
	if err != nil {
		return "", err
	}
	if tree == baseTree {
		return "", fmt.Errorf("change %s has nothing to commit: its files are already on %s", request.Branch, request.Base)
	}
	return run(ctx, command{dir: mirror, env: append(baseEnv(), identity...), stdin: []byte(cleanupWhitespace(request.Message)),
		args: []string{"commit-tree", "-p", base, "-F", "-", "--end-of-options", tree}})
}

// stage applies files in order to the scratch work tree, then updates the
// index with every touched path: a present file is added, an absent one
// removed. Deleting a file neither written earlier nor in the base fails.
func (s indexScratch) stage(ctx context.Context, files []FileChange) error {
	var paths, inBase []string
	present := map[string]bool{}
	for _, file := range files {
		clean, err := cleanChangePath(file.Path)
		if err != nil {
			return err
		}
		path, target := filepath.ToSlash(clean), filepath.Join(s.tree, clean)
		if !slices.Contains(paths, path) {
			paths = append(paths, path)
		}
		wasPresent, seen := present[path]
		present[path] = !file.Delete
		if !file.Delete {
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return fmt.Errorf("create directory for %s: %w", file.Path, err)
			}
			if err := os.WriteFile(target, file.Content, 0o644); err != nil {
				return fmt.Errorf("write %s: %w", file.Path, err)
			}
			continue
		}
		if seen && !wasPresent {
			return fmt.Errorf("delete %s: it was already deleted", file.Path)
		}
		if !seen {
			inBase = append(inBase, path)
		}
		if err := os.Remove(target); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("delete %s: %w", file.Path, err)
		}
	}
	if err := s.requireEntries(ctx, inBase); err != nil {
		return err
	}
	_, err := s.run(ctx, []byte(strings.Join(paths, "\x00")+"\x00"), "update-index", "--add", "--remove", "-z", "--stdin")
	return err
}

// requireEntries fails unless every path is a file of the index.
func (s indexScratch) requireEntries(ctx context.Context, paths []string) error {
	if len(paths) == 0 {
		return nil
	}
	listed, err := s.run(ctx, nil, append([]string{"ls-files", "-z", "--cached", "--"}, paths...)...)
	if err != nil {
		return err
	}
	entries := strings.Split(listed, "\x00")
	for _, path := range paths {
		if !slices.Contains(entries, path) {
			return fmt.Errorf("delete %s: no such file", path)
		}
	}
	return nil
}

// publishCommit pushes sha as request.Branch, with request.Tag created on it
// in the mirror for the push and deleted afterwards.
func publishCommit(ctx context.Context, mirror string, repo *Repository, sha string, request ChangeRequest) (result ChangeResult, err error) {
	options := PushOptions{Branch: request.Branch, Commit: sha}
	result = ChangeResult{Branch: request.Branch, CommitSHA: sha}
	if request.Tag != nil {
		tag := *request.Tag
		tag.Tagger = request.Author
		if err := createTag(ctx, mirror, sha, tag); err != nil {
			return ChangeResult{}, err
		}
		defer func() {
			_, deleted := run(ctx, command{dir: mirror, args: []string{"tag", "--delete", "--end-of-options", tag.Name}})
			err = errors.Join(err, deleted)
		}()
		options.Tags, result.Tag = []string{tag.Name}, tag.Name
	}
	if err := push(ctx, mirror, repo, options); err != nil {
		return ChangeResult{}, err
	}
	return result, nil
}

// cleanupWhitespace is git commit's whitespace cleanup of a message: trailing
// whitespace stripped from each line, leading and trailing blank lines dropped,
// runs of blank lines collapsed to one, ending in a newline.
func cleanupWhitespace(message string) string {
	var kept []string
	for _, line := range strings.Split(message, "\n") {
		line = strings.TrimRight(line, " \t\r\v\f")
		if line == "" && (len(kept) == 0 || kept[len(kept)-1] == "") {
			continue
		}
		kept = append(kept, line)
	}
	for len(kept) > 0 && kept[len(kept)-1] == "" {
		kept = kept[:len(kept)-1]
	}
	return strings.Join(kept, "\n") + "\n"
}
