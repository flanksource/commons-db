package git

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// ErrPathNotFound is returned by Cache.Show when the path does not exist at
// the commit (never added, or deleted by then).
var ErrPathNotFound = errors.New("path not found at commit")

// Show returns the bytes of the file at path in commit sha, read from the
// bare mirror; a commit the mirror lacks is fetched first. sha must be a full
// 40-character SHA. path is matched literally (no pathspec magic); a path that
// names a directory is an error.
func (c *Cache) Show(ctx context.Context, repo *Repository, sha, path string) ([]byte, error) {
	if !fullSHAPattern.MatchString(sha) {
		return nil, fmt.Errorf("show %q: give the full 40-character commit SHA", sha)
	}
	if path == "" {
		return nil, errors.New("show: path is required")
	}
	var content []byte
	err := c.withMirror(ctx, repo, func(mirror string) error {
		if !hasCommit(ctx, mirror, sha) {
			if _, err := fetch(ctx, mirror, repo, repo, Ref{Name: sha, FullName: sha, SHA: sha, Kind: RefSHA}, 0); err != nil {
				return err
			}
		}
		blob, err := blobAt(ctx, mirror, sha, path)
		if err != nil {
			return fmt.Errorf("%s: %w", repo.URL.Redacted(), err)
		}
		content, err = runBytes(ctx, command{dir: mirror, args: []string{"cat-file", "blob", "--end-of-options", blob}})
		return err
	})
	return content, err
}

// blobAt finds the blob id of path in commit sha with ls-tree, so a missing
// path is told apart from a failing command without parsing git's stderr.
func blobAt(ctx context.Context, mirror, sha, path string) (string, error) {
	out, err := run(ctx, command{dir: mirror, args: []string{"--literal-pathspecs", "ls-tree", "-z", "--full-tree",
		"--end-of-options", sha, "--", path}})
	if err != nil {
		return "", err
	}
	if out == "" {
		return "", fmt.Errorf("%s at %s: %w", path, sha, ErrPathNotFound)
	}
	entries := strings.Split(strings.TrimSuffix(out, "\x00"), "\x00")
	if len(entries) != 1 {
		return "", fmt.Errorf("%s at %s: expected one tree entry, got %d", path, sha, len(entries))
	}
	// "<mode> SP <type> SP <object> TAB <path>"
	meta, name, _ := strings.Cut(entries[0], "\t")
	fields := strings.Fields(meta)
	if len(fields) != 3 || name != path {
		return "", fmt.Errorf("%s at %s: unexpected ls-tree entry %q", path, sha, entries[0])
	}
	if fields[1] != "blob" {
		return "", fmt.Errorf("%s at %s is a %s, not a file", path, sha, fields[1])
	}
	return fields[2], nil
}
