package git

import (
	"context"
	"fmt"
)

// cachedTagOptions names a tag whose commit prepareRef already made present,
// so fetching the tag ref transfers the tag object only.
type cachedTagOptions struct {
	Paths      mirrorPaths
	Repository *Repository
	Ref        Ref
}

func cachedCloneTag(ctx context.Context, o cachedTagOptions) (string, error) {
	destination := "refs/commons-cache/repos/" + o.Repository.URL.CacheKey() + "/tags/" + o.Ref.Name
	sha, err := fetch(ctx, fetchOptions{Dir: o.Paths.git, Owner: o.Repository, Repo: o.Repository, Ref: o.Ref, Refspecs: []string{"+" + o.Ref.FullName + ":" + destination}})
	if err != nil {
		return "", err
	}
	if sha != o.Ref.SHA {
		return "", fmt.Errorf("tag %s moved from %s to %s", o.Ref.Name, o.Ref.SHA, sha)
	}
	return destination, nil
}
