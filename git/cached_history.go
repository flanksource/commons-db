package git

import "context"

func (c *Cache) Blame(ctx context.Context, repo *Repository, options BlameOptions) ([]BlameLine, error) {
	return cachedRead(ctx, c.Root, repo, "blame", options, func(ctx context.Context) ([]BlameLine, error) {
		return c.readBlame(ctx, repo, options)
	})
}

func (c *Cache) Log(ctx context.Context, repo *Repository, options LogOptions) ([]Commit, error) {
	load := func(ctx context.Context) ([]Commit, error) { return c.readLog(ctx, repo, options) }
	if !pinnedPair(options.Base, options.Head) {
		return load(ctx)
	}
	headRepository := ""
	if options.HeadFrom != nil {
		headRepository = options.HeadFrom.CacheKey()
	}
	key := struct {
		Base, Head, HeadRepository string
		Limit                      int
		Paths                      []string
	}{options.Base, options.Head, headRepository, options.Limit, options.Paths}
	return cachedRead(ctx, c.Root, repo, "log", key, load)
}

func (c *Cache) History(ctx context.Context, repo *Repository, ref string, options HistoryOptions) ([]Commit, error) {
	load := func(ctx context.Context) ([]Commit, error) { return c.readHistory(ctx, repo, ref, options) }
	if !fullSHAPattern.MatchString(ref) || options.Since != "" {
		return load(ctx)
	}
	return cachedRead(ctx, c.Root, repo, "history", struct {
		SHA     string
		Options HistoryOptions
	}{ref, options}, load)
}

func (c *Cache) Compare(ctx context.Context, repo *Repository, base, head string) (Comparison, error) {
	load := func(ctx context.Context) (Comparison, error) { return c.readComparison(ctx, repo, base, head) }
	if !pinnedPair(base, head) {
		return load(ctx)
	}
	return cachedRead(ctx, c.Root, repo, "compare", []string{base, head}, load)
}

func (c *Cache) MergeBase(ctx context.Context, repo *Repository, options MergeBaseOptions) (string, error) {
	load := func(ctx context.Context) (string, error) { return c.readMergeBase(ctx, repo, options) }
	if !pinnedPair(options.Base, options.Head) {
		return load(ctx)
	}
	headRepository := ""
	if options.HeadFrom != nil {
		headRepository = options.HeadFrom.CacheKey()
	}
	return cachedRead(ctx, c.Root, repo, "merge-base", []string{options.Base, options.Head, headRepository}, load)
}

func pinnedPair(base, head string) bool {
	return fullSHAPattern.MatchString(base) && fullSHAPattern.MatchString(head)
}
