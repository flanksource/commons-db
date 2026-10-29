package git

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
)

func (w *Worktree) localName(name string) string {
	if _, shared := w.repo.Provider.(RemoteHistoryProvider); !shared {
		return name
	}
	return "commons-cache/" + w.repo.URL.CacheKey() + "/" + filepath.Base(w.Dir) + "/" + name
}

func (w *Worktree) createSharedTag(ctx context.Context, o TagOptions) error {
	sha, err := w.head(ctx)
	if err != nil {
		return err
	}
	name := w.localName(o.Name)
	if err := writeNamedTag(ctx, namedTagOptions{Directory: w.Dir, Commit: sha, Name: name, Tag: o}); err != nil {
		return err
	}
	w.tags = append(w.tags, name)
	return nil
}

type namedTagOptions struct {
	Directory string
	Commit    string
	Name      string
	Tag       TagOptions
}

func writeNamedTag(ctx context.Context, options namedTagOptions) error {
	o := options.Tag
	if err := ValidateTagName(o.Name); err != nil {
		return err
	}
	sha := options.Commit
	if o.Message != "" {
		env, err := o.Tagger.env()
		if err != nil {
			return err
		}
		identity, err := run(ctx, command{dir: options.Directory, env: append(baseEnv(), env...), args: []string{"var", "GIT_COMMITTER_IDENT"}})
		if err != nil {
			return err
		}
		body := fmt.Sprintf("object %s\ntype commit\ntag %s\ntagger %s\n\n%s\n", sha, o.Name, identity, strings.TrimRight(o.Message, "\n"))
		sha, err = run(ctx, command{dir: options.Directory, stdin: []byte(body), args: []string{"mktag"}})
		if err != nil {
			return err
		}
	}
	if _, err := run(ctx, command{dir: options.Directory, args: []string{"update-ref", "refs/tags/" + options.Name, sha, strings.Repeat("0", 40)}}); err != nil {
		return err
	}
	return nil
}
