package git

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"strings"
)

// BlobRequest identifies a literal file path at a full commit SHA.
type BlobRequest struct {
	SHA  string
	Path string
}

// BlobResult distinguishes absent paths from present files with empty content.
type BlobResult struct {
	Content []byte
	Object  string
	Present bool
}

// ReadBlobs reads files in request order using one native Git object batch.
// Directories, invalid revisions and command failures return errors.
func (c *Cache) ReadBlobs(ctx context.Context, repo *Repository, requests []BlobRequest) ([]BlobResult, error) {
	results := make([]BlobResult, len(requests))
	if len(requests) == 0 {
		return results, nil
	}
	objects := make([]string, len(requests))
	refs := []refOn{}
	seen := map[string]bool{}
	for index, request := range requests {
		if !fullSHAPattern.MatchString(request.SHA) || !fs.ValidPath(request.Path) || request.Path == "." || strings.ContainsRune(request.Path, 0) {
			return nil, fmt.Errorf("read blob %q at %q: a full commit SHA and repository-relative file path are required", request.Path, request.SHA)
		}
		objects[index] = request.SHA + ":" + request.Path
		if !seen[request.SHA] {
			refs = append(refs, refOn{repo, request.SHA})
			seen[request.SHA] = true
		}
	}
	err := c.readRefs(ctx, repo, refReadOptions{Refs: refs}, func(mirror string, _ []string) error {
		return readObjectBatch(ctx, objectBatchOptions{Dir: mirror, Objects: objects, Read: func(index int, blob batchBlob, content io.Reader) error {
			if blob.Missing {
				return nil
			}
			raw, err := io.ReadAll(content)
			if err != nil {
				return err
			}
			results[index] = BlobResult{Content: raw, Object: blob.Object, Present: true}
			return nil
		}})
	})
	return results, err
}
