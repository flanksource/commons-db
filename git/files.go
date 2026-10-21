package git

import (
	"context"
	"fmt"
	"strings"
)

func (c *Cache) Files(ctx context.Context, repo *Repository, sha string) ([]string, error) {
	if !fullSHAPattern.MatchString(sha) {
		return nil, fmt.Errorf("list files: a full 40-character commit SHA is required")
	}
	paths := []string{}
	err := c.withMirror(ctx, repo, func(mirror string) error {
		if !hasCommit(ctx, mirror, sha) {
			if _, err := fetch(ctx, mirror, repo, repo, Ref{Name: sha, FullName: sha, SHA: sha, Kind: RefSHA}, 0); err != nil {
				return err
			}
		}
		output, err := run(ctx, command{dir: mirror, args: []string{"ls-tree", "-r", "-z", "--full-tree", "--end-of-options", sha}})
		if err != nil {
			return err
		}
		for _, entry := range strings.Split(strings.TrimSuffix(output, "\x00"), "\x00") {
			if entry == "" {
				continue
			}
			metadata, path, ok := strings.Cut(entry, "\t")
			fields := strings.Fields(metadata)
			if !ok || len(fields) != 3 {
				return fmt.Errorf("unexpected Git tree entry %q", entry)
			}
			if fields[1] == "blob" && (fields[0] == "100644" || fields[0] == "100755") {
				paths = append(paths, path)
			}
		}
		return nil
	})
	return paths, err
}
