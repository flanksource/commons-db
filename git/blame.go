package git

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// BlameOptions supplies the displayed contents of a file at an immutable commit.
type BlameOptions struct {
	SHA      string
	Path     string
	Contents []byte
}

type BlameAuthor struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

type BlameCommit struct {
	SHA     string      `json:"sha"`
	URL     string      `json:"url,omitempty"`
	Author  BlameAuthor `json:"author"`
	Date    time.Time   `json:"date"`
	Subject string      `json:"subject"`
}

// BlameLine has no Commit when its content is outside the selected revision.
type BlameLine struct {
	Line         int          `json:"line"`
	OriginalLine int          `json:"originalLine"`
	Path         string       `json:"path"`
	Content      string       `json:"content"`
	Commit       *BlameCommit `json:"commit"`
}

func (c *Cache) Blame(ctx context.Context, repo *Repository, options BlameOptions) (lines []BlameLine, err error) {
	if !fullSHAPattern.MatchString(options.SHA) {
		return nil, fmt.Errorf("blame %q: give the full 40-character commit SHA", options.SHA)
	}
	if options.Contents == nil || repo == nil {
		return nil, errors.New("blame: repository and displayed contents are required")
	}
	if _, err := safeJoin(".", options.Path); err != nil {
		return nil, err
	}
	var out []byte
	err = c.withRefs(ctx, repo, refReadOptions{Refs: []refOn{{repo, options.SHA}}, History: true}, func(mirror string, shas []string) error {
		if _, err := blobAt(ctx, mirror, shas[0], options.Path); err != nil {
			return err
		}
		var readErr error
		out, readErr = runBytes(ctx, command{dir: mirror, stdin: options.Contents, args: []string{"blame", "--line-porcelain", "--root", "--contents", "-", shas[0], "--", options.Path}})
		return readErr
	})

	if err != nil {
		return nil, err
	}
	lines, err = parseBlame(out)
	if err != nil {
		return nil, err
	}
	for _, line := range lines {
		if line.Commit == nil {
			continue
		}
		link, err := repo.URL.CommitURL(line.Commit.SHA)
		if err != nil && !errors.Is(err, ErrNoCommitView) {
			return nil, err
		}
		line.Commit.URL = link
	}
	return lines, nil
}
